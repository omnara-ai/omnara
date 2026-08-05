//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestPublicAgentInteractionResolveMarksWakeup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-resolve")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions?state=open",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data, ok := listed["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one public open interaction, got %+v", listed)
	}
	item := data[0].(map[string]any)
	if item["id"] != interactionPublicID ||
		item["interaction_kind"] != "question" ||
		item["state"] != "open" {
		t.Fatalf("unexpected listed interaction: %+v", item)
	}
	queuedInput, _, created, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID: project.ProjectUUID,
			AgentID:   agentID,
			Actor:     httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"blocked"}]`,
			),
			IdempotencyKey: "blocked-" + agentID.String(),
		},
	)
	if err != nil {
		t.Fatalf("queue content input during open interaction: %v", err)
	}
	if !created || queuedInput.State != "received" {
		t.Fatalf("queued input created=%v state=%s, want received", created, queuedInput.State)
	}
	var interactionState string
	if err := pool.QueryRow(
		ctx,
		`SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		project.ProjectUUID,
		agentID,
		interactionID,
	).Scan(&interactionState); err != nil {
		t.Fatalf("query preserved interaction: %v", err)
	}
	if interactionState != "open" {
		t.Fatalf("queued input changed interaction state to %s, want open", interactionState)
	}

	resolved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpQuestionResolutionRequest(0),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	adminActorID := httpOmnaraActorID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	)
	if resolved["id"] != interactionPublicID ||
		resolved["state"] != "resolved" ||
		resolved["resolved_by_input_id"] != httpInteractionResolvingInputPublicID(
			t, ctx, pool, project.ProjectUUID, agentID, interactionID,
		) {
		t.Fatalf("unexpected resolved interaction: %+v", resolved)
	}
	resolvingActorID, resolvingInputKind := interactionResolvingInput(
		t, ctx, pool, project.ProjectUUID, agentID, interactionID,
	)
	if resolvingActorID != adminActorID || resolvingInputKind != "interaction_response" {
		t.Fatalf(
			"resolving input actor=%s kind=%s, want admin omnara actor interaction_response",
			resolvingActorID,
			resolvingInputKind,
		)
	}
	var state string
	var wakeups int
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 `+
		`AND id = $3`, project.ProjectUUID, agentID, interactionID).Scan(&state); err != nil {
		t.Fatalf("query resolved interaction: %v", err)
	}
	if state != "resolved" {
		t.Fatalf("interaction state = %s, want resolved", state)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`,
		project.ProjectUUID,
		agentID,
	).Scan(&wakeups); err != nil {
		t.Fatalf("query wakeup: %v", err)
	}
	if wakeups != 1 {
		t.Fatalf(
			"interaction resolution should mark exactly one agent wakeup, got %d",
			wakeups,
		)
	}
	assertInteractionResponseAgentInput(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	assertInteractionResponseLedgerEvent(t, ctx, pool, project.ProjectUUID, agentID, interactionID)

	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpQuestionResolutionRequest(0),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayed["id"] != interactionPublicID ||
		replayed["state"] != "resolved" {
		t.Fatalf(
			"unexpected idempotent interaction replay response: %+v",
			replayed,
		)
	}
	assertInteractionResponseAgentInput(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpQuestionResolutionRequest(1),
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	assertInteractionResponseAgentInput(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
}

func TestListAgentInteractionsPaginatesAllInteractions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-pages")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	agentID, firstID := createHTTPStructuredQuestionInteraction(t, ctx, pool, store, project.OrgUUID, project.ProjectUUID, now)
	permissionID := copyHTTPInteractionForTest(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		firstID,
		"permission",
		httpPermissionRequest(
			t,
			"ask_question",
			json.RawMessage(
				`{"questions":[{"prompt":"Ship?","options":[`+
					`{"label":"Yes"},{"label":"No"}]}]}`,
			),
		),
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	want := []string{
		testPublicID(t, publicid.KindAgentInteraction, firstID),
		testPublicID(t, publicid.KindAgentInteraction, permissionID),
	}

	got := pageThroughAgentInteractions(
		t,
		handler,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions?state=open",
		project.AdminToken,
		1,
	)
	if len(got) != len(want) {
		t.Fatalf("paged interactions %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("paged interactions %v, want %v", got, want)
		}
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions?cursor=not-a-cursor",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
}

func TestPublicCreateAgentInputPreservesOrExplicitlyCancelsOpenInteraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "input-cancel-interaction")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 10, 0, 0, time.UTC)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	path := project.ProjectPath + "/agents/" + agentPublicID + "/inputs"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"content_blocks":[{"type":"text","text":"invalid queued cancellation"}],`+
			`"cancel_open_interactions":true}`,
		"input-reject-queued-cancellation",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	afterRejected, found, err := store.Execution().GetAgentInteraction(
		ctx,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	if err != nil {
		t.Fatalf("get interaction after rejected queued input: %v", err)
	}
	if !found || afterRejected.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("rejected queued input changed interaction: %+v", afterRejected)
	}
	body := `{"content_blocks":[{"type":"text","text":"continue instead"}]}`
	preserved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		body,
		"input-preserve-interaction",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if preserved["agent_input"].(map[string]any)["state"] != "received" {
		t.Fatalf("queued input should remain received: %+v", preserved)
	}
	if _, exists := preserved["agent_input"].(map[string]any)["cancel_open_interactions"]; exists {
		t.Fatalf("agent input response exposed transient cancellation option: %+v", preserved)
	}
	beforeCancel, found, err := store.Execution().GetAgentInteraction(
		ctx,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	if err != nil {
		t.Fatalf("get preserved interaction: %v", err)
	}
	if !found || beforeCancel.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("ordinary input should preserve the interaction: %+v", beforeCancel)
	}
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"content_blocks":[{"type":"text","text":"continue instead"}],`+
			`"delivery_mode":"steering","cancel_open_interactions":true}`,
		"input-cancel-interaction",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	input := created["agent_input"].(map[string]any)
	if input["state"] != "received" ||
		input["delivery_mode"] != string(executionstore.DeliveryModeSteering) {
		t.Fatalf("created input=%+v want received steering input", input)
	}
	if _, exists := input["cancel_open_interactions"]; exists {
		t.Fatalf("agent input response exposed transient cancellation option: %+v", input)
	}
	interaction, found, err := store.Execution().GetAgentInteraction(
		ctx,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	if err != nil {
		t.Fatalf("get canceled interaction: %v", err)
	}
	adminActorID := httpOmnaraActorID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	)
	if !found || interaction.State != executionstore.AgentInteractionStateCanceled ||
		interaction.ResolvedByInputID == storage.NilID {
		t.Fatalf("canceled interaction found=%v interaction=%+v", found, interaction)
	}
	supersedingActorID, supersedingInputKind := interactionResolvingInput(
		t, ctx, pool, project.ProjectUUID, agentID, interactionID,
	)
	if supersedingActorID != adminActorID || supersedingInputKind != "content" {
		t.Fatalf(
			"superseding input actor=%s kind=%s, want admin omnara actor content",
			supersedingActorID,
			supersedingInputKind,
		)
	}
	var toolState, toolOutcome string
	if err := pool.QueryRow(
		ctx,
		`SELECT tool_call.state, result.outcome
			 FROM tool_call_read_projection tool_call
			 JOIN tool_call_results result
			   ON result.agent_id = tool_call.agent_id
		  AND result.tool_call_id = tool_call.id
		 WHERE tool_call.project_id = $1
		   AND tool_call.agent_id = $2
		   AND tool_call.id = $3`,
		project.ProjectUUID,
		agentID,
		interaction.ToolCallID,
	).Scan(&toolState, &toolOutcome); err != nil {
		t.Fatalf("query superseded tool call: %v", err)
	}
	if toolState != "completed" || toolOutcome != "canceled" {
		t.Fatalf(
			"superseded tool call state=%s outcome=%s, want completed/canceled",
			toolState,
			toolOutcome,
		)
	}
	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"content_blocks":[{"type":"text","text":"continue instead"}],`+
			`"delivery_mode":"steering","cancel_open_interactions":true}`,
		"input-cancel-interaction",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayed["agent_input"].(map[string]any)["id"] != input["id"] {
		t.Fatalf("replayed input=%+v want id %v", replayed, input["id"])
	}
}

func TestPublicPromotionCanCancelOpenInteraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "promote-cancel-interaction")
	store := newIntegrationStore(pool)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		time.Date(2026, 5, 16, 12, 15, 0, 0, time.UTC),
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	path := project.ProjectPath + "/agents/" + agentPublicID + "/inputs"
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"content_blocks":[{"type":"text","text":"continue without waiting"}]}`,
		"promote-cancel-interaction-input",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	inputID := created["agent_input"].(map[string]any)["id"].(string)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path+"/"+inputID+"/promote_to_steering",
		`{"cancel_open_interactions":true}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)

	interaction, found, err := store.Execution().GetAgentInteraction(
		ctx,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	if err != nil {
		t.Fatalf("get interaction after queued input promotion: %v", err)
	}
	wantInputID, err := publicid.Decode(publicid.KindAgentInput, inputID)
	if err != nil {
		t.Fatalf("decode promoted input ID: %v", err)
	}
	if !found || interaction.State != executionstore.AgentInteractionStateCanceled || interaction.ResolvedByInputID != wantInputID {
		t.Fatalf("interaction after queued input promotion = %+v found=%v", interaction, found)
	}
}

func TestPublicAgentInteractionResolveRejectsMissingAndCanceledTargets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-missing-canceled")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 20, 0, 0, time.UTC)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	missingInteractionID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		httpTestID("missing_interaction_target"),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+missingInteractionID+"/resolve",
		httpQuestionResolutionRequest(0),
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	if _, err := store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: project.ProjectUUID,
			AgentID:   agentID,
			Actor:     httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
		},
	); err != nil {
		t.Fatalf("cancel agent with open interaction: %v", err)
	}
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpQuestionResolutionRequest(0),
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	assertNoInteractionResponseAgentInput(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	assertNoInteractionLedgerEvents(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
}

func TestPublicCancelAgentTerminalizesOpenInteractionToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "public-cancel-interaction-tool")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 22, 0, 0, time.UTC)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"",
		json.RawMessage(
			`{"tool_name":"run_command","command_resolution":{"command":"printf waiting",`+
				`"shell_selector":"default","wait_ms":600}}`,
		),
	)
	interaction, found, err := store.Execution().GetAgentInteraction(
		ctx,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	if err != nil {
		t.Fatalf("get interaction before cancel: %v", err)
	}
	if !found {
		t.Fatal("expected interaction before cancel")
	}
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)

	canceled := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/cancel",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	event := canceled["event"].(map[string]any)
	if event["event_kind"] != "agent_input" ||
		event["input_kind"] != "control" ||
		event["control_type"] != "cancel_current" {
		t.Fatalf("unexpected public cancel event: %+v", canceled)
	}
	if canceled["runtime_cancel_requested"] != true {
		t.Fatalf(
			"public cancel should request active runtime cancellation, got %+v",
			canceled,
		)
	}
	if canceled["actor_id"] != httpOmnaraActorPublicID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	) {
		t.Fatalf(
			"public cancel should echo the authenticated user's actor, got %+v",
			canceled,
		)
	}

	var interactionState string
	if err := pool.QueryRow(
		ctx,
		`SELECT state FROM `+
			`agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		project.ProjectUUID,
		agentID,
		interactionID,
	).Scan(&interactionState); err != nil {
		t.Fatalf("query canceled interaction state: %v", err)
	}
	if interactionState != "canceled" {
		t.Fatalf(
			"interaction state after public cancel = %s, want canceled",
			interactionState,
		)
	}
	adminActorID := httpOmnaraActorID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	)
	cancelingActorID, cancelingInputKind := interactionResolvingInput(
		t, ctx, pool, project.ProjectUUID, agentID, interactionID,
	)
	if cancelingActorID != adminActorID || cancelingInputKind != "control" {
		t.Fatalf(
			"canceling input actor=%s kind=%s, want admin omnara actor control",
			cancelingActorID,
			cancelingInputKind,
		)
	}
	var typedEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(event.id)
		FROM tool_call_results result
		JOIN tool_call_read_projection tool_call
		  ON tool_call.agent_id = result.agent_id
		 AND tool_call.id = result.tool_call_id
		JOIN agent_events event ON event.agent_id = result.agent_id
	  AND event.tool_call_result_id = result.id
	  AND event.event_kind = 'tool_result'
		WHERE tool_call.project_id = $1
	  AND result.agent_id = $2
	  AND result.tool_call_id = $3
	`, project.ProjectUUID, agentID, interaction.ToolCallID).Scan(&typedEvents); err != nil {
		t.Fatalf("query canceled tool result authority: %v", err)
	}
	if typedEvents != 1 {
		t.Fatalf("canceled tool result authority events=%d", typedEvents)
	}
	var toolState, toolOutcome string
	if err := pool.QueryRow(ctx, `
SELECT tool_call.state, result.outcome
	FROM tool_call_read_projection tool_call
	JOIN tool_call_results result
	  ON result.agent_id = tool_call.agent_id
 AND result.tool_call_id = tool_call.id
WHERE tool_call.project_id = $1
  AND tool_call.agent_id = $2
  AND tool_call.id = $3`, project.ProjectUUID, agentID, interaction.ToolCallID).Scan(&toolState, &toolOutcome); err != nil {
		t.Fatalf("query canceled tool call: %v", err)
	}
	if toolState != "completed" || toolOutcome != "canceled" {
		t.Fatalf(
			"canceled permission tool call state=%s outcome=%s, want completed/canceled",
			toolState,
			toolOutcome,
		)
	}
	var wakeups int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`,
		project.ProjectUUID,
		agentID,
	).Scan(&wakeups); err != nil {
		t.Fatalf("query cancel wakeups: %v", err)
	}
	if wakeups != 0 {
		t.Fatalf(
			"public cancel should leave no runnable wakeups, got %d",
			wakeups,
		)
	}
}

func TestPublicArchiveAgentCancelsOpenInteractionToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "public-delete-interaction-tool")
	store := storage.NewStore(pool)
	now := time.Date(2026, 5, 16, 12, 23, 0, 0, time.UTC)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"",
		json.RawMessage(
			`{"tool_name":"run_command","command_resolution":{"command":"printf waiting","shell_selector":"default","wait_ms":600}}`,
		),
	)
	interaction, found, err := store.Execution().GetAgentInteraction(ctx, project.ProjectUUID, agentID, interactionID)
	if err != nil {
		t.Fatalf("get interaction before delete: %v", err)
	}
	if !found {
		t.Fatal("expected interaction before delete")
	}
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)

	archived := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/archive",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if archived["agent"].(map[string]any)["state"] != "archived" {
		t.Fatalf("archive response = %+v, want archived state", archived)
	}
	readBack := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if readBack["agent"].(map[string]any)["state"] != "archived" {
		t.Fatalf("archived agent read = %+v, want archived state", readBack)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/archive",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)

	var agentState, interactionState string
	if err := pool.QueryRow(ctx, `SELECT state FROM agents WHERE project_id = $1 AND id = $2`, project.ProjectUUID, agentID).
		Scan(&agentState); err != nil {
		t.Fatalf("query archived agent: %v", err)
	}
	if agentState != "archived" {
		t.Fatalf("agent state after delete = %s, want archived", agentState)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 AND id = $3`, project.ProjectUUID, agentID, interactionID).
		Scan(&interactionState); err != nil {
		t.Fatalf("query canceled interaction state: %v", err)
	}
	if interactionState != "canceled" {
		t.Fatalf("interaction state after delete = %s, want canceled", interactionState)
	}
	adminActorID := httpOmnaraActorID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	)
	cancelingActorID, cancelingInputKind := interactionResolvingInput(
		t, ctx, pool, project.ProjectUUID, agentID, interactionID,
	)
	if cancelingActorID != adminActorID || cancelingInputKind != "control" {
		t.Fatalf(
			"canceling input actor=%s kind=%s, want admin omnara actor control",
			cancelingActorID,
			cancelingInputKind,
		)
	}
	var typedEvents int
	if err := pool.QueryRow(ctx, `
	SELECT count(event.id)
	FROM tool_call_results result
	JOIN tool_call_read_projection tool_call
	  ON tool_call.agent_id = result.agent_id
	 AND tool_call.id = result.tool_call_id
	JOIN agent_events event ON event.agent_id = result.agent_id
  AND event.tool_call_result_id = result.id
  AND event.event_kind = 'tool_result'
	WHERE tool_call.project_id = $1
  AND result.agent_id = $2
  AND result.tool_call_id = $3
`, project.ProjectUUID, agentID, interaction.ToolCallID).Scan(&typedEvents); err != nil {
		t.Fatalf("query canceled tool result authority: %v", err)
	}
	if typedEvents != 1 {
		t.Fatalf("canceled tool result authority events=%d", typedEvents)
	}
	var toolState, toolOutcome string
	if err := pool.QueryRow(ctx, `
SELECT tool_call.state, result.outcome
	FROM tool_call_read_projection tool_call
	JOIN tool_call_results result
	  ON result.agent_id = tool_call.agent_id
 AND result.tool_call_id = tool_call.id
WHERE tool_call.project_id = $1
  AND tool_call.agent_id = $2
  AND tool_call.id = $3`, project.ProjectUUID, agentID, interaction.ToolCallID).Scan(&toolState, &toolOutcome); err != nil {
		t.Fatalf("query canceled tool call: %v", err)
	}
	if toolState != "completed" || toolOutcome != "canceled" {
		t.Fatalf(
			"canceled permission tool call state=%s outcome=%s, want completed/canceled",
			toolState,
			toolOutcome,
		)
	}
	var wakeups int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, project.ProjectUUID, agentID).
		Scan(&wakeups); err != nil {
		t.Fatalf("query delete wakeups: %v", err)
	}
	if wakeups != 0 {
		t.Fatalf("delete should leave no runnable wakeups, got %d", wakeups)
	}
}

func TestPublicCancelAgentNoOpResponseShape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "public-cancel-noop")
	launch := launchPublicHTTPAgent(t, handler, project, "public-cancel-noop", project.AdminToken, http.StatusCreated)
	agentID := launch["agent"].(map[string]any)["id"].(string)

	canceled := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/cancel",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if canceled["event"] != nil || canceled["runtime_cancel_requested"] != false || canceled["affected"] != false {
		t.Fatalf("idle public cancel response = %+v, want nil event and false flags", canceled)
	}
	if canceled["actor_id"] != httpOmnaraActorPublicID(
		t,
		ctx,
		newIntegrationStore(pool),
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	) {
		t.Fatalf(
			"idle public cancel should still echo the authenticated user's actor, got %+v",
			canceled,
		)
	}
}

func TestAgentInteractionConcurrentConflictingResolutionSerializes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-concurrent")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 25, 0, 0, time.UTC)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
	)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, answer := range []int{0, 1} {
		answer := answer
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
				ProjectID:  project.ProjectUUID,
				AgentID:    agentID,
				ID:         interactionID,
				Resolution: httpQuestionResolution(answer),
				Actor:      httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, conflicted int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, storeerr.ErrIdempotencyConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent resolve error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf(
			"concurrent resolves succeeded=%d conflicted=%d, want one of each",
			succeeded,
			conflicted,
		)
	}
	assertInteractionResponseAgentInput(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	assertInteractionResponseLedgerEvent(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
}

func TestAgentInteractionCancelAndResolveRaceSerializes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-cancel-resolve-race")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 30, 0, 0, time.UTC)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
	)

	start := make(chan struct{})
	cancelErr := make(chan error, 1)
	resolveErr := make(chan error, 1)
	go func() {
		<-start
		_, err := store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
			ProjectID: project.ProjectUUID,
			AgentID:   agentID,
			Actor:     httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
		})
		cancelErr <- err
	}()
	go func() {
		<-start
		_, err := store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
			ProjectID:  project.ProjectUUID,
			AgentID:    agentID,
			ID:         interactionID,
			Resolution: httpQuestionResolution(0),
			Actor:      httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
		})
		resolveErr <- err
	}()
	close(start)
	if err := <-cancelErr; err != nil {
		t.Fatalf("cancel in race: %v", err)
	}
	err := <-resolveErr
	if err != nil && !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("resolve in race: %v", err)
	}

	var state string
	if scanErr := pool.QueryRow(ctx, `SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 `+
		`AND id = $3`, project.ProjectUUID, agentID, interactionID).Scan(&state); scanErr != nil {
		t.Fatalf("query raced interaction: %v", scanErr)
	}
	var responseInputs int
	if scanErr := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND `+
			`input_kind = 'interaction_response' AND target_interaction_id = $3`,
		project.ProjectUUID,
		agentID,
		interactionID,
	).Scan(&responseInputs); scanErr != nil {
		t.Fatalf("query raced interaction response inputs: %v", scanErr)
	}
	switch {
	case state == "resolved" && err == nil && responseInputs == 1:
		assertInteractionResponseLedgerEvent(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	case state == "canceled" && errors.Is(err, storeerr.ErrIdempotencyConflict) && responseInputs == 0:
		assertNoInteractionLedgerEvents(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	default:
		t.Fatalf(
			"race produced partial state: interaction_state=%s resolve_err=%v response_inputs=%d",
			state,
			err,
			responseInputs,
		)
	}
}

func TestPublicAgentInteractionResolveValidatesStructuredQuestionPayload(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-bad-payload")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)

	resp := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		`{"answers":[{"option_indices":[99]}]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if resp["code"] != string(openapi.ErrorCodeInvalidRequest) {
		t.Fatalf("unexpected validation response code: %+v", resp)
	}
	if resp["error"] != `invalid request: interaction form question 0 has no option at index 99` {
		t.Fatalf("unexpected validation response: %+v", resp)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		`{"state":"canceled"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	interaction, found, err := store.Execution().GetAgentInteraction(
		ctx,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	if err != nil || !found || interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("interaction after invalid response = found %v record %+v err %v", found, interaction, err)
	}
	assertNoInteractionLedgerEvents(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
}

func TestPublicAgentInteractionResolvePermissionApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-permission")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"",
		json.RawMessage(
			`{"tool_name":"run_command","command_resolution":{"command":"printf proposed",`+
				`"shell_selector":"default","wait_ms":600}}`,
		),
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions?state=open",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data, ok := listed["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one public permission interaction, got %+v", listed)
	}
	resolved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpPermissionResolutionRequest(toolpermission.AllowOptionIndex, ""),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if resolved["state"] != "resolved" ||
		resolved["resolved_by_input_id"] != httpInteractionResolvingInputPublicID(
			t, ctx, pool, project.ProjectUUID, agentID, interactionID,
		) {
		t.Fatalf("unexpected permission approval response: %+v", resolved)
	}
	approvingActorID, _ := interactionResolvingInput(
		t, ctx, pool, project.ProjectUUID, agentID, interactionID,
	)
	if approvingActorID != httpOmnaraActorID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	) {
		t.Fatalf("approving actor = %s, want admin omnara actor", approvingActorID)
	}
	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpPermissionResolutionRequest(toolpermission.AllowOptionIndex, ""),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayed["state"] != "resolved" {
		t.Fatalf("unexpected permission replay response: %+v", replayed)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpPermissionResolutionRequest(
			toolpermission.DenyOptionIndex,
			"changed my mind",
		),
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 `+
		`AND id = $3`, project.ProjectUUID, agentID, interactionID).Scan(&state); err != nil {
		t.Fatalf("query permission interaction: %v", err)
	}
	if state != "resolved" {
		t.Fatalf("permission state = %s, want resolved", state)
	}
	var toolState string
	if err := pool.QueryRow(ctx, `
SELECT tool_call.state
	FROM agent_interaction_read_projection interaction
	JOIN tool_calls tool_call ON tool_call.agent_id = interaction.agent_id
  AND tool_call.id = interaction.tool_call_id
WHERE interaction.project_id = $1
  AND interaction.agent_id = $2
  AND interaction.id = $3
`, project.ProjectUUID, agentID, interactionID).Scan(&toolState); err != nil {
		t.Fatalf("query allowed tool call: %v", err)
	}
	if toolState != "ready" {
		t.Fatalf("allowed tool call state=%s, want ready", toolState)
	}
	assertInteractionResponseAgentInput(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	assertInteractionResponseLedgerEvent(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
}

func TestPublicAgentInteractionResolvePermissionDenial(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-permission-denial")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 10, 0, 0, time.UTC)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"",
		json.RawMessage(
			`{"tool_name":"run_command","command_resolution":{"command":"printf denied",`+
				`"shell_selector":"default","wait_ms":600}}`,
		),
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)

	resolved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpPermissionResolutionRequest(
			toolpermission.DenyOptionIndex,
			"not allowed",
		),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if resolved["state"] != "resolved" ||
		resolved["resolved_by_input_id"] != httpInteractionResolvingInputPublicID(
			t, ctx, pool, project.ProjectUUID, agentID, interactionID,
		) {
		t.Fatalf("unexpected permission denial response: %+v", resolved)
	}
	denyingActorID, _ := interactionResolvingInput(
		t, ctx, pool, project.ProjectUUID, agentID, interactionID,
	)
	if denyingActorID != httpOmnaraActorID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	) {
		t.Fatalf("denying actor = %s, want admin omnara actor", denyingActorID)
	}
	var state string
	var resolution json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT state, resolution FROM agent_interaction_read_projection WHERE project_id = $1 AND `+
		`agent_id = $2 AND id = $3`, project.ProjectUUID, agentID, interactionID).Scan(&state, &resolution); err != nil {
		t.Fatalf("query denied permission interaction: %v", err)
	}
	var decodedResolution interactionform.Resolution
	if err := json.Unmarshal(resolution, &decodedResolution); err != nil {
		t.Fatalf("decode denied permission resolution: %v", err)
	}
	if len(decodedResolution.Answers) != 1 {
		t.Fatalf("denied permission resolution=%s, want one answer", resolution)
	}
	answer := decodedResolution.Answers[0]
	if state != "resolved" || len(answer.OptionIndices) != 1 ||
		answer.OptionIndices[0] != toolpermission.DenyOptionIndex ||
		answer.Text != "not allowed" {
		t.Fatalf("denied permission state=%s resolution=%s", state, resolution)
	}
	assertInteractionResponseAgentInput(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	assertInteractionResponseLedgerEvent(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	var toolState, toolOutcome string
	var resultEvents int
	if err := pool.QueryRow(ctx, `
SELECT tool_call.state, coalesce(result.outcome, ''), count(result_event.id)::int
	FROM agent_interaction_read_projection interaction
	JOIN tool_calls tool_call ON tool_call.agent_id = interaction.agent_id
  AND tool_call.id = interaction.tool_call_id
	LEFT JOIN tool_call_results result ON result.agent_id = tool_call.agent_id
  AND result.tool_call_id = tool_call.id
	LEFT JOIN agent_events result_event ON result_event.agent_id = result.agent_id
  AND result_event.tool_call_result_id = result.id
  AND result_event.event_kind = 'tool_result'
WHERE interaction.project_id = $1
  AND interaction.agent_id = $2
  AND interaction.id = $3
GROUP BY tool_call.state, result.outcome
`, project.ProjectUUID, agentID, interactionID).Scan(&toolState, &toolOutcome, &resultEvents); err != nil {
		t.Fatalf("query denied permission tool call: %v", err)
	}
	if toolState != "completed" || toolOutcome != "denied" ||
		resultEvents != 1 {
		t.Fatalf(
			"denied tool call state=%s outcome=%s result_events=%d, want completed/denied/1",
			toolState,
			toolOutcome,
			resultEvents,
		)
	}
	var wakeups int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`,
		project.ProjectUUID,
		agentID,
	).Scan(&wakeups); err != nil {
		t.Fatalf("query denial wakeup: %v", err)
	}
	if wakeups != 1 {
		t.Fatalf(
			"permission denial should mark one wakeup after storage-owned tool-result admission, got %d",
			wakeups,
		)
	}
}

func TestPublicAgentInteractionRejectsUnknownPermissionOption(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-permission-invalid-decision")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 15, 0, 0, time.UTC)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"",
		json.RawMessage(
			`{"tool_name":"run_command","command_resolution":{"command":"printf invalid",`+
				`"shell_selector":"default","wait_ms":600}}`,
		),
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpPermissionResolutionRequest(2, ""),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id `+
		`= $2 AND id = $3`, project.ProjectUUID, agentID, interactionID).Scan(&state); err != nil {
		t.Fatalf("query invalid permission option: %v", err)
	}
	if state != "open" {
		t.Fatalf("permission interaction state = %s, want open", state)
	}
	assertNoInteractionLedgerEvents(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
}

func TestPermissionApprovalUniquePerToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-permission-unique")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"",
		json.RawMessage(
			`{"tool_name":"run_command","input":{"command":"printf proposed"}}`,
		),
	)
	interaction, found, err := store.Execution().GetAgentInteraction(
		ctx,
		project.ProjectUUID,
		agentID,
		interactionID,
	)
	if err != nil {
		t.Fatalf("get permission interaction: %v", err)
	}
	if !found {
		t.Fatal("expected permission interaction")
	}
	var runtimeLockID storage.ID
	if err := pool.QueryRow(
		ctx,
		`SELECT runtime_lock.id FROM agent_runtime_locks runtime_lock `+
			`JOIN agents agent ON agent.id = runtime_lock.agent_id `+
			`WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2 `+
			`ORDER BY runtime_lock.started_at DESC, runtime_lock.id DESC LIMIT 1`,
		project.ProjectUUID,
		agentID,
	).Scan(&runtimeLockID); err != nil {
		t.Fatalf("query runtime lock: %v", err)
	}
	request, err := toolpermission.ParseRequest(interaction.Request)
	if err != nil {
		t.Fatalf("parse stored permission request: %v", err)
	}
	_, err = store.Execution().CreatePermissionInteraction(ctx, executionstore.CreatePermissionInteractionInput{
		ProjectID:     project.ProjectUUID,
		AgentID:       agentID,
		ToolCallID:    interaction.ToolCallID,
		RuntimeLockID: runtimeLockID,
		Request:       request,
	})
	if !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf(
			"duplicate permission approval error = %v, want ErrIdempotencyConflict",
			err,
		)
	}
}

func TestAgentInteractionListsAndResolvesSetIntegrationTargetPermission(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-set-target")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"set_integration_target",
		json.RawMessage(
			`{"tool_name":"set_integration_target","input":{"target_ref":"slack-abcd"}}`,
		),
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions?state=open",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data, ok := listed["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one set-target permission interaction, got %+v", listed)
	}
	item, ok := data[0].(map[string]any)
	if !ok || item["id"] != interactionPublicID {
		t.Fatalf("unexpected set-target permission interaction: %+v", data[0])
	}
	request, ok := item["request"].(map[string]any)
	if !ok || request["title"] != "Permission requested for set_integration_target" {
		t.Fatalf("unexpected set-target permission request: %+v", item["request"])
	}
	if _, exposed := request["authorization"]; exposed {
		t.Fatalf("public permission request exposed internal authorization: %+v", request)
	}
	resolved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpPermissionResolutionRequest(toolpermission.AllowOptionIndex, ""),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if resolved["id"] != interactionPublicID || resolved["state"] != "resolved" {
		t.Fatalf("unexpected resolved set-target permission: %+v", resolved)
	}
}

func TestPublicAgentInteractionExposesWebToolPermissionRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "interaction-web-permission")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	request := json.RawMessage(
		`{"tool_name":"web_search","input":{"query":"go release notes"}}`,
	)
	agentID, interactionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		now,
		"permission",
		"web_search",
		request,
	)
	agentPublicID := testPublicID(t, publicid.KindAgent, agentID)
	interactionPublicID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		interactionID,
	)

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions?state=open",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data, ok := listed["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf(
			"expected one public web permission interaction, got %+v",
			listed,
		)
	}
	item := data[0].(map[string]any)
	if item["id"] != interactionPublicID ||
		item["interaction_kind"] != "permission" ||
		item["state"] != "open" {
		t.Fatalf("unexpected listed web permission: %+v", item)
	}
	payload := item["request"].(map[string]any)
	if payload["title"] != "Permission requested for web_search" {
		t.Fatalf("web permission payload = %+v", payload)
	}
	if _, exposed := payload["authorization"]; exposed {
		t.Fatalf("public permission request exposed internal authorization: %+v", payload)
	}

	resolved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/interactions/"+interactionPublicID+"/resolve",
		httpPermissionResolutionRequest(toolpermission.AllowOptionIndex, ""),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if resolved["id"] != interactionPublicID ||
		resolved["state"] != "resolved" {
		t.Fatalf("unexpected resolved web permission: %+v", resolved)
	}
	assertInteractionResponseAgentInput(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
	assertInteractionResponseLedgerEvent(t, ctx, pool, project.ProjectUUID, agentID, interactionID)
}

func assertInteractionResponseAgentInput(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, interactionID storage.ID,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_inputs
WHERE project_id = $1
  AND agent_id = $2
  AND input_kind = 'interaction_response'
  AND delivery_mode = 'immediate'
  AND state = 'resolved'
  AND target_interaction_id = $3
  AND admitted_event_id IS NOT NULL
  AND admitted_at IS NOT NULL
`, projectID, agentID, interactionID).
		Scan(&count); err != nil {
		t.Fatalf("query interaction response agent input: %v", err)
	}
	if count != 1 {
		t.Fatalf(
			"interaction resolution should create exactly one resolved immediate agent input, got %d",
			count,
		)
	}
}

func assertNoInteractionResponseAgentInput(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, interactionID storage.ID,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND `+
			`input_kind = 'interaction_response' AND target_interaction_id = $3`,
		projectID,
		agentID,
		interactionID,
	).Scan(&count); err != nil {
		t.Fatalf("query interaction response agent input: %v", err)
	}
	if count != 0 {
		t.Fatalf(
			"late interaction resolution should not create response agent input, got %d",
			count,
		)
	}
}

func assertInteractionResponseLedgerEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, interactionID storage.ID,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
	FROM agent_events event
	JOIN agent_inputs input ON input.agent_id = event.agent_id
	  AND input.admitted_event_id = event.id
	WHERE input.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'agent_input'
  AND input.input_kind = 'interaction_response'
  AND input.target_interaction_id = $3`, projectID, agentID, interactionID).Scan(&count); err != nil {
		t.Fatalf("query interaction response ledger event: %v", err)
	}
	if count != 1 {
		t.Fatalf("interaction resolution should append exactly one turn-attached response event, got %d", count)
	}
}

func assertNoInteractionLedgerEvents(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, interactionID storage.ID,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
	FROM agent_events event
	JOIN agent_inputs input ON input.agent_id = event.agent_id
	  AND input.admitted_event_id = event.id
	WHERE input.project_id = $1
  AND event.agent_id = $2
  AND input.input_kind = 'interaction_response'
  AND input.target_interaction_id = $3`, projectID, agentID, interactionID).Scan(&count); err != nil {
		t.Fatalf("query interaction ledger events: %v", err)
	}
	if count != 0 {
		t.Fatalf(
			"agent interactions should not append root ledger events, got %d",
			count,
		)
	}
}

func createHTTPStructuredQuestionInteraction(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	orgID, projectID storage.ID,
	now time.Time,
) (storage.ID, storage.ID) {
	t.Helper()
	return createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		orgID,
		projectID,
		now,
		"question",
		"",
		json.RawMessage(
			`{"questions":[{"prompt":"Ship?","options":[`+
				`{"label":"Yes"},{"label":"No"}]}]}`,
		),
	)
}

func copyHTTPInteractionForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, sourceID storage.ID,
	kind string,
	request json.RawMessage,
) storage.ID {
	t.Helper()
	var id storage.ID
	if err := pool.QueryRow(ctx, `
	INSERT INTO agent_interactions(
	  agent_id,
  tool_call_id,
  interaction_kind,
  state,
  request,
  created_at
)
	SELECT agent_id,
       tool_call_id,
       $4,
       'open',
       $5::jsonb,
	       interaction.created_at + INTERVAL '1 microsecond'
	FROM agent_interaction_read_projection interaction
	WHERE project_id = $1 AND agent_id = $2 AND id = $3
RETURNING id`, projectID, agentID, sourceID, kind, string(request)).Scan(&id); err != nil {
		t.Fatalf("copy interaction fixture: %v", err)
	}
	return id
}

func httpPermissionRequest(
	t *testing.T,
	toolName string,
	input json.RawMessage,
) json.RawMessage {
	t.Helper()
	authorization, err := toolpermission.NewAuthorization(
		toolName,
		input,
	)
	if err != nil {
		t.Fatalf("build permission authorization: %v", err)
	}
	mode, ok := toolpermission.FindMode(
		toolpermission.CommonModeDescriptors(),
		toolpermission.ModeAlwaysAsk,
	)
	if !ok {
		t.Fatal("always_ask permission mode missing")
	}
	request, err := toolpermission.NewRequest(
		mode,
		toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		authorization,
		mustHTTPPermissionForm(t, toolName),
	)
	if err != nil {
		t.Fatalf("build permission request: %v", err)
	}
	return mustHTTPJSON(request)
}

func mustHTTPPermissionForm(
	t *testing.T,
	toolName string,
) interactionform.Form {
	t.Helper()
	value, err := toolpermission.NewAllowDenyForm(
		"Permission requested for "+toolName,
		nil,
	)
	if err != nil {
		t.Fatalf("build permission interaction form: %v", err)
	}
	return value
}

func pageThroughAgentInteractions(t *testing.T, handler http.Handler, path, token string, limit int) []string {
	t.Helper()
	got := make([]string, 0)
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		pagePath := path + "&limit=" + int64String(int64(limit))
		if cursor != "" {
			pagePath += "&cursor=" + cursor
		}
		page := requestJSONWithHeaders(t, handler, http.MethodGet, pagePath, "", "", http.StatusOK, authHeaders(token))
		rows := page["data"].([]any)
		if len(rows) > limit {
			t.Fatalf("interaction page returned %d rows, want <= %d: %+v", len(rows), limit, page)
		}
		for _, raw := range rows {
			id := raw.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("interaction pagination returned duplicate id %s; got=%v", id, got)
			}
			seen[id] = true
			got = append(got, id)
		}
		if pages > 10 {
			t.Fatalf("interaction pagination did not terminate; got=%v", got)
		}
		next, ok := page["next_cursor"]
		if !ok {
			t.Fatalf("interaction response missing next_cursor: %+v", page)
		}
		if next == nil {
			break
		}
		cursor = next.(string)
	}
	return got
}

func createHTTPInteractionAuthority(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	orgID, projectID storage.ID,
	now time.Time,
	kind, permissionToolName string,
	request json.RawMessage,
) (storage.ID, storage.ID) {
	t.Helper()
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "interaction-" + kind + "-" + permissionToolName + "@example.com",
		DisplayName: "Interaction " + kind + " " + permissionToolName,
	},
	)
	if err != nil {
		t.Fatalf("create interaction user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  orgID,
		UserID: user.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add interaction user org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     orgID,
		ProjectID: projectID,
		UserID:    user.ID,
		Role:      "operator",
	}); err != nil {
		t.Fatalf("add interaction user membership: %v", err)
	}
	launch := createHTTPRuntimeAgent(
		t,
		ctx,
		store,
		orgID,
		projectID,
		user.ID,
		"interaction-"+kind+"-"+permissionToolName,
	)
	agent := launch.Agent
	agentID := agent.ID
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID: projectID,
			AgentID:   agentID,
			Actor:     httpOmnaraActorParams(t, orgID, user.ID),
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"ask me"}]`,
			),
			IdempotencyKey: "msg-" + agentID.String(),
		},
	)
	if err != nil {
		t.Fatalf("create interaction input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(
		ctx,
		httpTestClaimInput(),
	)
	if err != nil {
		t.Fatalf("claim interaction input: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || len(claim.Model.AdmittedInputTurn.Inputs) != 1 ||
		claim.Model.AdmittedInputTurn.Inputs[0].ID != input.ID {
		t.Fatalf(
			"claim interaction input found=%v executable=%v input=%+v want %s",
			found,
			claim.Kind == executionstore.AgentWorkModel,
			claim.Model.AdmittedInputTurn.Inputs,
			input.ID,
		)
	}
	runtime := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(ctx, projectID, agentID, admitted.Events[0].Sequence)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	modelCall := claimNormalModelCallForHTTPTest(
		t,
		ctx,
		store,
		projectID,
		agentID,
		runtime,
		[]storage.ID{input.ID},
		snapshot.AgentConfig.ID,
		admitted.Events[0].Sequence,
	)
	modelContext := modelCall.Context
	providerResponseID := "resp_" + agentID.String()
	toolName := "ask_question"
	if kind == "permission" {
		toolName = permissionToolName
		if toolName == "" {
			toolName = "run_command"
		}
	}
	providerCallID := "call_" + agentID.String()
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"http-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         providerResponseID,
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls(
				[]model.ToolCall{{ID: providerCallID, Name: toolName, Input: request}},
			),
		},
	)
	if err != nil {
		t.Fatalf("build interaction provider response: %v", err)
	}
	responseToolCalls := model.ToolCallsFromEnvelope(providerResponse)
	if len(responseToolCalls) != 1 {
		t.Fatalf("interaction provider tool calls = %d, want 1", len(responseToolCalls))
	}
	responseToolCall := responseToolCalls[0]
	source, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          projectID,
			AgentID:            agentID,
			RuntimeLockID:      runtime.ID,
			ModelCallContextID: modelContext.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{
					ProviderCallID: responseToolCall.ID,
					Type:           toolcatalog.ToolTypeBuiltIn,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("record interaction tool source: %v", err)
	}
	if source.ID == storage.NilID || len(calls) != 1 {
		t.Fatalf(
			"unexpected interaction source/calls: event=%+v calls=%+v",
			source,
			calls,
		)
	}
	if kind == "question" {
		if _, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID:     projectID,
			AgentID:       agentID,
			ID:            calls[0].ID,
			RuntimeLockID: runtime.ID,
		}); err != nil {
			t.Fatalf("mark question tool call permission allowed: %v", err)
		}
	}
	if kind == "permission" {
		permissionRequest, err := toolpermission.ParseRequest(
			httpPermissionRequest(t, toolName, request),
		)
		if err != nil {
			t.Fatalf("parse permission request: %v", err)
		}
		interaction, err := store.Execution().CreatePermissionInteraction(
			ctx,
			executionstore.CreatePermissionInteractionInput{
				ProjectID:     projectID,
				AgentID:       agentID,
				ToolCallID:    calls[0].ID,
				RuntimeLockID: runtime.ID,
				Request:       permissionRequest,
			},
		)
		if err != nil {
			t.Fatalf("create permission interaction: %v", err)
		}
		return agentID, interaction.ID
	}
	var payload struct {
		Questions []interactionform.Question `json:"questions"`
	}
	if err := json.Unmarshal(request, &payload); err != nil {
		t.Fatalf("decode question interaction request: %v", err)
	}
	title := "Questions"
	if len(payload.Questions) == 1 {
		title = "Question"
	}
	value, err := interactionform.New(title, nil, payload.Questions)
	if err != nil {
		t.Fatalf("build question interaction: %v", err)
	}
	execution, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     projectID,
			AgentID:       agentID,
			ToolCallID:    calls[0].ID,
			RuntimeLockID: runtime.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateQuestionForToolCall(
				executionstore.CreateQuestionInteractionInput{
					Form: value,
				},
			), nil
		},
	)
	if err != nil {
		t.Fatalf("create question interaction: %v", err)
	}
	interaction, ok := execution.CommandResult.(executionstore.AgentInteractionRecord)
	if !ok {
		t.Fatalf("question command returned %T", execution.CommandResult)
	}
	if err := store.Execution().ReleaseToolCallRuntimeOwnership(
		ctx,
		executionstore.ReleaseToolCallRuntimeOwnershipInput{
			ProjectID:     projectID,
			AgentID:       agentID,
			ToolCallID:    calls[0].ID,
			RuntimeLockID: runtime.ID,
		},
	); err != nil {
		t.Fatalf("release question tool call: %v", err)
	}
	return agentID, interaction.ID
}

func mustHTTPJSON(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func httpQuestionResolution(optionIndex int) interactionform.Resolution {
	return interactionResolution(optionIndex, "")
}

func httpQuestionResolutionRequest(optionIndex int) string {
	return string(mustHTTPJSON(httpQuestionResolution(optionIndex)))
}

func httpPermissionResolutionRequest(
	optionIndex int,
	text string,
) string {
	return string(mustHTTPJSON(interactionResolution(optionIndex, text)))
}

func interactionResolution(
	optionIndex int,
	text string,
) interactionform.Resolution {
	return interactionform.Resolution{
		Answers: []interactionform.Answer{{
			OptionIndices: []int{optionIndex},
			Text:          text,
		}},
	}
}

func interactionResolvingInput(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, interactionID storage.ID,
) (storage.ID, string) {
	t.Helper()
	var actorID storage.ID
	var inputKind string
	if err := pool.QueryRow(ctx, `
SELECT coalesce(input.actor_id, '00000000-0000-0000-0000-000000000000'::uuid), input.input_kind
	FROM agent_interaction_read_projection interaction
JOIN agent_inputs input ON input.project_id = interaction.project_id
  AND input.agent_id = interaction.agent_id
  AND input.id = interaction.resolved_by_input_id
WHERE interaction.project_id = $1 AND interaction.agent_id = $2 AND interaction.id = $3
`, projectID, agentID, interactionID).Scan(&actorID, &inputKind); err != nil {
		t.Fatalf("query interaction resolving input: %v", err)
	}
	return actorID, inputKind
}

func httpInteractionResolvingInputPublicID(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, interactionID storage.ID,
) string {
	t.Helper()
	var inputID storage.ID
	if err := pool.QueryRow(
		ctx,
		`SELECT resolved_by_input_id FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		projectID,
		agentID,
		interactionID,
	).Scan(&inputID); err != nil {
		t.Fatalf("query interaction resolving input id: %v", err)
	}
	return testPublicID(t, publicid.KindAgentInput, inputID)
}

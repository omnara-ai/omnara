//go:build integration

package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestPublicAuthenticatedInputFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	store := newIntegrationStore(pool)
	handler := newIntegrationHTTPHandler(mustNewServer(t, store).Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "input-flow")
	author, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "input-author@example.com", DisplayName: "Author"})
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	other, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "input-other@example.com", DisplayName: "Other"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	viewer, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "input-viewer@example.com", DisplayName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	for _, membership := range []identitystore.AddProjectMembershipInput{
		{OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: author.ID, Role: "developer"},
		{OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: other.ID, Role: "developer"},
		{OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: viewer.ID, Role: "viewer"},
	} {
		if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
			OrgID:  membership.OrgID,
			UserID: membership.UserID,
			Role:   "member",
		}); err != nil {
			t.Fatalf("add org membership: %v", err)
		}
		if _, err := store.Identity().AddProjectMembership(ctx, membership); err != nil {
			t.Fatalf("add project membership: %v", err)
		}
	}
	authorPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:  author.ID,
			Name:    "Author",
			TokenID: "input-author",
		},
	)
	if err != nil {
		t.Fatalf("create author token: %v", err)
	}
	otherPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:  other.ID,
			Name:    "Other",
			TokenID: "input-other",
		},
	)
	if err != nil {
		t.Fatalf("create other token: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:  viewer.ID,
			Name:    "Viewer",
			TokenID: "input-viewer",
		},
	)
	if err != nil {
		t.Fatalf("create viewer token: %v", err)
	}
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"input-flow-author",
		authorPAT.Token,
		http.StatusCreated,
	)
	agentID := launch["agent"].(map[string]any)["id"].(string)

	first := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"first"}]}`,
		"idem-input-first",
		http.StatusCreated,
		authHeaders(authorPAT.Token),
	)
	firstInput := first["agent_input"].(map[string]any)
	if firstInput["state"] != "received" ||
		firstInput["delivery_mode"] != string(executionstore.DeliveryModeQueued) ||
		!publicEventTextEquals(firstInput, "first") {
		t.Fatalf("unexpected created input response: %+v", firstInput)
	}
	if firstInput["actor_id"] != httpOmnaraActorPublicID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		author.ID,
	) {
		t.Fatalf(
			"created input should echo the authenticated user's actor, got %+v",
			firstInput,
		)
	}
	firstInputID := firstInput["id"].(string)
	second := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"second"}]}`,
		"idem-input-second",
		http.StatusCreated,
		authHeaders(authorPAT.Token),
	)
	secondInputID := second["agent_input"].(map[string]any)["id"].(string)
	third := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"third"}],"delivery_mode":"`+string(executionstore.DeliveryModeSteering)+`"}`,
		"idem-input-third",
		http.StatusCreated,
		authHeaders(authorPAT.Token),
	)
	thirdInputID := third["agent_input"].(map[string]any)["id"].(string)
	preAdmissionEvents := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/events",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	preAdmissionData := preAdmissionEvents["data"].([]any)
	if len(preAdmissionData) != 1 ||
		preAdmissionData[0].(map[string]any)["input_kind"] != "config_change" {
		t.Fatalf(
			"received inputs must not appear in event history before admission except initial config change: %+v",
			preAdmissionEvents,
		)
	}
	if preAdmissionEvents["has_more"] != false || jsonInt64(t, preAdmissionEvents["next_after_sequence"]) != 1 {
		t.Fatalf("single event page should expose terminal sequence metadata, got %+v", preAdmissionEvents)
	}

	backlog := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/inputs/backlog",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	backlogData := backlog["data"].([]any)
	if len(backlogData) != 2 ||
		backlogData[0].(map[string]any)["id"] != firstInput["id"] ||
		backlogData[1].(map[string]any)["id"] != secondInputID {
		t.Fatalf(
			"queued backlog should exclude steering input, got %+v",
			backlogData,
		)
	}
	viewerBacklog := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/inputs/backlog",
		"",
		"",
		http.StatusOK,
		authHeaders(viewerPAT.Token),
	)
	if len(viewerBacklog["data"].([]any)) != 2 {
		t.Fatalf(
			"viewer should be able to read queued backlog, got %+v",
			viewerBacklog,
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+firstInputID+"/move",
		`{"position":"front"}`,
		"",
		http.StatusForbidden,
		authHeaders(viewerPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+firstInputID+"/promote_to_steering",
		"",
		"",
		http.StatusForbidden,
		authHeaders(viewerPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+thirdInputID+"/demote_to_queued",
		"",
		"",
		http.StatusForbidden,
		authHeaders(viewerPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+firstInputID+"/cancel",
		"",
		"",
		http.StatusForbidden,
		authHeaders(viewerPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+secondInputID+"/move",
		`{"position":"front"}`,
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	reordered := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/inputs/backlog",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	if reordered["data"].([]any)[0].(map[string]any)["id"] != secondInputID {
		t.Fatalf("expected moved input at front, got %+v", reordered)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+secondInputID+"/promote_to_steering",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	promoted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/inputs/backlog",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	if len(promoted["data"].([]any)) != 1 {
		t.Fatalf("promoted input should leave backlog: %+v", promoted)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+secondInputID+"/demote_to_queued",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+thirdInputID+"/demote_to_queued",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+firstInputID+"/cancel",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs/"+firstInputID+"/cancel",
		"",
		"",
		http.StatusConflict,
		authHeaders(authorPAT.Token),
	)
	afterCancelBacklog := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/inputs/backlog",
		"",
		"",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	for _, item := range afterCancelBacklog["data"].([]any) {
		if item.(map[string]any)["id"] == firstInputID {
			t.Fatalf(
				"canceled input must leave backlog: %+v",
				afterCancelBacklog,
			)
		}
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"sender_type":"agent","content_blocks":[{"type":"text","text":"sender override"}]}`,
		"idem-input-sender-override",
		http.StatusBadRequest,
		authHeaders(authorPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"kind":"input","content_blocks":[{"type":"text","text":"kind override"}]}`,
		"idem-input-kind-override",
		http.StatusBadRequest,
		authHeaders(authorPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[]}`,
		"idem-input-empty-content",
		http.StatusBadRequest,
		authHeaders(authorPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"viewer write"}]}`,
		"idem-input-viewer-write",
		http.StatusForbidden,
		authHeaders(viewerPAT.Token),
	)

	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"first"}]}`,
		"idem-input-first",
		http.StatusOK,
		authHeaders(authorPAT.Token),
	)
	if replayed["agent_input"].(map[string]any)["id"] != firstInputID {
		t.Fatalf("expected idempotent replay, got %+v", replayed)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"first"}]}`,
		"idem-input-first",
		http.StatusConflict,
		authHeaders(otherPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/events?after_sequence=-1",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(viewerPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/events?limit=501",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(viewerPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentID+"/turns?cursor=bad",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(viewerPAT.Token),
	)
}

func TestPublicQueuedInputReorderControlsAdmittedEventOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	store := newIntegrationStore(pool)
	server := mustNewServer(t, store)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "queued-order")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"queued-order",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)

	first := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"first queued"}]}`,
		"idem-queued-order-first",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	second := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"second queued"}]}`,
		"idem-queued-order-second",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	firstID := first["agent_input"].(map[string]any)["id"].(string)
	secondID := second["agent_input"].(map[string]any)["id"].(string)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/inputs/"+secondID+"/move",
		`{"position":"front"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)

	claim, found, err := store.Execution().ClaimNextAgentWork(
		ctx,
		httpTestClaimInput(),
	)
	if err != nil {
		t.Fatalf("claim reordered input: %v", err)
	}
	admitted := claim.Model.AdmittedInputTurn
	secondUUID, err := publicid.Decode(publicid.KindAgentInput, secondID)
	if err != nil {
		t.Fatalf("decode second input id: %v", err)
	}
	if !found || admitted.Inputs[0].ID != secondUUID {
		t.Fatalf(
			"admitted input id=%s found=%v, want reordered front input %s (first was %s)",
			admitted.Inputs[0].ID,
			found,
			secondUUID,
			firstID,
		)
	}

	events := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	eventData := events["data"].([]any)
	if len(eventData) != 2 ||
		eventData[0].(map[string]any)["input_kind"] != "config_change" ||
		!publicEventTextEquals(eventData[1].(map[string]any), "second queued") {
		t.Fatalf(
			"static events should expose reordered input first, got %+v",
			eventData,
		)
	}
	adminActorPublicID := httpOmnaraActorPublicID(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
	)
	if eventData[1].(map[string]any)["actor_id"] != adminActorPublicID {
		t.Fatalf(
			"admitted input event should be attributed to the authenticated user's actor, got %+v",
			eventData[1],
		)
	}
	if eventData[1].(map[string]any)["agent_input_id"] != secondID {
		t.Fatalf(
			"admitted input event should correlate back to input %s, got %+v",
			secondID,
			eventData[1],
		)
	}
	if eventData[1].(map[string]any)["input_idempotency_key"] != "idem-queued-order-second" {
		t.Fatalf(
			"admitted input event should echo the sender's idempotency key, got %+v",
			eventData[1],
		)
	}
	if _, hasKey := eventData[0].(map[string]any)["input_idempotency_key"]; hasKey {
		t.Fatalf(
			"config change event must not echo an idempotency key, got %+v",
			eventData[0],
		)
	}
	turns := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	turnData := turns["data"].([]any)
	if len(turnData) != 2 ||
		!publicEventTextEquals(turnData[0].(map[string]any)["opening_events"].([]any)[0].(map[string]any), "second queued") ||
		turnData[1].(map[string]any)["opening_events"].([]any)[0].(map[string]any)["input_kind"] != "config_change" {
		t.Fatalf("turn opening event should be reordered input, got %+v", turnData)
	}
	if turns["next_before_turn_sequence"] != nil {
		t.Fatalf("full turn page should not advertise older turns, got %+v", turns)
	}
	firstTurnPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns?limit=1",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	firstTurnData := firstTurnPage["data"].([]any)
	if len(firstTurnData) != 1 ||
		jsonInt64(
			t,
			firstTurnPage["next_before_turn_sequence"],
		) != jsonInt64(
			t,
			firstTurnData[0].(map[string]any)["turn_sequence"],
		) {
		t.Fatalf("single turn page should expose older continuation, got %+v", firstTurnPage)
	}
	secondTurnPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns?limit=1&before_turn_sequence="+int64String(
			jsonInt64(t, firstTurnPage["next_before_turn_sequence"]),
		),
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if len(secondTurnPage["data"].([]any)) != 1 || secondTurnPage["next_before_turn_sequence"] != nil {
		t.Fatalf("second turn page should drain the sequence, got %+v", secondTurnPage)
	}

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		httpServer.URL+project.ProjectPath+"/agents/"+agentPublicID+"/events/stream",
		nil,
	)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	for key, value := range authHeaders(project.AdminToken) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Last-Event-ID", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status=%d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || scanner.Text() != ": ok" {
		t.Fatalf("sse stream should flush initial comment before events")
	}
	sawID := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: 1") {
			sawID = true
			continue
		}
		if sawID && strings.HasPrefix(line, "data: ") &&
			strings.Contains(line, "second queued") {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read sse: %v", err)
	}
	t.Fatalf("sse catch-up did not expose reordered input as first event")
}

func TestPublicTurnsEventsAndSSEUseCanonicalEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	store := newIntegrationStore(pool)
	server := mustNewServer(t, store)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "events-content-blocks")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"events-content-blocks",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	agentID, err := publicid.Decode(publicid.KindAgent, agentPublicID)
	if err != nil {
		t.Fatalf("decode agent id: %v", err)
	}

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"visible user input","metadata":{"omnara_hidden":true,"source":{"kind":"test"}}}]}`,
		"idem-visible-input",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	inputPublicID := created["agent_input"].(map[string]any)["id"].(string)
	inputID, err := publicid.Decode(publicid.KindAgentInput, inputPublicID)
	if err != nil {
		t.Fatalf("decode agent input id: %v", err)
	}

	claim, found, err := store.Execution().ClaimNextAgentWork(
		ctx,
		httpTestClaimInput(),
	)
	if err != nil {
		t.Fatalf("claim public input: %v", err)
	}
	admitted := claim.Model.AdmittedInputTurn
	runtime := claim.RuntimeLock
	if !found || admitted.Inputs[0].ID != inputID {
		t.Fatalf("admitted input found=%v id=%s want %s", found, admitted.Inputs[0].ID, inputID)
	}
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		project.ProjectUUID,
		agentID,
		admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	modelCall := claimNormalModelCallForHTTPTest(
		t,
		ctx,
		store,
		project.ProjectUUID,
		agentID,
		runtime,
		[]storage.ID{admitted.Inputs[0].ID},
		snapshot.AgentConfig.ID,
		admitted.Events[0].Sequence,
	)
	contextRow := modelCall.Context
	toolCall := model.ToolCall{
		ID:    "call_events_projection",
		Name:  "read_process",
		Input: json.RawMessage(`{}`),
	}
	providerIdentity := loadModelCallProviderIdentityForHTTPTest(
		t, ctx, store, project.ProjectUUID, modelCall.Context,
	)
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		providerIdentity.Slug,
		providerIdentity.APIFormat,
		providerIdentity.APIVariant,
		model.Response{
			ID: "resp_events_projection",
			Content: append(
				[]model.ResponsePart{{Type: "text", Text: "visible assistant output"}},
				modeltest.ResponsePartsForToolCalls([]model.ToolCall{toolCall})...,
			),
			StopReason: model.StopReasonToolUse,
		},
	)
	if err != nil {
		t.Fatalf("build provider response: %v", err)
	}
	responseToolCalls := model.ToolCallsFromEnvelope(providerResponse)
	if len(responseToolCalls) != 1 {
		t.Fatalf("provider tool calls = %d, want 1", len(responseToolCalls))
	}
	responseToolCall := responseToolCalls[0]
	_, toolCalls, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          project.ProjectUUID,
			AgentID:            agentID,
			RuntimeLockID:      runtime.ID,
			ModelCallContextID: contextRow.ID,
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
		t.Fatalf("record model tool source: %v", err)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(toolCalls))
	}
	if _, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID:     project.ProjectUUID,
		AgentID:       agentID,
		ID:            toolCalls[0].ID,
		RuntimeLockID: runtime.ID,
	}); err != nil {
		t.Fatalf("authorize tool call: %v", err)
	}
	completedToolCall, err := store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          project.ProjectUUID,
		AgentID:            agentID,
		ID:                 toolCalls[0].ID,
		RuntimeLockID:      runtime.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"visible tool result","metadata":{"tool_result":true}}]`),
	})
	if err != nil {
		t.Fatalf("complete tool call: %v", err)
	}

	events := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	eventData := events["data"].([]any)
	if len(eventData) != 4 ||
		eventData[0].(map[string]any)["input_kind"] != "config_change" {
		t.Fatalf(
			"expected user input, model output, and tool result events, got %+v",
			eventData,
		)
	}
	if events["has_more"] != false ||
		jsonInt64(t, events["next_after_sequence"]) != jsonInt64(t, eventData[3].(map[string]any)["sequence"]) {
		t.Fatalf("full event page should expose terminal sequence metadata, got %+v", events)
	}
	firstEventPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events?limit=2",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	firstEventData := firstEventPage["data"].([]any)
	if len(firstEventData) != 2 || firstEventPage["has_more"] != true ||
		jsonInt64(t, firstEventPage["next_after_sequence"]) != jsonInt64(t, firstEventData[1].(map[string]any)["sequence"]) {
		t.Fatalf("first event page should expose forward continuation, got %+v", firstEventPage)
	}
	secondEventPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events?limit=2&after_sequence="+int64String(
			jsonInt64(t, firstEventPage["next_after_sequence"]),
		),
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	secondEventData := secondEventPage["data"].([]any)
	if len(secondEventData) != 2 || secondEventPage["has_more"] != false ||
		jsonInt64(
			t,
			secondEventPage["next_after_sequence"],
		) != jsonInt64(
			t,
			secondEventData[1].(map[string]any)["sequence"],
		) {
		t.Fatalf("second event page should drain forward continuation, got %+v", secondEventPage)
	}
	latestEventPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events?limit=2&before_sequence=0",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	latestEventData := latestEventPage["data"].([]any)
	if len(latestEventData) != 2 || latestEventPage["has_more"] != true ||
		jsonInt64(t, latestEventData[0].(map[string]any)["sequence"]) != jsonInt64(t, eventData[2].(map[string]any)["sequence"]) ||
		jsonInt64(t, latestEventData[1].(map[string]any)["sequence"]) != jsonInt64(t, eventData[3].(map[string]any)["sequence"]) ||
		jsonInt64(t, latestEventPage["next_before_sequence"]) != jsonInt64(t, eventData[2].(map[string]any)["sequence"]) {
		t.Fatalf("latest event page should expose backward continuation, got %+v", latestEventPage)
	}
	olderEventPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events?limit=2&before_sequence="+int64String(
			jsonInt64(t, latestEventPage["next_before_sequence"]),
		),
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	olderEventData := olderEventPage["data"].([]any)
	if len(olderEventData) != 2 || olderEventPage["has_more"] != false ||
		olderEventPage["next_before_sequence"] != nil ||
		jsonInt64(t, olderEventData[0].(map[string]any)["sequence"]) != jsonInt64(t, eventData[0].(map[string]any)["sequence"]) ||
		jsonInt64(t, olderEventData[1].(map[string]any)["sequence"]) != jsonInt64(t, eventData[1].(map[string]any)["sequence"]) {
		t.Fatalf("older event page should drain backward continuation, got %+v", olderEventPage)
	}
	if !publicEventTextEquals(eventData[1].(map[string]any), "visible user input") ||
		!publicEventContainsText(eventData[2].(map[string]any), "visible assistant output") ||
		!publicEventTextEquals(eventData[3].(map[string]any), "visible tool result") {
		t.Fatalf("events should expose canonical content blocks: %+v", eventData)
	}
	inputMetadata := eventData[1].(map[string]any)["content_blocks"].([]any)[0].(map[string]any)["metadata"].(map[string]any)
	inputSource, ok := inputMetadata["source"].(map[string]any)
	if inputMetadata["omnara_hidden"] != true || !ok || inputSource["kind"] != "test" {
		t.Fatalf("input content block metadata = %+v", inputMetadata)
	}
	toolResultMetadata := eventData[3].(map[string]any)["content_blocks"].([]any)[0].(map[string]any)["metadata"].(map[string]any)
	if toolResultMetadata["tool_result"] != true {
		t.Fatalf("tool result content block metadata = %+v", toolResultMetadata)
	}
	contextEvents, err := store.Execution().ListContextEvents(
		ctx,
		project.ProjectUUID,
		agentID,
		0,
		jsonInt64(t, eventData[3].(map[string]any)["sequence"]),
		100,
	)
	if err != nil {
		t.Fatalf("list model context events: %v", err)
	}
	sawInput := false
	for _, contextEvent := range contextEvents {
		contentParts := string(contextEvent.ContentParts)
		if strings.Contains(contentParts, `"metadata"`) {
			t.Fatalf("model context exposed content block metadata: %s", contentParts)
		}
		sawInput = sawInput || strings.Contains(contentParts, "visible user input")
	}
	if !sawInput {
		t.Fatal("model context omitted input content")
	}
	toolCallPublicID, err := publicID(
		publicid.KindToolCall,
		completedToolCall.ID,
	)
	if err != nil {
		t.Fatalf("encode tool call id: %v", err)
	}
	if !publicEventContainsToolCall(
		eventData[2].(map[string]any),
		toolCallPublicID,
	) {
		t.Fatalf(
			"model output should expose public tool_call block id: %+v",
			eventData[2],
		)
	}
	if eventData[2].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("model output should expose its stop reason, got %+v", eventData[2])
	}
	toolResultEvent := eventData[3].(map[string]any)
	if toolResultEvent["event_kind"] != "tool_result" ||
		toolResultEvent["tool_call_id"] != toolCallPublicID ||
		toolResultEvent["outcome"] != "succeeded" {
		t.Fatalf(
			"tool result event should be publicly attributed to the tool call, got %+v",
			toolResultEvent,
		)
	}
	if _, hasActor := toolResultEvent["actor_id"]; hasActor {
		t.Fatalf("tool result event must not carry an actor_id, got %+v", toolResultEvent)
	}
	turns := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	turnData := turns["data"].([]any)
	if len(turnData) != 2 {
		t.Fatalf("expected config and content turns, got %+v", turnData)
	}
	if turns["next_before_turn_sequence"] != nil {
		t.Fatalf("full turn page should not advertise older turns, got %+v", turns)
	}
	turnID := turnData[0].(map[string]any)["id"].(string)
	if len(turnData[0].(map[string]any)["opening_events"].([]any)) != 1 {
		t.Fatalf("turn should include opening events: %+v", turnData[0])
	}
	missingTurnID := testPublicID(t, publicid.KindAgentTurn, httpTestID("missing-turn-events"))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns/"+missingTurnID+"/events",
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	otherLaunch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"events-content-blocks-other",
		project.AdminToken,
		http.StatusCreated,
	)
	otherAgentPublicID := otherLaunch["agent"].(map[string]any)["id"].(string)
	otherTurns := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+otherAgentPublicID+"/turns",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	otherTurnData := otherTurns["data"].([]any)
	if len(otherTurnData) == 0 {
		t.Fatalf("other agent should have a config-change turn, got %+v", otherTurns)
	}
	foreignTurnID := otherTurnData[0].(map[string]any)["id"].(string)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns/"+foreignTurnID+"/events",
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns/"+turnID+"/events?cursor=bad",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	turnEventsPath := project.ProjectPath + "/agents/" + agentPublicID + "/turns/" + turnID + "/events"
	gotTurnEventSequences := pageThroughTurnEventSequences(t, handler, turnEventsPath, project.AdminToken, 1)
	wantTurnEventSequences := []int64{
		jsonInt64(t, eventData[3].(map[string]any)["sequence"]),
		jsonInt64(t, eventData[2].(map[string]any)["sequence"]),
		jsonInt64(t, eventData[1].(map[string]any)["sequence"]),
	}
	assertInt64SliceEqual(t, gotTurnEventSequences, wantTurnEventSequences)

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		httpServer.URL+project.ProjectPath+"/agents/"+agentPublicID+"/events/stream",
		nil,
	)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	for key, value := range authHeaders(project.AdminToken) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status=%d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: 2") {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read sse: %v", err)
	}
	t.Fatalf("sse catch-up did not include event sequence 2")
}

func TestPublicMaxTokensModelOutputReplaysAcrossEventAPIs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	store := newIntegrationStore(pool)
	handler := newIntegrationHTTPHandler(mustNewServer(t, store).Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "max-tokens-event-replay")
	launch := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"max-tokens-event-replay",
		project.AdminToken,
		http.StatusCreated,
	)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	agentID, err := publicid.Decode(publicid.KindAgent, agentPublicID)
	if err != nil {
		t.Fatalf("decode agent id: %v", err)
	}

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentPublicID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"write until the output limit"}]}`,
		"idem-max-tokens-input",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	inputPublicID := created["agent_input"].(map[string]any)["id"].(string)
	inputID, err := publicid.Decode(publicid.KindAgentInput, inputPublicID)
	if err != nil {
		t.Fatalf("decode input id: %v", err)
	}

	work, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	if err != nil {
		t.Fatalf("claim input: %v", err)
	}
	if !found || len(work.Model.AdmittedInputTurn.Inputs) != 1 ||
		work.Model.AdmittedInputTurn.Inputs[0].ID != inputID {
		t.Fatalf("claim did not admit max_tokens test input: found=%v claim=%+v", found, work)
	}
	admitted := work.Model.AdmittedInputTurn
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		project.ProjectUUID,
		agentID,
		admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	modelCall := claimNormalModelCallForHTTPTest(
		t,
		ctx,
		store,
		project.ProjectUUID,
		agentID,
		work.RuntimeLock,
		[]storage.ID{inputID},
		snapshot.AgentConfig.ID,
		admitted.Events[0].Sequence,
	)
	providerIdentity := loadModelCallProviderIdentityForHTTPTest(
		t, ctx, store, project.ProjectUUID, modelCall.Context,
	)
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		providerIdentity.Slug,
		providerIdentity.APIFormat,
		providerIdentity.APIVariant,
		model.Response{
			ID:         "resp_max_tokens_event_replay",
			Content:    []model.ResponsePart{{Type: "text", Text: "partial but durable output"}},
			StopReason: model.StopReasonMaxTokens,
		},
	)
	if err != nil {
		t.Fatalf("build max_tokens provider response: %v", err)
	}
	outputEvent, err := store.Execution().RecordModelOutputAndCompleteContext(
		ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID:          project.ProjectUUID,
			AgentID:            agentID,
			RuntimeLockID:      work.RuntimeLock.ID,
			ModelCallContextID: modelCall.Context.ID,
			ProviderResponse:   providerResponse,
		},
	)
	if err != nil {
		t.Fatalf("record max_tokens model output: %v", err)
	}

	events := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	assertPublicMaxTokensEvent(t, events["data"].([]any))

	turns := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	turnData := turns["data"].([]any)
	if len(turnData) < 1 {
		t.Fatalf("max_tokens output turn missing: %+v", turns)
	}
	turnID := turnData[0].(map[string]any)["id"].(string)
	turnEvents := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/turns/"+turnID+"/events",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	assertPublicMaxTokensEvent(t, turnEvents["data"].([]any))

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	req, err := http.NewRequestWithContext(
		streamCtx,
		http.MethodGet,
		httpServer.URL+project.ProjectPath+"/agents/"+agentPublicID+"/events/stream",
		nil,
	)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	for key, value := range authHeaders(project.AdminToken) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Last-Event-ID", strconv.FormatInt(outputEvent.Sequence-1, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status=%d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") &&
			strings.Contains(line, `"stop_reason":"max_tokens"`) {
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read sse: %v", err)
	}
	t.Fatal("sse catch-up did not replay max_tokens model output")
}

func TestPublicEventStreamDeliversLiveWakeupViaRedis(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)

	redisClient := integrationredis.OpenClient(t)
	bus, err := notifications.NewRedisBus(redisClient, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}
	presence, err := notifications.NewRedisPresenceStore(redisClient)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     bus,
			AgentEventWakeups: bus,
			WorkerControls:    bus,
		},
		presence,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create routed publisher: %v", err)
	}
	t.Cleanup(publisher.Close)

	keyWrapper := integrationKeyWrapper()
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(keyWrapper), storage.WithPostCommitPublisher(publisher))
	server := mustNewServer(
		t,
		store,
		WithSecretKeyWrapper(keyWrapper),
		WithAgentEventWakeupSubscriber(bus),
		WithAgentStreamDeltaSubscriber(bus),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "sse-live-wakeup")
	launch := launchPublicHTTPAgent(t, handler, project, "sse-live-wakeup", project.AdminToken, http.StatusCreated)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)

	initial := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/agents/"+agentPublicID+"/events", "", "", http.StatusOK, authHeaders(project.AdminToken))
	initialData := initial["data"].([]any)
	if len(initialData) != 1 {
		t.Fatalf("expected one initial config change event, got %d", len(initialData))
	}
	startingSeq := int64(1)

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+project.ProjectPath+"/agents/"+agentPublicID+"/events/stream", nil)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	for key, value := range authHeaders(project.AdminToken) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status=%d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), ":") {
		t.Fatalf("sse stream missing preamble: %q", scanner.Text())
	}
	time.Sleep(100 * time.Millisecond)

	postStart := time.Now()
	createInputResp := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents/"+agentPublicID+"/inputs", `{"content_blocks":[{"type":"text","text":"live wakeup payload"}]}`, "idem-sse-live", http.StatusCreated, authHeaders(project.AdminToken))
	_ = createInputResp

	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	if err != nil {
		t.Fatalf("claim agent work: %v", err)
	}
	if !found {
		t.Fatalf("no work claimed for live wakeup")
	}
	admitted := claim.Model.AdmittedInputTurn
	if len(admitted.Events) != 1 || admitted.Events[0].Sequence <= startingSeq {
		t.Fatalf("admission did not append an agent event past startingSeq=%d: %+v", startingSeq, admitted.Events)
	}

	deadline := time.After(5 * time.Second)
	frames := make(chan string, 8)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			frames <- line
		}
	}()
	sawDataFrame := false
	for !sawDataFrame {
		select {
		case line := <-frames:
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "live wakeup payload") {
				sawDataFrame = true
			}
		case <-deadline:
			t.Fatalf("sse did not receive live wakeup frame within 5s; live publish/subscribe path is not working")
		}
	}
	elapsed := time.Since(postStart)
	if elapsed > 3*time.Second {
		t.Logf("warning: live wakeup arrived in %s (publish path may be slow)", elapsed)
	}
}

func TestPublicEventStreamDeliversStreamDeltasViaRedis(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)

	redisClient := integrationredis.OpenClient(t)
	bus, err := notifications.NewRedisBus(redisClient, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}
	presence, err := notifications.NewRedisPresenceStore(redisClient)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     bus,
			AgentEventWakeups: bus,
			WorkerControls:    bus,
		},
		presence,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create routed publisher: %v", err)
	}
	t.Cleanup(publisher.Close)

	keyWrapper := integrationKeyWrapper()
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(keyWrapper), storage.WithPostCommitPublisher(publisher))
	server := mustNewServer(
		t,
		store,
		WithSecretKeyWrapper(keyWrapper),
		WithAgentEventWakeupSubscriber(bus),
		WithAgentStreamDeltaSubscriber(bus),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "sse-stream-delta")
	launch := launchPublicHTTPAgent(t, handler, project, "sse-stream-delta", project.AdminToken, http.StatusCreated)
	agentPublicID := launch["agent"].(map[string]any)["id"].(string)
	agentID, err := publicid.Decode(publicid.KindAgent, agentPublicID)
	if err != nil {
		t.Fatalf("decode agent public id: %v", err)
	}

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		httpServer.URL+project.ProjectPath+"/agents/"+agentPublicID+"/events/stream?stream_deltas=true",
		nil,
	)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	for key, value := range authHeaders(project.AdminToken) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status=%d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), ":") {
		t.Fatalf("sse stream missing preamble: %q", scanner.Text())
	}

	payload := json.RawMessage(`{"model_call_context_id":"mcc_test","seq":1,"event":{"kind":"text_delta","delta":"stream hello"}}`)
	if err := bus.PublishAgentStreamDelta(ctx, agentID, payload); err != nil {
		t.Fatalf("publish stream delta: %v", err)
	}

	deadline := time.After(5 * time.Second)
	frames := make(chan string, 8)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			frames <- line
		}
	}()
	sawEvent := false
	sawData := false
	for !(sawEvent && sawData) {
		select {
		case line := <-frames:
			if line == "event: model_output_delta" {
				sawEvent = true
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "stream hello") {
				sawData = true
			}
		case <-deadline:
			t.Fatalf(
				"sse did not receive stream delta frame within 5s; saw event=%v data=%v",
				sawEvent,
				sawData,
			)
		}
	}
}

func publicEventTextEquals(record map[string]any, want string) bool {
	blocks, ok := record["content_blocks"].([]any)
	if !ok || len(blocks) != 1 {
		return false
	}
	block, ok := blocks[0].(map[string]any)
	return ok && block["type"] == "text" && block["text"] == want
}

func jsonInt64(t *testing.T, value any) int64 {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("value %v (%T) is not a JSON number", value, value)
	}
	return int64(number)
}

func pageThroughTurnEventSequences(t *testing.T, handler http.Handler, path, token string, limit int) []int64 {
	t.Helper()
	got := make([]int64, 0)
	seen := map[int64]bool{}
	before := ""
	for pages := 0; ; pages++ {
		pagePath := path + "?limit=" + strconv.Itoa(limit)
		if before != "" {
			pagePath += "&before_sequence=" + before
		}
		page := requestJSONWithHeaders(t, handler, http.MethodGet, pagePath, "", "", http.StatusOK, authHeaders(token))
		rows := page["data"].([]any)
		if len(rows) > limit {
			t.Fatalf("turn event page returned %d rows, want <= %d: %+v", len(rows), limit, page)
		}
		for _, raw := range rows {
			sequence := jsonInt64(t, raw.(map[string]any)["sequence"])
			if seen[sequence] {
				t.Fatalf("turn event pagination returned duplicate sequence %d; got=%v", sequence, got)
			}
			seen[sequence] = true
			got = append(got, sequence)
		}
		if pages > 10 {
			t.Fatalf("turn event pagination did not terminate; got=%v", got)
		}
		next, ok := page["next_before_sequence"]
		if !ok {
			t.Fatalf("turn event response missing next_before_sequence: %+v", page)
		}
		if next == nil {
			break
		}
		before = int64String(jsonInt64(t, next))
	}
	return got
}

func assertInt64SliceEqual(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got sequences %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got sequences %v, want %v", got, want)
		}
	}
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}

func publicEventContainsText(record map[string]any, want string) bool {
	blocks, ok := record["content_blocks"].([]any)
	if !ok {
		return false
	}
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if ok && block["type"] == "text" && block["text"] == want {
			return true
		}
	}
	return false
}

func publicEventContainsToolCall(record map[string]any, wantID string) bool {
	blocks, ok := record["content_blocks"].([]any)
	if !ok {
		return false
	}
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if ok && block["type"] == "tool_call" &&
			block["tool_call_id"] == wantID {
			return true
		}
	}
	return false
}

func assertPublicMaxTokensEvent(t *testing.T, records []any) {
	t.Helper()
	for _, raw := range records {
		record := raw.(map[string]any)
		if record["event_kind"] == "model_output" &&
			record["stop_reason"] == "max_tokens" &&
			publicEventContainsText(record, "partial but durable output") {
			return
		}
	}
	t.Fatalf("max_tokens model output missing from public events: %+v", records)
}

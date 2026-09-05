//go:build integration

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCronTriggerFiringAndCascade(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()))
	service := crontrigger.NewService(store.Execution(), nil, testLogger())

	project := bootstrapPublicHTTPProject(t, handler, "cron-fire")
	profile := createPublicHTTPAgent(t, handler, project, "cron-fire-profile", project.AdminToken)
	profileID := profile["id"].(string)
	launch := launchPublicHTTPAgent(t, handler, project, "cron-fire-agent", project.AdminToken, http.StatusCreated)
	agentID := launch["agent"].(map[string]any)["id"].(string)
	triggersPath := project.ProjectPath + "/cron-triggers"

	profileTrigger := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"daily-triage","target":{"type":"profile","agent_profile_id":"`+profileID+`"},`+
			`"cron":"0 9 * * *","message_template":"Triage {{ .trigger.name }} since [{{ .trigger.last_fired_at }}]."}`,
		"idem-cron-fire-profile-trigger",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	profileTriggerID := profileTrigger["id"].(string)
	agentTrigger := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"agent-nudge","target":{"type":"agent","agent_id":"`+agentID+`"},`+
			`"cron":"0 9 * * *","message_template":"Nudge {{ .trigger.name }}."}`,
		"idem-cron-fire-agent-trigger",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	agentTriggerID := agentTrigger["id"].(string)
	if agentTrigger["target"].(map[string]any)["delivery_mode"] != "queued" {
		t.Fatalf("expected default agent delivery_mode queued: %+v", agentTrigger)
	}

	idleStats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire with nothing due: %v", err)
	}
	if idleStats.Claimed != 0 {
		t.Fatalf("expected no due triggers before backdating, got %+v", idleStats)
	}

	for _, publicTriggerID := range []string{profileTriggerID, agentTriggerID} {
		triggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, publicTriggerID)
		if _, err := pool.Exec(
			ctx,
			"UPDATE cron_triggers SET next_fire_after = transaction_timestamp() - interval '1 minute' WHERE id = $1",
			triggerUUID,
		); err != nil {
			t.Fatalf("backdate cron trigger %s: %v", publicTriggerID, err)
		}
	}

	stats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire due triggers: %v", err)
	}
	if stats.Claimed != 2 || stats.Launched != 1 || stats.Inputs != 1 ||
		stats.Failures != 0 || stats.Disabled != 0 {
		t.Fatalf("unexpected fire stats: %+v", stats)
	}

	fired := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+profileTriggerID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if fired["last_fired_at"] == nil {
		t.Fatalf("fired trigger should have last_fired_at: %+v", fired)
	}
	if fired["next_fire_at"] == nil {
		t.Fatalf("fired trigger should have next_fire_at rescheduled: %+v", fired)
	}

	assertCronBacklogMessage(t, ctx, pool, handler, project, agentID, "Nudge agent-nudge.")

	agents := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	agentRows := agents["data"].([]any)
	if len(agentRows) != 2 {
		t.Fatalf("profile trigger should launch one new agent, got %+v", agentRows)
	}
	launchedAgentID := ""
	for _, raw := range agentRows {
		row := raw.(map[string]any)
		if row["id"] != agentID {
			launchedAgentID = row["id"].(string)
		}
	}
	if launchedAgentID == "" {
		t.Fatalf("could not find cron-launched agent in %+v", agentRows)
	}
	assertCronBacklogMessage(
		t,
		ctx,
		pool,
		handler,
		project,
		launchedAgentID,
		"Triage daily-triage since [].",
	)

	repeatStats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("refire after claim: %v", err)
	}
	if repeatStats.Claimed != 0 {
		t.Fatalf("claimed triggers must not refire before their next schedule: %+v", repeatStats)
	}

	profileTriggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, profileTriggerID)
	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET next_fire_after = transaction_timestamp() - interval '1 minute',"+
			" claimed_until = transaction_timestamp() + interval '5 minutes' WHERE id = $1",
		profileTriggerUUID,
	); err != nil {
		t.Fatalf("lease cron trigger: %v", err)
	}
	leasedStats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire with active lease: %v", err)
	}
	if leasedStats.Claimed != 0 {
		t.Fatalf("leased trigger must not be claimable: %+v", leasedStats)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET claimed_until = transaction_timestamp() - interval '1 second' WHERE id = $1",
		profileTriggerUUID,
	); err != nil {
		t.Fatalf("expire cron trigger lease: %v", err)
	}
	expiredStats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire after lease expiry: %v", err)
	}
	if expiredStats.Claimed != 1 || expiredStats.Launched != 1 {
		t.Fatalf("expired lease must be reclaimed and fired: %+v", expiredStats)
	}
	var claimedUntil *string
	if err := pool.QueryRow(
		ctx,
		"SELECT claimed_until::text FROM cron_triggers WHERE id = $1",
		profileTriggerUUID,
	).Scan(&claimedUntil); err != nil {
		t.Fatalf("load cron trigger lease: %v", err)
	}
	if claimedUntil != nil {
		t.Fatalf("completed firing must clear the claim lease, got %q", *claimedUntil)
	}

	agentTriggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, agentTriggerID)
	beforeFailure := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+agentTriggerID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET message_template = '{{ .missing }}',"+
			" next_fire_after = transaction_timestamp() - interval '1 minute' WHERE id = $1",
		agentTriggerUUID,
	); err != nil {
		t.Fatalf("corrupt cron trigger template: %v", err)
	}
	renderFailStats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire trigger with broken template: %v", err)
	}
	if renderFailStats.Claimed != 1 || renderFailStats.Failures != 1 || renderFailStats.Inputs != 0 {
		t.Fatalf("broken template must fail without sending a message: %+v", renderFailStats)
	}
	renderFailed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+agentTriggerID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	renderReport, ok := renderFailed["failure_report"].(map[string]any)
	if !ok {
		t.Fatalf("render failure must be recorded in failure_report: %+v", renderFailed)
	}
	if !strings.Contains(renderReport["message"].(string), "invalid message template") {
		t.Fatalf("unexpected failure report message: %+v", renderReport)
	}
	if renderReport["will_retry"] != false || renderReport["failed_at"] == nil {
		t.Fatalf("render failure must be permanent with a timestamp: %+v", renderReport)
	}
	if renderFailed["next_fire_at"] == nil {
		t.Fatalf("permanent failure must still reschedule the trigger: %+v", renderFailed)
	}
	if renderFailed["last_fired_at"] != beforeFailure["last_fired_at"] {
		t.Fatalf(
			"failed firing must not update last_fired_at: %v -> %v",
			beforeFailure["last_fired_at"],
			renderFailed["last_fired_at"],
		)
	}
	assertCronBacklogMessage(t, ctx, pool, handler, project, agentID, "Nudge agent-nudge.")

	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET message_template = 'Recovered {{ .trigger.name }}.',"+
			" next_fire_after = transaction_timestamp() - interval '3 minutes' WHERE id = $1",
		agentTriggerUUID,
	); err != nil {
		t.Fatalf("repair cron trigger template: %v", err)
	}
	recoveredStats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire trigger with repaired template: %v", err)
	}
	if recoveredStats.Claimed != 1 || recoveredStats.Inputs != 1 || recoveredStats.Failures != 0 {
		t.Fatalf("repaired template must fire cleanly: %+v", recoveredStats)
	}
	recovered := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+agentTriggerID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if recovered["failure_report"] != nil {
		t.Fatalf("successful firing must clear failure_report: %+v", recovered)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET cron_expression = '61 0 * * *',"+
			" next_fire_after = transaction_timestamp() - interval '1 minute' WHERE id = $1",
		agentTriggerUUID,
	); err != nil {
		t.Fatalf("corrupt cron trigger expression: %v", err)
	}
	disableStats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire trigger with broken schedule: %v", err)
	}
	if disableStats.Claimed != 0 || disableStats.Disabled != 1 {
		t.Fatalf("broken schedule must disable the trigger: %+v", disableStats)
	}
	disabled := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+agentTriggerID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if disabled["enabled"] != false || disabled["next_fire_at"] != nil {
		t.Fatalf("broken schedule must leave the trigger disabled: %+v", disabled)
	}
	disableReport, ok := disabled["failure_report"].(map[string]any)
	if !ok {
		t.Fatalf("disable must be recorded in failure_report: %+v", disabled)
	}
	if !strings.Contains(disableReport["message"].(string), "invalid cron expression") ||
		disableReport["will_retry"] != false {
		t.Fatalf("unexpected disable failure report: %+v", disableReport)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/agent-profiles/"+profileID,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+profileTriggerID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/archive",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+agentTriggerID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"archived-target","target":{"type":"agent","agent_id":"`+agentID+`"},`+
			`"cron":"0 9 * * *","message_template":"Archived target."}`,
		"idem-cron-fire-archived-target",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
}

func TestCronTriggerFiringSteeringDelivery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()))
	service := crontrigger.NewService(store.Execution(), nil, testLogger())

	project := bootstrapPublicHTTPProject(t, handler, "cron-steer")
	launch := launchPublicHTTPAgent(t, handler, project, "cron-steer-agent", project.AdminToken, http.StatusCreated)
	agentID := launch["agent"].(map[string]any)["id"].(string)
	triggersPath := project.ProjectPath + "/cron-triggers"

	trigger := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"steer-nudge","target":{"type":"agent","agent_id":"`+agentID+`","delivery_mode":"steering"},`+
			`"cron":"0 9 * * *","message_template":"Steer {{ .trigger.name }}."}`,
		"idem-cron-steer-trigger",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	triggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, trigger["id"].(string))
	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET next_fire_after = transaction_timestamp() - interval '1 minute' WHERE id = $1",
		triggerUUID,
	); err != nil {
		t.Fatalf("backdate cron trigger: %v", err)
	}

	stats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire due triggers: %v", err)
	}
	if stats.Claimed != 1 || stats.Inputs != 1 || stats.Failures != 0 {
		t.Fatalf("unexpected fire stats: %+v", stats)
	}

	agentUUID := mustPublicHTTPID(t, publicid.KindAgent, agentID)
	var deliveryMode, text string
	if err := pool.QueryRow(
		ctx,
		"SELECT input.delivery_mode, block.text_content FROM agent_inputs input"+
			" JOIN content_blocks block ON block.owner_agent_input_id = input.id AND block.block_kind = 'text'"+
			" WHERE input.agent_id = $1 AND input.input_kind = 'content'",
		agentUUID,
	).Scan(&deliveryMode, &text); err != nil {
		t.Fatalf("load cron input: %v", err)
	}
	if deliveryMode != "steering" || text != "Steer steer-nudge." {
		t.Fatalf("cron input = (%q, %q), want steering delivery of the rendered message", deliveryMode, text)
	}
}

func TestCronTriggerClaimFencing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()))

	project := bootstrapPublicHTTPProject(t, handler, "cron-fence")
	launch := launchPublicHTTPAgent(t, handler, project, "cron-fence-agent", project.AdminToken, http.StatusCreated)
	agentID := launch["agent"].(map[string]any)["id"].(string)

	trigger := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/cron-triggers",
		`{"name":"fence","target":{"type":"agent","agent_id":"`+agentID+`"},`+
			`"cron":"0 9 * * *","message_template":"Fence."}`,
		"idem-cron-fence-trigger",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	triggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, trigger["id"].(string))

	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET next_fire_after = transaction_timestamp() - interval '1 minute' WHERE id = $1",
		triggerUUID,
	); err != nil {
		t.Fatalf("backdate cron trigger: %v", err)
	}
	firstClaim, err := store.Execution().ClaimDueCronTriggers(ctx, 10)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(firstClaim.Claimed) != 1 {
		t.Fatalf("expected one claimed trigger, got %+v", firstClaim)
	}
	stale := firstClaim.Claimed[0]

	if _, err := pool.Exec(
		ctx,
		"UPDATE cron_triggers SET claimed_until = transaction_timestamp() - interval '1 second' WHERE id = $1",
		triggerUUID,
	); err != nil {
		t.Fatalf("expire cron trigger lease: %v", err)
	}
	secondClaim, err := store.Execution().ClaimDueCronTriggers(ctx, 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(secondClaim.Claimed) != 1 {
		t.Fatalf("expected the expired lease to be reclaimed, got %+v", secondClaim)
	}
	current := secondClaim.Claimed[0]
	if current.ClaimToken == stale.ClaimToken {
		t.Fatal("reclaim must mint a fresh claim token")
	}

	staleComplete := store.Execution().CompleteCronTriggerFiring(ctx, executionstore.CompleteCronTriggerFiringInput{
		ProjectID:  stale.ProjectID,
		TriggerID:  stale.TriggerID,
		ClaimToken: stale.ClaimToken,
		Fired:      true,
	})
	if !errors.Is(staleComplete, storeerr.ErrConflict) {
		t.Fatalf("stale completion must conflict, got %v", staleComplete)
	}
	staleFailure := store.Execution().RecordCronTriggerFailure(ctx, executionstore.CronTriggerFailureParams{
		ProjectID:  stale.ProjectID,
		TriggerID:  stale.TriggerID,
		ClaimToken: stale.ClaimToken,
		Message:    "stale worker failure",
		WillRetry:  true,
	})
	if !errors.Is(staleFailure, storeerr.ErrConflict) {
		t.Fatalf("stale failure recording must conflict, got %v", staleFailure)
	}
	var claimHeld, fireRecorded bool
	if err := pool.QueryRow(
		ctx,
		"SELECT claimed_until IS NOT NULL, last_fired_at IS NOT NULL FROM cron_triggers WHERE id = $1",
		triggerUUID,
	).Scan(&claimHeld, &fireRecorded); err != nil {
		t.Fatalf("load cron trigger claim state: %v", err)
	}
	if !claimHeld || fireRecorded {
		t.Fatalf("stale writes must not touch the active claim: held=%v fired=%v", claimHeld, fireRecorded)
	}

	if err := store.Execution().CompleteCronTriggerFiring(ctx, executionstore.CompleteCronTriggerFiringInput{
		ProjectID:  current.ProjectID,
		TriggerID:  current.TriggerID,
		ClaimToken: current.ClaimToken,
		Fired:      true,
	}); err != nil {
		t.Fatalf("current claim completion: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT claimed_until IS NOT NULL, last_fired_at IS NOT NULL FROM cron_triggers WHERE id = $1",
		triggerUUID,
	).Scan(&claimHeld, &fireRecorded); err != nil {
		t.Fatalf("load cron trigger completion state: %v", err)
	}
	if claimHeld || !fireRecorded {
		t.Fatalf("holder completion must clear the claim and record the firing: held=%v fired=%v", claimHeld, fireRecorded)
	}
}

func assertCronBacklogMessage(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	handler http.Handler,
	project publicHTTPProject,
	agentPublicID string,
	wantText string,
) {
	t.Helper()
	backlog := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/inputs/backlog",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data := backlog["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected 1 queued cron input for %s, got %+v", agentPublicID, data)
	}
	inputUUID := mustPublicHTTPID(
		t,
		publicid.KindAgentInput,
		data[0].(map[string]any)["id"].(string),
	)
	var text string
	if err := pool.QueryRow(
		ctx,
		"SELECT text_content FROM content_blocks WHERE owner_agent_input_id = $1 AND block_kind = 'text'",
		inputUUID,
	).Scan(&text); err != nil {
		t.Fatalf("load cron input content block: %v", err)
	}
	if text != wantText {
		t.Fatalf("cron input text = %q, want %q", text, wantText)
	}
}

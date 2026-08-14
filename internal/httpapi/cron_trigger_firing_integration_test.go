//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
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

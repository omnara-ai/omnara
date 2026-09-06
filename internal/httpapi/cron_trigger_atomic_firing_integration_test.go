//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
)

func TestCronTriggerAgentInputCompletionIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()))
	service := crontrigger.NewService(store.Execution(), nil, testLogger())
	project := bootstrapPublicHTTPProject(t, handler, "cron-atomic")
	launch := launchPublicHTTPAgent(t, handler, project, "cron-atomic-agent", project.AdminToken, http.StatusCreated)
	agentID := launch["agent"].(map[string]any)["id"].(string)
	agentUUID := mustPublicHTTPID(t, publicid.KindAgent, agentID)
	triggersPath := project.ProjectPath + "/cron-triggers"
	trigger := requestJSONWithHeaders(t, handler, http.MethodPost, triggersPath,
		`{"name":"atomic-nudge","target":{"type":"agent","agent_id":"`+agentID+`"},`+
			`"cron":"0 9 * * *","message_template":"Original message."}`,
		"idem-cron-atomic", http.StatusCreated, authHeaders(project.AdminToken))
	triggerID := trigger["id"].(string)
	triggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, triggerID)
	if _, err := pool.Exec(ctx,
		"UPDATE cron_triggers SET next_fire_after = transaction_timestamp() - interval '1 minute' WHERE id = $1",
		triggerUUID,
	); err != nil {
		t.Fatalf("backdate cron trigger: %v", err)
	}
	// Fail at commit, after both the input and completion have been written.
	// Separate delivery/completion transactions would leave the input committed.
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION fail_cron_completion_commit() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
    IF OLD.claim_token IS NOT NULL AND NEW.claim_token IS NULL
       AND NEW.last_fired_at IS DISTINCT FROM OLD.last_fired_at THEN
        RAISE EXCEPTION 'injected cron completion commit failure';
    END IF;
    RETURN NEW;
END
$function$;
CREATE CONSTRAINT TRIGGER cron_completion_commit_failure
AFTER UPDATE ON cron_triggers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION fail_cron_completion_commit();
`); err != nil {
		t.Fatalf("install completion commit failure: %v", err)
	}
	stats, err := service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("fire with completion failure: %v", err)
	}
	if stats.Claimed != 1 || stats.Failures != 1 || stats.Inputs != 0 {
		t.Fatalf("failed commit must not count as delivery: %+v", stats)
	}
	var inputCount int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'", agentUUID,
	).Scan(&inputCount); err != nil {
		t.Fatalf("count rolled-back inputs: %v", err)
	}
	if inputCount != 0 {
		t.Fatalf("completion failure must roll back input delivery, got %d inputs", inputCount)
	}
	var claimHeld, fireRecorded bool
	if err := pool.QueryRow(ctx,
		"SELECT claim_token IS NOT NULL, last_fired_at IS NOT NULL FROM cron_triggers WHERE id = $1", triggerUUID,
	).Scan(&claimHeld, &fireRecorded); err != nil {
		t.Fatalf("load failed firing state: %v", err)
	}
	if !claimHeld || fireRecorded {
		t.Fatalf("failed commit must preserve the unfinished occurrence: claimed=%v fired=%v", claimHeld, fireRecorded)
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER cron_completion_commit_failure ON cron_triggers"); err != nil {
		t.Fatalf("remove completion commit failure: %v", err)
	}
	requestJSONWithHeaders(t, handler, http.MethodPatch, triggersPath+"/"+triggerID,
		`{"target":{"type":"agent","agent_id":"`+agentID+`","delivery_mode":"steering"},`+
			`"message_template":"Updated message."}`,
		"", http.StatusOK, authHeaders(project.AdminToken))
	if _, err := pool.Exec(ctx,
		"UPDATE cron_triggers SET claimed_until = transaction_timestamp() - interval '1 second' WHERE id = $1",
		triggerUUID,
	); err != nil {
		t.Fatalf("expire failed claim: %v", err)
	}
	stats, err = service.FireDueTriggers(ctx)
	if err != nil {
		t.Fatalf("retry edited trigger: %v", err)
	}
	if stats.Claimed != 1 || stats.Inputs != 1 || stats.Failures != 0 {
		t.Fatalf("edited trigger must retry without an idempotency conflict: %+v", stats)
	}
	var mode, message string
	if err := pool.QueryRow(ctx,
		"SELECT input.delivery_mode, block.text_content FROM agent_inputs input"+
			" JOIN content_blocks block ON block.owner_agent_input_id = input.id AND block.block_kind = 'text'"+
			" WHERE input.agent_id = $1 AND input.input_kind = 'content'", agentUUID,
	).Scan(&mode, &message); err != nil {
		t.Fatalf("load retried input: %v", err)
	}
	if mode != "steering" || message != "Updated message." {
		t.Fatalf("retried input = (%q, %q), want updated steering message", mode, message)
	}
	completed := requestJSONWithHeaders(t, handler, http.MethodGet, triggersPath+"/"+triggerID,
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	if completed["last_fired_at"] == nil || completed["failure_report"] != nil {
		t.Fatalf("successful delivery must complete the occurrence and clear its failure: %+v", completed)
	}
	stats, err = service.FireDueTriggers(ctx)
	if err != nil || stats.Claimed != 0 {
		t.Fatalf("delivered occurrence must not be retried: stats=%+v error=%v", stats, err)
	}
}

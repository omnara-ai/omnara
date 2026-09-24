//go:build integration

package executionstore_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestFireDueTriggersProfileUsesOccurrenceTimezone(t *testing.T) {
	t.Parallel()
	f, _ := cronIntegrationFixture(t)
	trigger, err := f.store.Execution().CreateCronTrigger(f.ctx, executionstore.CreateCronTriggerInput{
		ProjectID:      testProjectID,
		Name:           "ordinary-profile",
		Enabled:        true,
		CronExpression: "0 9 * * *",
		Timezone:       "America/Los_Angeles",
		IdempotencyKey: "ordinary-profile",
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetAgentProfile,
			ID:   f.profile.ID,
		},
		MessageTemplate: `{{.trigger.local_date}}|{{.trigger.fired_at}}|{{.trigger.last_fired_at}}`,
	})
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `UPDATE cron_triggers
 SET next_fire_after='2026-03-09T01:30:00Z', last_fired_at='2026-03-07T17:00:00Z' WHERE id=$1`, trigger.ID)
	require.NoError(t, err)
	// PostgreSQL in Docker may have a different clock from the test host.
	before, err := f.store.q.DBNow(f.ctx)
	require.NoError(t, err)
	service := crontrigger.NewService(f.store.Execution(), nil, slog.Default())
	stats, err := service.FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{Claimed: 1, Launched: 1}, stats)
	after, err := f.store.q.DBNow(f.ctx)
	require.NoError(t, err)

	var message, provider, tenant, actorID, name string
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT block.text_content,
 actor.provider,actor.provider_tenant_id,actor.provider_user_id,actor.display_name
 FROM agent_inputs input JOIN content_blocks block ON block.owner_agent_input_id=input.id
 JOIN actors actor ON actor.id=input.actor_id WHERE input.project_id=$1 AND block.block_kind='text'`,
		testProjectID).Scan(&message, &provider, &tenant, &actorID, &name))
	parts := strings.Split(message, "|")
	require.Len(t, parts, 3)
	require.Equal(t, "2026-03-08", parts[0])
	firedAt, err := time.Parse(time.RFC3339, parts[1])
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(parts[1], "Z"))
	require.False(t, firedAt.Before(before.Truncate(time.Second)))
	require.False(t, firedAt.After(after))
	require.Equal(t, "2026-03-07T17:00:00Z", parts[2])
	wantTenant, err := publicid.Encode(publicid.KindOrganization, testOrgID)
	require.NoError(t, err)
	wantActor, err := publicid.Encode(publicid.KindCronTrigger, trigger.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ActorProviderOmnara, provider)
	require.Equal(t, wantTenant, tenant)
	require.Equal(t, wantActor, actorID)
	require.Equal(t, trigger.Name, name)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, trigger.ID)
	require.NoError(t, err)
	require.NotNil(t, current.LastFiredAt)
	require.False(t, current.LastFiredAt.Before(firedAt))
	require.False(t, current.LastFiredAt.After(after))
	require.Nil(t, current.FailureReport)
	require.Nil(t, current.LastRun)
	var count int
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox`).Scan(&count))
	require.Zero(t, count)
	stats, err = service.FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{}, stats)
}

func TestFireDueTriggersIntegrationHandoffRollbackAndRecovery(t *testing.T) {
	t.Parallel()
	f, trigger := cronIntegrationFixture(t)
	due := time.Date(2026, 3, 9, 1, 30, 0, 0, time.UTC)
	_, err := f.store.pool.Exec(f.ctx, `UPDATE cron_triggers SET next_fire_after=$2 WHERE id=$1`, trigger.ID, due)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `CREATE FUNCTION fail_integration_handoff() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.last_integration_receipt_id IS NOT NULL THEN RAISE EXCEPTION 'injected handoff failure'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER fail_integration_handoff BEFORE UPDATE ON cron_triggers
 FOR EACH ROW EXECUTE FUNCTION fail_integration_handoff()`)
	require.NoError(t, err)
	service := crontrigger.NewService(f.store.Execution(), nil, slog.Default())
	stats, err := service.FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{Claimed: 1, Failures: 1}, stats)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, trigger.ID)
	require.NoError(t, err)
	require.NotNil(t, current.FailureReport)
	require.Equal(t, "Scheduled integration action could not be queued.", current.FailureReport.Message)
	require.True(t, current.FailureReport.WillRetry)
	require.Nil(t, current.LastFiredAt)
	require.Nil(t, current.LastRun)
	require.NotNil(t, current.NextFireAfter)
	require.True(t, current.NextFireAfter.Equal(due))
	var count int
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox`).Scan(&count))
	require.Zero(t, count, "receipt insertion must roll back with the failed pointer update")
	var held, pointed bool
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT claim_token IS NOT NULL AND claimed_until > now(),
 last_integration_receipt_id IS NOT NULL FROM cron_triggers WHERE id=$1`, trigger.ID).Scan(&held, &pointed))
	require.True(t, held)
	require.False(t, pointed)
	stats, err = service.FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{}, stats, "the failed claim remains leased until recovery")

	_, err = f.store.pool.Exec(f.ctx, `DROP TRIGGER fail_integration_handoff ON cron_triggers`)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx,
		`UPDATE cron_triggers SET claimed_until=now()-interval '1 second' WHERE id=$1`, trigger.ID)
	require.NoError(t, err)
	stats, err = service.FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{Claimed: 1, Queued: 1}, stats)
	receipt, event := cronIntegrationReceipt(t, f, trigger.ID)
	require.True(t, event.Occurrence.DueAt.Equal(due), "recovery must queue the same occurrence")
	var key string
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT receipt_key FROM integration_inbox WHERE id=$1`, receipt).
		Scan(&key))
	require.Equal(t, "cron_trigger:"+trigger.ID.String()+":"+due.Format(time.RFC3339), key)
	current, err = f.store.Execution().GetCronTrigger(f.ctx, testProjectID, trigger.ID)
	require.NoError(t, err)
	require.Nil(t, current.FailureReport)
	require.NotNil(t, current.LastFiredAt)
	require.NotNil(t, current.LastRun)
	require.Equal(t, executionstore.CronTriggerLastRunQueued, current.LastRun.State)
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox`).Scan(&count))
	require.Equal(t, 1, count)
	stats, err = service.FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{}, stats)
}

//go:build integration

package executionstore_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookBacklogMatchesDeliveryLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "webhook_backlog")
	webhook := createAgentEventWebhook(t, f, nil)
	execution := f.Store.Execution()
	sample, err := execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.Equal(t, executionstore.EventWebhookBacklog{}, sample)
	// A due retry, a future retry, a live claim, an expired claim that is due
	// again, an expired row, and an expired row protected by a live claim.
	for _, state := range []struct{ age, delay, claim string }{
		{"2 minutes", "-1 minute", "NULL"},
		{"9 minutes", "1 minute", "NULL"},
		{"3 minutes", "-1 minute", "statement_timestamp() + interval '30 seconds'"},
		{"4 minutes", "-1 minute", "statement_timestamp() - interval '1 second'"},
		{"11 minutes", "-1 minute", "NULL"},
		{"12 minutes", "-1 minute", "statement_timestamp() + interval '30 seconds'"},
	} {
		token := "NULL"
		if state.claim != "NULL" {
			token = "uuidv7()"
		}
		_, err = f.Store.pool.Exec(ctx, `INSERT INTO event_webhook_deliveries
            (org_id, webhook_id, agent_id, tool_call_id, tool_state, created_at, next_attempt_at,
                claim_token, claim_expires_at)
            SELECT org_id, $1, id, $3, 'ready', statement_timestamp() - $4::interval,
                statement_timestamp() + $5::interval, `+token+`, `+state.claim+` FROM agents WHERE id = $2`,
			webhook.ID, f.AgentID, uuid.New(), state.age, state.delay)
		require.NoError(t, err)
	}
	sample, err = execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, sample.Due)
	require.EqualValues(t, 1, sample.Delayed)
	require.EqualValues(t, 2, sample.InFlight)
	require.EqualValues(t, 1, sample.Expired)
	require.GreaterOrEqual(t, sample.OldestDueAge, 4*time.Minute)
	require.Less(t, sample.OldestDueAge, 5*time.Minute)
	require.GreaterOrEqual(t, sample.OldestDueLag, time.Minute)
	require.Less(t, sample.OldestDueLag, 2*time.Minute)
	// Due includes organization-throttled work: a high age is useful even
	// when another delivery currently consumes the organization's last slot.
	_, err = execution.ClaimEventWebhookDelivery(ctx, 1)
	require.Error(t, err)
	claimed, err := execution.ClaimEventWebhookDelivery(ctx, 100)
	require.NoError(t, err)
	require.False(t, claimed.CreatedAt.IsZero())
	require.WithinDuration(t, time.Now().Add(-time.Minute), claimed.NextAttemptAt, 10*time.Second)
	sample, err = execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, sample.Due)
	require.EqualValues(t, 3, sample.InFlight)
	retry, err := execution.RetryEventWebhookDelivery(ctx, claimed.ID, claimed.ClaimToken, time.Minute)
	require.NoError(t, err)
	require.False(t, retry.GaveUp)
	sample, err = execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, sample.Due)
	require.EqualValues(t, 2, sample.Delayed)
	require.EqualValues(t, 2, sample.InFlight)
	claimed, err = execution.ClaimEventWebhookDelivery(ctx, 100)
	require.NoError(t, err)
	deleted, err := execution.DeleteExpiredEventWebhookDeliveries(ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted, "expired live claims must survive cleanup")
	sample, err = execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.Zero(t, sample.Expired)
	require.EqualValues(t, 3, sample.InFlight)
	require.NoError(t, execution.CompleteEventWebhookDelivery(ctx, claimed.ID, claimed.ClaimToken))
	sample, err = execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, sample.InFlight)
}

func TestEventWebhookGivenUpDeliveryRemainsUntilExpiryCleanup(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "webhook_give_up_cleanup")
	webhook := createAgentEventWebhook(t, f, nil)
	_, err := f.Store.pool.Exec(ctx, `INSERT INTO event_webhook_deliveries
		(org_id, webhook_id, agent_id, tool_call_id, tool_state, created_at)
		VALUES ($1, $2, $3, $4, 'ready', statement_timestamp() - interval '9 minutes')`,
		testOrgID, webhook.ID, f.AgentID, uuid.New())
	require.NoError(t, err)
	execution := f.Store.Execution()
	delivery, err := execution.ClaimEventWebhookDelivery(ctx, 1)
	require.NoError(t, err)
	retry, err := execution.RetryEventWebhookDelivery(ctx, delivery.ID, delivery.ClaimToken, 2*time.Minute)
	require.NoError(t, err)
	require.True(t, retry.GaveUp)
	sample, err := execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, sample.Delayed, "given-up rows are retained until their window expires")
	deleted, err := execution.DeleteExpiredEventWebhookDeliveries(ctx, 100)
	require.NoError(t, err)
	require.Zero(t, deleted)
	_, err = f.Store.pool.Exec(ctx, `UPDATE event_webhook_deliveries
		SET created_at = statement_timestamp() - interval '11 minutes' WHERE id = $1`, delivery.ID)
	require.NoError(t, err)
	sample, err = execution.SampleEventWebhookBacklog(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, sample.Expired)
	deleted, err = execution.DeleteExpiredEventWebhookDeliveries(ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted, "cleanup counts previously given-up rows")
	deleted, err = execution.DeleteExpiredEventWebhookDeliveries(ctx, 100)
	require.NoError(t, err)
	require.Zero(t, deleted, "cleanup counts each retained row once")
}

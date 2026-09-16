//go:build integration

package executionstore_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestCronAndOrdinaryLaunchLockTargetsInSameOrder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newPublicChannelFixture(t, ctx, "cron-launch-target-order")
	second := secondCronChannel(t, ctx, f)
	ids := []uuid.UUID{f.target.ID, second.ID}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	cronInput := cronChannelInput(f)
	cronInput.ChannelBindings = nil
	trigger, err := f.store.Execution().CreateCronTrigger(ctx, cronInput)
	require.NoError(t, err)
	// Hold the first target. Neither path may acquire the second target before
	// waiting here, even when the ordinary caller submits the opposite order.
	control, err := f.store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = control.Rollback(ctx) }()
	var controlPID int32
	require.NoError(t, control.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&controlPID))
	_, err = control.Exec(ctx, `SELECT id FROM integration_targets WHERE id=$1 FOR NO KEY UPDATE`, ids[0])
	require.NoError(t, err)
	bindings := []executionstore.LaunchChannelBinding{
		{ChannelID: ids[1], Grants: integrationstore.ChannelGrants{SendAllowed: true}},
		{ChannelID: ids[0], Grants: integrationstore.ChannelGrants{ReadAllowed: true}},
	}
	launchDone := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		// No profile lock shared with cron: target order must itself be safe.
		return f.store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID: testProjectID, AgentConfigID: f.agent.CurrentConfigID,
			LaunchedBy: userPrincipal(f.user.ID), IdempotencyKey: "reverse-order-launch", ChannelBindings: bindings,
		})
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.store.pool, "-- name: LockIntegrationTargetForBinding ", controlPID)
	cronDone := integrationdb.RunAsync(func() (executionstore.CronTriggerRecord, error) {
		return f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
			ProjectID: testProjectID, TriggerID: trigger.ID, ChannelBindings: &bindings,
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockIntegrationTargetForBinding", 2)
	_, err = control.Exec(ctx,
		`SELECT id FROM integration_targets WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, ids[1])
	require.NoError(t, err, "reverse-order launch must not hold the later target while waiting for the first")
	require.NoError(t, control.Commit(ctx))
	launched := integrationdb.AwaitSuccess(t, launchDone, "ordinary launch after target release")
	configured := integrationdb.AwaitSuccess(t, cronDone, "cron update after target release")
	require.True(t, launched.Created)
	require.ElementsMatch(t, bindings, configured.ChannelBindings)
	var granted int
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings
WHERE agent_id=$1 AND revoked_at IS NULL`, launched.Agent.ID).Scan(&granted))
	require.Equal(t, 2, granted)
	require.Equal(t, ids[1], bindings[0].ChannelID, "storage lock ordering must not mutate caller input")
}

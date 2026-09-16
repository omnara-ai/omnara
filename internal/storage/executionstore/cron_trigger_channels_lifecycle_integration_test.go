//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestCronChannelUpdateLocksInstallationBeforeProfileAndTrigger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-channel-lock-order")
	created, err := f.store.Execution().CreateCronTrigger(ctx, cronChannelInput(f))
	require.NoError(t, err)
	setup, err := f.store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = setup.Rollback(ctx) }()
	var setupPID int32
	require.NoError(t, setup.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&setupPID))
	_, err = setup.Exec(ctx,
		`SELECT id FROM integration_installs WHERE project_id=$1 AND id=$2 FOR UPDATE`, testProjectID, f.install.ID)
	require.NoError(t, err)
	bindings := created.ChannelBindings
	bindings[0].Grants.ReceiveAllowed = true
	finished := integrationdb.RunAsyncError(func() error {
		_, updateErr := f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
			ProjectID: testProjectID, TriggerID: created.ID, ChannelBindings: &bindings,
		})
		return updateErr
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx,
		f.store.pool, "-- name: LockIntegrationTargetCreateAuthority ", setupPID)
	// Installation setup may next lock the profile; profile launch completes by
	// locking the cron row. The waiting update must hold neither of those rows.
	_, err = setup.Exec(ctx,
		`SELECT id FROM agent_profiles WHERE project_id=$1 AND id=$2 FOR UPDATE NOWAIT`, testProjectID, f.agent.AgentProfileID)
	require.NoError(t, err)
	_, err = setup.Exec(ctx,
		`SELECT id FROM cron_triggers WHERE project_id=$1 AND id=$2 FOR UPDATE NOWAIT`, testProjectID, created.ID)
	require.NoError(t, err)
	require.NoError(t, setup.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, finished, "cron channel update after installation setup"))
	updated, err := f.store.Execution().GetCronTrigger(ctx, testProjectID, created.ID)
	require.NoError(t, err)
	require.True(t, updated.ChannelBindings[0].Grants.ReceiveAllowed)
}

func TestCronProfileLaunchConcurrentScheduleDeletionRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-delete-during-launch")
	created, err := f.store.Execution().CreateCronTrigger(ctx, cronChannelInput(f))
	require.NoError(t, err)
	makeCronDue(t, ctx, f, created.ID)
	claim := claimCron(t, ctx, f)
	control, err := f.store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = control.Rollback(ctx) }()
	var controlPID int32
	require.NoError(t, control.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&controlPID))
	_, err = control.Exec(ctx, `SELECT id FROM agent_profiles WHERE id=$1 FOR UPDATE`, f.agent.AgentProfileID)
	require.NoError(t, err)
	input := cronLaunchInput(t, f, claim)
	finished := integrationdb.RunAsyncError(func() error {
		_, launchErr := f.store.Execution().LaunchCronTriggerAgent(ctx, input)
		return launchErr
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.store.pool, "-- name: LockAgentProfile ", controlPID)
	require.NoError(t, f.store.Execution().DeleteCronTrigger(ctx, testProjectID, created.ID))
	require.NoError(t, control.Commit(ctx))
	require.ErrorIs(t, integrationdb.Await(t, finished, "launch after schedule deletion"), storeerr.ErrNotFound)
	var launches, inputs, bindings int
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE id<>$1`, f.agent.ID).Scan(&launches))
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id<>$1`, f.agent.ID).Scan(&inputs))
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings`).Scan(&bindings))
	require.Zero(t, launches)
	require.Zero(t, inputs)
	require.Zero(t, bindings, "losing schedule deletion cannot leave an orphan grant")
}

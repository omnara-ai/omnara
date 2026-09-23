//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func (f appActivationFixture) launchWithSelectedApp(t *testing.T) executionstore.LaunchAgentResult {
	t.Helper()
	definition := f.withSendingTools(t, f.definition(t, "Selected app"))
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(definition.CompiledDefinition, &compiled))
	compiled.InteractionHandlers = map[string]agentconfig.AppCapabilityCompiled{
		"chat": {AppID: f.app.ID},
	}
	definition = f.encodedDefinition(t, compiled)
	input := f.launchInput(uuid.Nil, "selected-app")
	input.DerivedConfig = &definition
	input.Subscriptions = []appstore.AppSubscriptionAttachment{f.attachment()}
	launch, err := f.store.Execution().LaunchAgent(f.ctx, input)
	require.NoError(t, err)
	target := (appInteractionFixture{ctx: f.ctx, store: f.store, app: f.app}).target(t, launch.Agent.ID, "C123:111.222")
	tx := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err = dbsqlc.New(tx).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: launch.Agent.ID,
	})
	require.NoError(t, err)
	selection, err := f.store.Execution().SelectInteractionDestinationForOriginTx(
		f.ctx, tx, testProjectID, launch.Agent.ID, target.ID,
	)
	require.NoError(t, err)
	require.Equal(t, "chat", selection.HandlerKey)
	require.Equal(t, target.ID, selection.AppTargetID)
	require.NoError(t, tx.Commit(f.ctx))
	return launch
}

func TestConfigChangeDropsPreviousAppWithoutItsGate(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	launch := f.launchWithSelectedApp(t)
	before := f.subscriptions(t, launch.Agent.ID)
	control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(t, dbsqlc.New(control).LockProjectAppLifecycleExclusive(
		f.ctx, dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
	))
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	changed, err := f.store.Execution().IntegrationChangeAgentConfigOnce(
		ctx, f.changeInput(t, launch.Agent.ID, "Remove all app references", "remove-app"),
	)
	require.NoError(t, err, "removing old tools and handlers must not wait for their app gate")
	current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, changed.AgentConfig.ID, current.CurrentConfigID)
	selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection, "removed handler must still be reconciled")
	require.Equal(t, before, f.subscriptions(t, launch.Agent.ID), "receiving remains independent of config")
}

func (f appActivationFixture) revokeAppForConfigGateTest(state string) error {
	if state == "deleted" {
		return f.store.Apps().DeleteProjectApp(f.ctx, testOrgID, testProjectID, f.app.ID)
	}
	_, err := f.store.Apps().DisconnectProjectApp(f.ctx, appstore.DisconnectProjectAppInput{
		ProjectID: testProjectID, AppID: f.app.ID,
	})
	return err
}

func TestConfigChangeSerializesNextAppWithRevocation(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"disconnected", "deleted"} {
		for _, order := range []string{"config-first", "revocation-first"} {
			t.Run(state+"/"+order, func(t *testing.T) {
				t.Parallel()
				f := newAppActivationFixture(t)
				launch := f.launchWithSelectedApp(t)
				before := f.subscriptions(t, launch.Agent.ID)
				var compiled agentconfig.Compiled
				require.NoError(t, json.Unmarshal(launch.AgentConfig.CompiledDefinition, &compiled))
				compiled.Instruction = "Edit while the referenced app is revoked"
				input := f.changeInput(t, launch.Agent.ID, compiled.Instruction, "concurrent-revocation")
				input.CreateAgentConfigInput = f.encodedDefinition(t, compiled)
				input.ExpectedCurrentConfigID = launch.Agent.CurrentConfigID
				change := func() (executionstore.ChangeAgentConfigResult, error) {
					return f.store.Execution().IntegrationChangeAgentConfigOnce(f.ctx, input)
				}
				revoke := func() error { return f.revokeAppForConfigGateTest(state) }
				control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
				var changed <-chan integrationdb.AsyncResult[executionstore.ChangeAgentConfigResult]
				var revoked <-chan error
				if order == "config-first" {
					require.NoError(t, dbsqlc.New(control).LockAgentMachineSources(
						f.ctx, dbsqlc.LockAgentMachineSourcesParams{AgentID: launch.Agent.ID},
					))
					changed = integrationdb.RunAsync(change)
					integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentMachineSources", 1)
					revoked = integrationdb.RunAsyncError(revoke)
					integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleExclusive", 1)
				} else {
					require.NoError(t, dbsqlc.New(control).LockProjectAppLifecycleShared(
						f.ctx, dbsqlc.LockProjectAppLifecycleSharedParams{AppID: f.app.ID},
					))
					revoked = integrationdb.RunAsyncError(revoke)
					integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleExclusive", 1)
					changed = integrationdb.RunAsync(change)
					integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockProjectAppLifecycleShared", 1)
				}
				require.NoError(t, control.Commit(f.ctx))
				result := integrationdb.AwaitSuccess(t, changed, "config change racing app revocation")
				require.NoError(t, integrationdb.Await(t, revoked, "app revocation racing config change"))
				current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, launch.Agent.ID)
				require.NoError(t, err)
				require.Equal(t, result.AgentConfig.ID, current.CurrentConfigID, "revoked references remain valid metadata")
				destination, err := f.store.Execution().GetSelectedInteractionDestination(f.ctx, testProjectID, launch.Agent.ID)
				require.NoError(t, err)
				require.Nil(t, destination, "config activation cannot grant live authority to a revoked app")
				if state == "deleted" || order == "revocation-first" {
					selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, launch.Agent.ID)
					require.NoError(t, err)
					require.Equal(t, executionstore.InteractionSelection{}, selection)
				}
				if state == "deleted" {
					require.Empty(t, f.subscriptions(t, launch.Agent.ID), "deletion removes subscriptions")
				} else {
					require.Equal(t, before, f.subscriptions(t, launch.Agent.ID), "disconnection preserves subscriptions")
				}
			})
		}
	}
}

func TestConfigChangeReplaySkipsRevokedAppGate(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"disconnected", "deleted"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			launch := f.launchWithSelectedApp(t)
			input := f.changeInput(t, launch.Agent.ID, "Accepted app config", "accepted-app-config")
			input.CreateAgentConfigInput = f.withSendingTools(t, input.CreateAgentConfigInput)
			input.ExpectedCurrentConfigID = launch.Agent.CurrentConfigID
			accepted, err := f.store.Execution().ChangeAgentConfig(f.ctx, input)
			require.NoError(t, err)
			latest, err := f.store.Execution().ChangeAgentConfig(
				f.ctx, f.changeInput(t, launch.Agent.ID, "Latest without apps", "latest-config"),
			)
			require.NoError(t, err)
			require.NoError(t, f.revokeAppForConfigGateTest(state))
			before := f.subscriptions(t, launch.Agent.ID)
			control := integrationdb.BeginTx(t, f.ctx, f.store.pool)
			require.NoError(t, dbsqlc.New(control).LockProjectAppLifecycleExclusive(
				f.ctx, dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
			))
			ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
			defer cancel()
			replayed, err := f.store.Execution().IntegrationChangeAgentConfigOnce(ctx, input)
			require.NoError(t, err, "committed replay skips app gates and stale expected-current validation")
			require.Equal(t, accepted.AgentConfig.ID, replayed.AgentConfig.ID)
			require.Equal(t, accepted.ConfigChange.AgentInput.ID, replayed.ConfigChange.AgentInput.ID)
			require.Equal(t, accepted.ConfigChange.Event.ID, replayed.ConfigChange.Event.ID)
			input.Reason = "conflicting replay"
			_, err = f.store.Execution().IntegrationChangeAgentConfigOnce(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			current, err := f.store.Execution().GetAgentInProject(f.ctx, testProjectID, launch.Agent.ID)
			require.NoError(t, err)
			require.Equal(t, latest.AgentConfig.ID, current.CurrentConfigID, "replay cannot reactivate the older config")
			require.Equal(t, before, f.subscriptions(t, launch.Agent.ID), "replay cannot restore subscriptions")
			selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, launch.Agent.ID)
			require.NoError(t, err)
			require.Equal(t, executionstore.InteractionSelection{}, selection)
			var events int
			require.NoError(t, f.store.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM agent_events WHERE agent_id=$1 AND agent_input_id=$2`,
				launch.Agent.ID, accepted.ConfigChange.AgentInput.ID).Scan(&events))
			require.Equal(t, 1, events, "replay must retain exactly one causal event")
		})
	}
}

//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestCurrentChannelFollowsAdmissionAndExplicitSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "current-channel")
	store, pool := f.store, f.store.pool
	agent, install := f.agent, f.install
	definitionID := f.target.ChannelDefinitionID
	type origin struct {
		target  integrationstore.IntegrationTargetRecord
		binding integrationstore.IntegrationTargetBindingRecord
	}
	origins := make([]origin, 0, 3)
	for _, name := range []string{"a", "b", "c"} {
		target, err := store.Integrations().CreateIntegrationTarget(ctx,
			integrationstore.CreateIntegrationTargetInput{
				ProjectID: testProjectID, ChannelDefinitionID: definitionID, IntegrationInstallID: install.ID,
				ProviderRef: name, ProviderRefKind: "thread",
			})
		require.NoError(t, err)
		binding, err := store.Integrations().CreateIntegrationTargetBinding(ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
				IntegrationTargetID: target.ID, ReceiveAllowed: true, SendAllowed: true, Source: "current-channel",
			})
		require.NoError(t, err)
		origins = append(origins, origin{target: target, binding: binding})
	}
	current := func(want ID) {
		t.Helper()
		// Recreate the composition root to prove this is durable state.
		id, err := newSecretIntegrationStore(pool).Execution().GetAgentCurrentChannelID(ctx, testProjectID, agent.ID)
		require.NoError(t, err)
		require.Equal(t, want, id)
	}
	enqueue := func(index int) executionstore.AgentInputRecord {
		t.Helper()
		source := origins[index]
		input, _, _, err := store.Execution().CreateAgentContentInput(ctx,
			executionstore.CreateAgentContentInputInput{
				ProjectID: testProjectID, AgentID: agent.ID, ChannelID: source.target.ID,
				Actor:          mustOmnaraActorParams(t, f.user.ID),
				ContentBlocks:  json.RawMessage(`[{"type":"text","text":"incoming"}]`),
				IdempotencyKey: source.target.ProviderRef, DeliveryMode: executionstore.DeliveryModeSteering,
			})
		require.NoError(t, err)
		require.Equal(t, source.binding.ID, input.IntegrationTargetBindingID)
		return input
	}
	current(NilID)
	first, second := enqueue(0), enqueue(1)
	current(NilID)
	runtime, err := store.Execution().AcquireAgentRuntimeLock(ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration)
	require.NoError(t, err)
	admitted, found := admitNextAgentInputAndOpenTurnForTest(t, ctx, store, testProjectID, agent.ID, runtime.ID)
	require.True(t, found)
	require.Equal(t, []ID{first.ID, second.ID}, []ID{admitted.Inputs[0].ID, admitted.Inputs[1].ID})
	current(origins[1].target.ID)

	_, err = executionstore.IntegrationSetAgentIntegrationTarget(ctx,
		store.q,
		testProjectID,
		agent.ID,
		origins[0].target.ID)
	require.NoError(t, err)
	current(origins[0].target.ID)
	require.Equal(t, second.ID, enqueue(1).ID, "event replay reuses the original input")
	_, found = admitNextAgentInputAndOpenTurnForTest(t, ctx, store, testProjectID, agent.ID, runtime.ID)
	require.False(t, found)
	current(origins[0].target.ID)

	_, _, _, err = store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: agent.ID, IdempotencyKey: "dashboard",
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"continue"}]`),
		DeliveryMode:  executionstore.DeliveryModeSteering,
	})
	require.NoError(t, err)
	_, found = admitNextAgentInputAndOpenTurnForTest(t, ctx, store, testProjectID, agent.ID, runtime.ID)
	require.True(t, found)
	current(origins[0].target.ID)

	enqueue(2)
	current(origins[0].target.ID)
	_, err = store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
		ProjectID: testProjectID, ID: install.ID, ExpectedOAuthFlowID: &install.LastOAuthFlowID,
	})
	require.NoError(t, err)
	_, found = admitNextAgentInputAndOpenTurnForTest(t, ctx, store, testProjectID, agent.ID, runtime.ID)
	require.True(t, found, "an unavailable origin must not block input admission")
	current(origins[2].target.ID)
	_, err = executionstore.IntegrationSetAgentIntegrationTarget(ctx,
		store.q,
		testProjectID,
		agent.ID,
		origins[0].target.ID)
	require.ErrorIs(t, err, storeerr.ErrConflict, "explicit selection checks current authority")
	current(origins[2].target.ID)
	_, err = executionstore.IntegrationSetAgentIntegrationTarget(ctx, store.q, testProjectID, agent.ID, NilID)
	require.NoError(t, err)
	current(NilID)
}

func TestDeleteInstallLocksRevokedCurrentChannelAgentBeforeInstall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "revoked-current-channel")
	definitionID := createChannelTestDefinition(t, ctx, store, install)
	target, err := store.Integrations().CreateIntegrationTarget(ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID, IntegrationInstallID: install.ID,
			ProviderRef: "revoked-current", ProviderRefKind: "thread",
		})
	require.NoError(t, err)
	binding, err := store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
			IntegrationTargetID: target.ID, SendAllowed: true, Source: "revoked-current",
		})
	require.NoError(t, err)
	_, err = executionstore.IntegrationSetAgentIntegrationTarget(ctx, store.q, testProjectID, agent.ID, target.ID)
	require.NoError(t, err)
	require.NoError(t, store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))

	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(context.Background()) }()
	var holderPID int32
	require.NoError(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID))
	_, err = holder.Exec(ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		agent.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID) }()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "-- name: LockAgentInProject ", holderPID)
	// Deletion must not hold the install row while waiting on a current agent,
	// even when that agent has no remaining active binding to enumerate.
	probe, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = probe.Exec(ctx, `SELECT id FROM integration_installs WHERE id = $1 FOR UPDATE NOWAIT`, install.ID)
	_ = probe.Rollback(context.Background())
	require.NoError(t, err)
	require.NoError(t, holder.Rollback(ctx))
	require.NoError(t, <-done)
	current, err := store.Execution().GetAgentCurrentChannelID(ctx, testProjectID, agent.ID)
	require.NoError(t, err)
	require.Equal(t, NilID, current)
}

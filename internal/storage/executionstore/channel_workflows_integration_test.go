//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type channelWorkflowFixture struct {
	Store      *Store
	Identity   executionstore.ChannelWorkflowIdentity
	Definition integrationstore.ChannelDefinition
}

func newChannelWorkflowFixture(t *testing.T, ctx context.Context, name string) channelWorkflowFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, name)
	route, err := store.Integrations().CreateIntegrationRoute(ctx, integrationstore.CreateIntegrationRouteInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, AgentProfileID: agent.AgentProfileID,
		DeploymentKey: "conversation", BehaviorKey: "conversation",
	})
	require.NoError(t, err)
	definition, err := store.Integrations().PublishConnectorChannelDefinition(
		ctx, integrationstore.PublishChannelDefinitionInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindDiscordThread,
			SendParamsSchema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities:          integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: testChannelCapabilities(testChannelProvider),
		})
	require.NoError(t, err)
	return channelWorkflowFixture{
		Store: store, Definition: definition,
		Identity: executionstore.ChannelWorkflowIdentity{
			ProjectID: testProjectID, IntegrationInstallID: install.ID, IntegrationRouteID: route.ID,
			InstanceKey: "thread-1", Capabilities: testChannelCapabilities(testChannelProvider),
		},
	}
}

func (f channelWorkflowFixture) event(
	t *testing.T,
	ctx context.Context,
	eventID string,
) executionstore.DeliverChannelWorkflowInput {
	t.Helper()
	_, err := f.Store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		EventID: eventID, Payload: json.RawMessage(`{"text":"hello"}`), Capabilities: f.Identity.Capabilities,
	})
	require.NoError(t, err)
	lease, found, err := f.Store.Integrations().ClaimNextIntegrationEvent(
		ctx, integrationstore.ClaimNextIntegrationEventInput{
			Capability: f.Identity.Capabilities[0], LeaseDuration: time.Minute,
		})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, eventID, lease.EventID)
	prepared, err := f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
	require.NoError(t, err)
	return executionstore.DeliverChannelWorkflowInput{
		Prepared: prepared,
		InputKey: eventID,
		Receipt: executionstore.ChannelEventLease{
			ReceiptID: lease.ID, LeaseToken: lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration,
		},
		Target: integrationstore.CreateIntegrationTargetInput{
			ProviderRef: "thread-1", ProviderRefKind: "thread", ChannelDefinitionID: f.Definition.ID,
		},
		ReadAllowed: true, SendAllowed: true, ProviderUserID: "author",
		Content: executionstore.PreparedInputContent{Blocks: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
	}
}

func TestChannelWorkflowConcurrentFirstEventsAndReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "workflow-concurrent")
	inputs := []executionstore.DeliverChannelWorkflowInput{f.event(t, ctx, "first"), f.event(t, ctx, "second")}
	require.NotEqual(t, inputs[0].Prepared.AgentID(), inputs[1].Prepared.AgentID())
	results := make([]executionstore.ChannelInputResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range inputs {
		workers.Go(func() {
			<-start
			results[i], errs[i] = f.Store.Execution().DeliverChannelWorkflow(ctx, inputs[i])
		})
	}
	close(start)
	workers.Wait()
	winner := -1
	for i := range inputs {
		if errs[i] == nil {
			require.Equal(t, -1, winner, "only one provisional identity can win")
			winner = i
		} else {
			require.ErrorIs(t, errs[i], executionstore.ErrChannelWorkflowAgentChanged)
		}
	}
	require.NotEqual(t, -1, winner)
	loser := 1 - winner
	prepared, err := f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
	require.NoError(t, err)
	require.Equal(t, results[winner].AgentInput.AgentID, prepared.AgentID())
	inputs[loser].Prepared = prepared
	results[loser], err = f.Store.Execution().DeliverChannelWorkflow(ctx, inputs[loser])
	require.NoError(t, err)
	require.Equal(t, results[winner].AgentInput.AgentID, results[loser].AgentInput.AgentID)
	require.NotEqual(t, results[winner].AgentInput.ID, results[loser].AgentInput.ID,
		"distinct events reach the same agent")
	for i := range inputs {
		inputs[i].Prepared = prepared
		replay, err := f.Store.Execution().DeliverChannelWorkflow(ctx, inputs[i])
		require.NoError(t, err)
		require.False(t, replay.CreatedInput)
		require.False(t, replay.CreatedAgent)
		require.Equal(t, results[i].AgentInput.ID, replay.AgentInput.ID)
	}
	var workflows, inputsCount int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_workflows`).Scan(&workflows))
	require.Equal(t, 1, workflows)
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'`, prepared.AgentID()).Scan(&inputsCount))
	require.Equal(t, 2, inputsCount)
}

func TestChannelWorkflowRejectsRevokedAccessAndExpiredReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "workflow-live-authority")
	first := f.event(t, ctx, "first")
	created, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
	require.NoError(t, err)
	second := f.event(t, ctx, "second")
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE integration_event_receipts SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, second.Receipt.ReceiptID)
	require.NoError(t, err)
	_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, second)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	// Keep this expired receipt from being the next due receipt in the fixture.
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE integration_event_receipts SET available_at = statement_timestamp() + interval '1 hour' WHERE id = $1`, second.Receipt.ReceiptID)
	require.NoError(t, err)
	third := f.event(t, ctx, "third")
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, created.BindingID))
	_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, third)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "new events cannot recreate a revoked relationship")
	_, err = f.Store.Integrations().GetActiveReceiveBindingForTarget(
		ctx, testProjectID, created.AgentInput.AgentID, created.ChannelID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	var contentInputs int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'`, created.AgentInput.AgentID).Scan(&contentInputs))
	require.Equal(t, 1, contentInputs)
}

func TestChannelWorkflowInputFailureRollsBackLaunchAndBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "workflow-atomic-input")
	input := f.event(t, ctx, "first")
	input.Content.Blocks = json.RawMessage(`[{"type":"unsupported","text":"invalid"}]`)
	_, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.Error(t, err)
	for _, table := range []string{"integration_workflows", "integration_targets", "integration_target_bindings"} {
		var count int
		require.NoError(t, f.Store.pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count))
		require.Zero(t, count, table)
	}
	_, err = f.Store.Execution().GetAgentInProject(ctx, testProjectID, input.Prepared.AgentID())
	require.True(t, errors.Is(err, storeerr.ErrNotFound), "failed first input leaves no agent: %v", err)
	input.Content.Blocks = json.RawMessage(`[{"type":"text","text":"corrected"}]`)
	created, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.NoError(t, err)
	require.True(t, created.CreatedAgent)
	require.True(t, created.CreatedInput)
	require.Equal(t, input.Prepared.AgentID(), created.AgentInput.AgentID)
}

func TestChannelWorkflowRollsBackWhenReceiptExpiresDuringAdmission(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newChannelWorkflowFixture(t, ctx, "workflow-expiry-during-launch")
	input := f.event(t, ctx, "first")
	blocker, err := f.Store.pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(context.WithoutCancel(ctx)) }()
	_, err = blocker.Exec(ctx, `
SELECT profile.id FROM agent_profiles profile
JOIN integration_routes route ON route.agent_profile_id = profile.id
WHERE route.id = $1 FOR UPDATE OF profile`, f.Identity.IntegrationRouteID)
	require.NoError(t, err)
	_, err = f.Store.pool.Exec(ctx, `
UPDATE integration_event_receipts
SET lease_expires_at = statement_timestamp() + interval '2 seconds'
WHERE id = $1`, input.Receipt.ReceiptID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
		done <- err
	}()
	// Admission holds the receipt fence before waiting for this profile. Advance
	// past the actual DB deadline before releasing it, without guessing a sleep.
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockAgentProfile", 1)
	_, err = f.Store.pool.Exec(ctx, `
SELECT pg_sleep(GREATEST(0, EXTRACT(EPOCH FROM lease_expires_at - clock_timestamp())) + 0.01)
FROM integration_event_receipts WHERE id = $1`, input.Receipt.ReceiptID)
	require.NoError(t, err)
	require.NoError(t, blocker.Commit(ctx))
	require.ErrorIs(t, <-done, storeerr.ErrStateTransitionConflict)
	_, err = f.Store.Execution().GetAgentInProject(ctx, testProjectID, input.Prepared.AgentID())
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	var workflows int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_workflows`).Scan(&workflows))
	require.Zero(t, workflows, "an expired receipt leaves no partially admitted workflow")
}

func TestChannelWorkflowReplayPreservesOriginAfterBindingReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "workflow-replaced-binding")
	first := f.event(t, ctx, "first")
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
	require.NoError(t, err)
	first.Prepared, err = f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
	require.NoError(t, err)
	assertReplay := func() {
		t.Helper()
		replay, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
		require.NoError(t, err)
		require.False(t, replay.CreatedInput)
		require.Equal(t, accepted.AgentInput.ID, replay.AgentInput.ID)
		require.Equal(t, accepted.BindingID, replay.BindingID)
		require.Equal(t, accepted.ChannelID, replay.ChannelID)
		require.JSONEq(t, string(accepted.ContentBlocks), string(replay.ContentBlocks))
	}
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, accepted.BindingID))
	assertReplay()
	replacementRoute, err := f.Store.Integrations().CreateIntegrationRoute(
		ctx, integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			DeploymentKey: "replacement", BehaviorKey: "conversation",
		})
	require.NoError(t, err)
	replacement, err := f.Store.Integrations().CreateIntegrationTargetBinding(
		ctx, integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: accepted.AgentInput.AgentID,
			IntegrationInstallID: f.Identity.IntegrationInstallID,
			IntegrationTargetID:  accepted.ChannelID, IntegrationRouteID: replacementRoute.ID,
			ReceiveAllowed: true, Source: "replacement",
		})
	require.NoError(t, err)
	assertReplay()
	// Reprocessing the same immutable receipt after a behavior deployment returns
	// its accepted projection; it never changes the old input or sends a new one.
	first.Content.Blocks = json.RawMessage(`[{"type":"text","text":"new behavior projection"}]`)
	first.ProviderUserID = ""
	assertReplay()
	second, err := f.Store.Execution().DeliverChannelWorkflow(ctx, f.event(t, ctx, "second"))
	require.NoError(t, err)
	require.True(t, second.CreatedInput)
	require.Equal(t, replacement.ID, second.BindingID)
	var count int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'`, accepted.AgentInput.AgentID).Scan(&count))
	require.Equal(t, 2, count)
}

func TestChannelWorkflowBehaviorRemovalRevokesOnlyItsGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "workflow-behavior-revoke")
	first := f.event(t, ctx, "first")
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
	require.NoError(t, err)
	grant, err := f.Store.Integrations().GetIntegrationTargetBinding(ctx, testProjectID, accepted.BindingID)
	require.NoError(t, err)
	require.Equal(t, f.Identity.IntegrationRouteID, grant.IntegrationRouteID)
	independent, err := f.Store.Integrations().CreateIntegrationTargetBinding(
		ctx, integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: accepted.AgentInput.AgentID,
			IntegrationInstallID: f.Identity.IntegrationInstallID,
			IntegrationTargetID:  accepted.ChannelID, ReadAllowed: true, Source: "independent",
		})
	require.NoError(t, err)
	require.NoError(t, f.Store.Integrations().DeleteIntegrationRoute(
		ctx, testProjectID, f.Identity.IntegrationInstallID, f.Identity.IntegrationRouteID))
	_, err = f.Store.Integrations().GetIntegrationTargetBinding(ctx, testProjectID, accepted.BindingID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	remaining, err := f.Store.Integrations().GetIntegrationTargetBinding(ctx, testProjectID, independent.ID)
	require.NoError(t, err)
	require.True(t, remaining.ReadAllowed)
}

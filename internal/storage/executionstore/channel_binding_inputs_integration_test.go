//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type boundChannelInputFixture struct {
	store    *Store
	identity executionstore.ChannelBindingInputIdentity
	install  integrationstore.IntegrationInstallRecord
	binding  integrationstore.IntegrationTargetBindingRecord
}

func newBoundChannelInputFixture(t *testing.T, options ...storage.Option) boundChannelInputFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool, options...)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "bound-input")
	definitionID := createChannelTestDefinition(t, ctx, store, install)
	target, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definitionID,
		ProviderRef: "existing-thread", ProviderRefKind: "thread", DisplayName: "Existing conversation",
	})
	require.NoError(t, err)
	binding, err := store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
			IntegrationTargetID: target.ID, Source: "api", ReceiveAllowed: true,
		})
	require.NoError(t, err)
	return boundChannelInputFixture{
		store: store, install: install, binding: binding,
		identity: executionstore.ChannelBindingInputIdentity{
			ProjectID: testProjectID, IntegrationInstallID: install.ID, BindingID: binding.ID,
			Capabilities: testChannelCapabilities(install.Provider),
		},
	}
}

func (f boundChannelInputFixture) event(t *testing.T, eventID string) executionstore.DeliverBoundChannelInput {
	t.Helper()
	ctx := t.Context()
	_, err := f.store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: f.identity.ProjectID, IntegrationInstallID: f.identity.IntegrationInstallID,
		EventID: eventID, Payload: json.RawMessage(`{"text":"hello"}`), Capabilities: f.identity.Capabilities,
	})
	require.NoError(t, err)
	lease, found, err := f.store.Integrations().ClaimNextIntegrationEvent(ctx,
		integrationstore.ClaimNextIntegrationEventInput{
			Capability: f.identity.Capabilities[0], LeaseDuration: time.Minute,
		})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, eventID, lease.EventID)
	prepared, err := f.store.Execution().PrepareBoundChannelInput(ctx, f.identity)
	require.NoError(t, err)
	return executionstore.DeliverBoundChannelInput{
		Prepared: prepared, InputKey: eventID,
		Receipt: executionstore.ChannelEventLease{
			ReceiptID: lease.ID, LeaseToken: lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration,
		},
		ProviderUserID: "author", ActorDisplayName: "Original author",
		Content: executionstore.PreparedInputContent{Blocks: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
	}
}

// These are all local effects of admission; receipt creation/claim is deliberately
// excluded so failed admission can be compared without conflating receipt intake.
func (f boundChannelInputFixture) rows(t *testing.T) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{
		"agents", "integration_routes", "integration_workflows", "integration_targets", "integration_target_bindings",
		"agent_inputs", "content_blocks", "artifacts", "actors", "agent_wakeups", "integration_event_outcomes",
	} {
		var count int
		require.NoError(t, f.store.pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count))
		counts[table] = count
	}
	return counts
}

func TestBoundChannelInputAdmitsExistingReceiveOnlyAgentWithoutWorkflow(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	before := f.rows(t)
	require.Zero(t, before["integration_routes"])
	require.Zero(t, before["integration_workflows"])
	require.True(t, f.binding.ReceiveAllowed)
	require.False(t, f.binding.ReadAllowed)
	require.False(t, f.binding.SendAllowed)
	require.Equal(t, uuid.Nil, f.binding.IntegrationRouteID)
	input := f.event(t, "existing-agent")
	require.Equal(t, before, f.rows(t), "preparing the recipient must not create durable associations")
	input.Metadata = json.RawMessage(`{"provider_message":"message-1"}`)
	input.DeliveryMode = executionstore.DeliveryModeSteering
	result, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.NoError(t, err)
	require.True(t, result.CreatedInput)
	require.False(t, result.CreatedAgent)
	require.Equal(t, f.binding.AgentID, input.Prepared.AgentID())
	require.Equal(t, f.binding.AgentID, result.AgentInput.AgentID)
	require.Equal(t, f.binding.ID, result.BindingID)
	require.Equal(t, result.BindingID, result.AgentInput.IntegrationTargetBindingID)
	require.Equal(t, f.binding.IntegrationTargetID, result.ChannelID)
	require.Equal(t, result.ChannelID, result.AgentInput.IntegrationTargetID)
	require.Equal(t, integrationstore.IdempotencyScope(f.install), result.AgentInput.IdempotencyScope)
	require.Equal(t, input.InputKey, result.AgentInput.InputIdempotencyKey)
	require.Equal(t, input.DeliveryMode, result.AgentInput.DeliveryMode)
	require.JSONEq(t, string(input.Metadata), string(result.AgentInput.Metadata))
	require.JSONEq(t, string(input.Content.Blocks), string(result.ContentBlocks))
	actor, err := f.store.Execution().GetActor(t.Context(), testProjectID, result.AgentInput.ActorID)
	require.NoError(t, err)
	require.Equal(t, f.install.Provider, actor.Provider)
	require.Equal(t, f.install.ProviderTenantID, actor.ProviderTenantID)
	require.Equal(t, input.ProviderUserID, actor.ProviderUserID)
	after := f.rows(t)
	for _, table := range []string{
		"agents", "integration_routes", "integration_workflows", "integration_targets", "integration_target_bindings",
	} {
		require.Equal(t, before[table], after[table], table)
	}
	require.Equal(t, before["agent_inputs"]+1, after["agent_inputs"])
	require.Equal(t, before["content_blocks"]+1, after["content_blocks"])
	require.Equal(t, before["integration_event_outcomes"]+1, after["integration_event_outcomes"])
	var wakeups int
	require.NoError(t, f.store.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agent_wakeups WHERE agent_id = $1`, f.binding.AgentID).Scan(&wakeups))
	require.Equal(t, 1, wakeups)
}

func TestBoundChannelInputSemanticAndReceiptReplayAfterRevoke(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	input := f.event(t, "receipt-original")
	input.InputKey = "provider-message"
	original, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, f.store.Integrations().RevokeIntegrationTargetBinding(t.Context(), testProjectID, f.binding.ID))
	before := f.rows(t)
	replay := f.event(t, "receipt-sibling") // Fresh preparation may resolve historical identities for replay.
	replay.InputKey = input.InputKey
	replay.ProviderUserID = ""
	replay.Content.Blocks = json.RawMessage(`[{"type":"unsupported"}]`)
	replay.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: input.InputKey, Exists: false}
	for range 2 {
		got, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), replay)
		require.NoError(t, err)
		require.False(t, got.CreatedInput)
		require.False(t, got.CreatedAgent)
		require.Equal(t, original.AgentInput.ID, got.AgentInput.ID)
		require.Equal(t, original.AgentInput.ActorID, got.AgentInput.ActorID)
		require.Equal(t, original.BindingID, got.BindingID)
		require.JSONEq(t, string(original.ContentBlocks), string(got.ContentBlocks))
		replay.InputKey = "changed-key-on-same-receipt"
	}
	before["integration_event_outcomes"]++
	require.Equal(t, before, f.rows(t), "replay records its outcome but neither changes content nor revives authority")
	_, err = f.store.Execution().DeliverBoundChannelInput(t.Context(), f.event(t, "new-message-after-revoke"))
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.Equal(t, before, f.rows(t))
}

func TestBoundChannelInputConcurrentSemanticReceiptsCreateOneInput(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	inputs := []executionstore.DeliverBoundChannelInput{f.event(t, "message"), f.event(t, "mention")}
	before := f.rows(t)
	type completion struct {
		result executionstore.ChannelInputResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan completion, len(inputs))
	for _, input := range inputs {
		input.InputKey = "same-provider-message"
		go func() {
			<-start
			result, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
			results <- completion{result, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, first.result.AgentInput.ID, second.result.AgentInput.ID)
	require.NotEqual(t, first.result.CreatedInput, second.result.CreatedInput)
	after := f.rows(t)
	require.Equal(t, before["agent_inputs"]+1, after["agent_inputs"])
	require.Equal(t, before["integration_event_outcomes"]+2, after["integration_event_outcomes"])
}

func TestBoundChannelInputPreconditionRequiresRerender(t *testing.T) {
	t.Parallel()
	for _, firstKey := range []string{"plain", "files"} {
		t.Run(firstKey+"-first", func(t *testing.T) {
			t.Parallel()
			f := newBoundChannelInputFixture(t)
			first, stale := f.event(t, "first"), f.event(t, "later")
			secondKey := "files"
			if firstKey == secondKey {
				secondKey = "plain"
			}
			first.InputKey, stale.InputKey = firstKey, secondKey
			first.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: secondKey, Exists: false}
			stale.InputPrecondition = &executionstore.ChannelInputPrecondition{InputKey: firstKey, Exists: false}
			_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), first)
			require.NoError(t, err)
			before := f.rows(t)
			_, err = f.store.Execution().DeliverBoundChannelInput(t.Context(), stale)
			require.ErrorIs(t, err, executionstore.ErrChannelInputPreconditionChanged)
			require.Equal(t, before, f.rows(t))
			stale.InputPrecondition.Exists = true
			stale.Content.Blocks = json.RawMessage(`[{"type":"text","text":"rendered after observing the first input"}]`)
			accepted, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), stale)
			require.NoError(t, err)
			require.True(t, accepted.CreatedInput)
			require.JSONEq(t, string(stale.Content.Blocks), string(accepted.ContentBlocks))
		})
	}
}

func (f boundChannelInputFixture) replaceBinding(t *testing.T, receive bool) boundChannelInputFixture {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, f.store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, f.binding.ID))
	binding, err := f.store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: f.binding.AgentID, IntegrationInstallID: f.install.ID,
			IntegrationTargetID: f.binding.IntegrationTargetID, Source: "api",
			ReceiveAllowed: receive, SendAllowed: !receive,
		})
	require.NoError(t, err)
	require.NotEqual(t, f.binding.ID, binding.ID)
	f.binding, f.identity.BindingID = binding, binding.ID
	return f
}

// Inject a failure after input, actor, artifact and wakeup writes, at the last
// outcome insert. Each test owns an isolated database and the trigger's lifetime.
func (f boundChannelInputFixture) rejectOutcome(t *testing.T) func() {
	t.Helper()
	_, err := f.store.pool.Exec(t.Context(), `
CREATE FUNCTION reject_bound_input_outcome() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'bound input outcome failure'; END;
$$;
CREATE TRIGGER reject_bound_input_outcome BEFORE INSERT ON integration_event_outcomes
FOR EACH ROW EXECUTE FUNCTION reject_bound_input_outcome();`)
	require.NoError(t, err)
	return func() {
		t.Helper()
		_, err := f.store.pool.Exec(context.WithoutCancel(t.Context()),
			`DROP TRIGGER reject_bound_input_outcome ON integration_event_outcomes;
DROP FUNCTION reject_bound_input_outcome();`)
		require.NoError(t, err)
	}
}

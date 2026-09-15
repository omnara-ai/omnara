//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestBoundChannelInputPreparationRequiresExactProjectInstallBindingAndCapability(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	ctx := t.Context()
	_, _, _, otherInstall := createChannelLifecycleFixture(t, ctx, f.store, "other-install")
	otherProject := uuid.New()
	_, err := f.store.pool.Exec(ctx, `
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Other project', 'bound-input-other-project', now(), now())`, otherProject, testOrgID)
	require.NoError(t, err)
	before := f.rows(t)
	for name, change := range map[string]func(*executionstore.ChannelBindingInputIdentity){
		"project":      func(i *executionstore.ChannelBindingInputIdentity) { i.ProjectID = otherProject },
		"installation": func(i *executionstore.ChannelBindingInputIdentity) { i.IntegrationInstallID = otherInstall.ID },
		"binding":      func(i *executionstore.ChannelBindingInputIdentity) { i.BindingID = uuid.New() },
		"capability pair": func(i *executionstore.ChannelBindingInputIdentity) {
			i.Capabilities = []channelconnector.Capability{
				{ConnectorKey: testChannelConnector, Provider: "other_provider"},
				{ConnectorKey: "other_connector", Provider: testChannelProvider},
			}
		},
	} {
		identity := f.identity
		change(&identity)
		_, err := f.store.Execution().PrepareBoundChannelInput(ctx, identity)
		require.ErrorIs(t, err, storeerr.ErrNotFound, name)
	}
	require.Equal(t, before, f.rows(t))
}

func TestBoundChannelInputRejectsForeignReceiptAndPreparedStore(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	input := f.event(t, "bound-receipt")
	ctx := t.Context()
	_, _, _, otherInstall := createChannelLifecycleFixture(t, ctx, f.store, "receipt-other-install")
	// Claim a real receipt from another installation, keeping the original prepared recipient.
	_, err := f.store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: otherInstall.ID,
		EventID: "foreign-receipt", Payload: json.RawMessage(`{}`), Capabilities: f.identity.Capabilities,
	})
	require.NoError(t, err)
	lease, found, err := f.store.Integrations().ClaimNextIntegrationEvent(ctx,
		integrationstore.ClaimNextIntegrationEventInput{Capability: f.identity.Capabilities[0], LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "foreign-receipt", lease.EventID)
	before := f.rows(t)
	foreign := input
	foreign.Receipt = executionstore.ChannelEventLease{
		ReceiptID: lease.ID, LeaseToken: lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration,
	}
	_, err = f.store.Execution().DeliverBoundChannelInput(ctx, foreign)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	otherStore := newSecretIntegrationStore(f.store.pool)
	_, err = otherStore.Execution().DeliverBoundChannelInput(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	require.Equal(t, before, f.rows(t))
}

func TestBoundChannelInputNeverBorrowsReplacementReceiveAuthority(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	input := f.event(t, "prepared-before-revoke")
	replacement := f.replaceBinding(t, true)
	before := f.rows(t)
	_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.Equal(t, before, f.rows(t))
	input.Prepared, err = f.store.Execution().PrepareBoundChannelInput(t.Context(), replacement.identity)
	require.NoError(t, err)
	accepted, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.NoError(t, err)
	require.True(t, accepted.CreatedInput)
	require.Equal(t, replacement.binding.ID, accepted.BindingID)
}

func TestBoundChannelInputSendOnlyBindingCannotReceive(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t).replaceBinding(t, false)
	input := f.event(t, "send-only")
	before := f.rows(t)
	_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.Equal(t, before, f.rows(t))
}

func TestBoundChannelInputRechecksDisabledInstallationEvenOnReplay(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	accepted := f.event(t, "before-disable")
	_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), accepted)
	require.NoError(t, err)
	newInput := f.event(t, "prepared-before-disable")
	changed, err := f.store.Integrations().DisableIntegrationInstall(t.Context(),
		integrationstore.DisableIntegrationInstallInput{
			ProjectID: testProjectID, ID: f.install.ID, ExpectedOAuthFlowID: &f.install.LastOAuthFlowID,
		})
	require.NoError(t, err)
	require.True(t, changed)
	before := f.rows(t)
	for _, input := range []executionstore.DeliverBoundChannelInput{accepted, newInput} {
		_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}
	require.Equal(t, before, f.rows(t))
}

func TestBoundChannelInputRequiresCurrentLeaseForAdmissionAndReplay(t *testing.T) {
	t.Parallel()
	for _, replay := range []bool{false, true} {
		name := "new-input"
		if replay {
			name = "accepted-replay"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newBoundChannelInputFixture(t)
			input := f.event(t, "leased-input")
			if replay {
				_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
				require.NoError(t, err)
			}
			before := f.rows(t)
			wrongToken, wrongGeneration := input, input
			wrongToken.Receipt.LeaseToken = uuid.New()
			wrongGeneration.Receipt.LeaseGeneration++
			for _, stale := range []executionstore.DeliverBoundChannelInput{wrongToken, wrongGeneration} {
				_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), stale)
				require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
			}
			_, err := f.store.pool.Exec(t.Context(),
				`UPDATE integration_event_receipts SET lease_expires_at = now() - interval '1 second' WHERE id = $1`,
				input.Receipt.ReceiptID)
			require.NoError(t, err)
			_, err = f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
			require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
			require.Equal(t, before, f.rows(t))
		})
	}
}

func TestBoundChannelInputSemanticKeyCannotMoveToAnotherChannel(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	input := f.event(t, "original-channel")
	_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.NoError(t, err)
	target, err := f.store.Integrations().CreateIntegrationTarget(t.Context(),
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, IntegrationInstallID: f.install.ID,
			ChannelDefinitionID: createChannelTestDefinition(t, t.Context(), f.store, f.install),
			ProviderRef:         "another-thread", ProviderRefKind: "thread",
		})
	require.NoError(t, err)
	binding, err := f.store.Integrations().CreateIntegrationTargetBinding(t.Context(),
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: f.binding.AgentID, IntegrationInstallID: f.install.ID,
			IntegrationTargetID: target.ID, Source: "api", ReceiveAllowed: true,
		})
	require.NoError(t, err)
	f.identity.BindingID = binding.ID
	other := f.event(t, "other-channel-receipt")
	other.InputKey = input.InputKey
	before := f.rows(t)
	_, err = f.store.Execution().DeliverBoundChannelInput(t.Context(), other)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.Equal(t, before, f.rows(t))
}

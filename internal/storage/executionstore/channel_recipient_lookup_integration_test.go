//go:build integration

package executionstore_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func (f boundChannelInputFixture) lookupInput(
	input executionstore.DeliverBoundChannelInput,
) executionstore.LookupChannelRecipientsInput {
	return executionstore.LookupChannelRecipientsInput{
		ProjectID: f.identity.ProjectID, IntegrationInstallID: f.identity.IntegrationInstallID,
		ProviderRef: "existing-thread", Receipt: input.Receipt, Capabilities: f.identity.Capabilities,
		InputKeys: []string{input.InputKey, "missing-message"}, Limit: 2,
	}
}

func TestChannelRecipientLookupKeepsInputKeysWithinInstallation(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	firstReceipt := f.event(t, "same-message-key")
	_, _, _, install := createChannelLifecycleFixture(t, t.Context(), f.store, "lookup-key-scope")
	target, err := f.store.Integrations().CreateIntegrationTarget(t.Context(),
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			ChannelDefinitionID: createChannelTestDefinition(t, t.Context(), f.store, install),
			ProviderRef:         "existing-thread", ProviderRefKind: "thread",
		})
	require.NoError(t, err)
	binding, err := f.store.Integrations().CreateIntegrationTargetBinding(t.Context(),
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: f.binding.AgentID, IntegrationInstallID: install.ID,
			IntegrationTargetID: target.ID, Source: "api", ReceiveAllowed: true,
		})
	require.NoError(t, err)
	other := f
	other.install, other.binding = install, binding
	other.identity.IntegrationInstallID, other.identity.BindingID = install.ID, binding.ID
	accepted := other.event(t, "other-install-event")
	accepted.InputKey = firstReceipt.InputKey
	_, err = other.store.Execution().DeliverBoundChannelInput(t.Context(), accepted)
	require.NoError(t, err)
	first, err := f.store.Execution().LookupChannelRecipients(t.Context(), f.lookupInput(firstReceipt))
	require.NoError(t, err)
	require.Equal(t, f.binding.IntegrationTargetID, first.ChannelID)
	require.Equal(t, []executionstore.ChannelRecipient{{
		AgentID: f.binding.AgentID, BindingID: f.binding.ID, InputKeys: []string{},
	}}, first.Recipients)
	second, err := other.store.Execution().LookupChannelRecipients(t.Context(), other.lookupInput(accepted))
	require.NoError(t, err)
	require.Equal(t, target.ID, second.ChannelID)
	require.Equal(t, []executionstore.ChannelRecipient{{
		AgentID: binding.AgentID, BindingID: binding.ID, InputKeys: []string{accepted.InputKey},
	}}, second.Recipients, "the same provider reference and agent do not merge installation input scopes")
}

func TestChannelRecipientLookupPagesDistinctAgentsWithScopedInputKeys(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	input := f.event(t, "known-message")
	_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.NoError(t, err)
	expected := map[ID][]string{f.binding.AgentID: {input.InputKey}}
	bindings := map[ID]ID{f.binding.ID: f.binding.AgentID}
	for range 2 {
		agentID := mustCreateAgent(t, t.Context(), f.store)
		expected[agentID] = []string{}
		binding, err := f.store.Integrations().CreateIntegrationTargetBinding(t.Context(),
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agentID, IntegrationInstallID: f.install.ID,
				IntegrationTargetID: f.binding.IntegrationTargetID, Source: "api", ReceiveAllowed: true,
			})
		require.NoError(t, err)
		bindings[binding.ID] = agentID
	}
	for _, receive := range []bool{true, false} {
		agentID := f.binding.AgentID
		if !receive {
			agentID = mustCreateAgent(t, t.Context(), f.store)
		}
		binding, err := f.store.Integrations().CreateIntegrationTargetBinding(t.Context(),
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agentID, IntegrationInstallID: f.install.ID,
				IntegrationTargetID: f.binding.IntegrationTargetID, Source: "other-grant",
				ReceiveAllowed: receive, SendAllowed: true,
			})
		require.NoError(t, err)
		if receive {
			bindings[binding.ID] = agentID
		}
	}
	before := f.rows(t)
	lookup := f.lookupInput(input)
	seen := make(map[ID][]string)
	for pageNumber := range 2 {
		page, err := f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
		require.NoError(t, err)
		require.Equal(t, f.binding.IntegrationTargetID, page.ChannelID)
		require.True(t, page.HasReceiveBindingHistory)
		require.False(t, page.WorkflowStarted, "a bound admission outcome is not a workflow launch")
		require.Equal(t, pageNumber == 0, page.HasMore)
		require.Len(t, page.Recipients, 2-pageNumber)
		for _, recipient := range page.Recipients {
			require.NotContains(t, seen, recipient.AgentID)
			require.Equal(t, recipient.AgentID, bindings[recipient.BindingID])
			require.NotNil(t, recipient.InputKeys)
			seen[recipient.AgentID] = recipient.InputKeys
			lookup.AfterAgentID = recipient.AgentID
		}
	}
	require.Equal(t, expected, seen, "input presence is per agent; duplicate grants must not consume a page slot")
	last, err := f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
	require.NoError(t, err)
	require.Empty(t, last.Recipients)
	require.False(t, last.HasMore)
	require.Equal(t, before, f.rows(t), "lookup never creates recipients or workflows")
}

func TestChannelRecipientLookupPreservesRevokedHistoryWithoutGrantingReceive(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	input := f.event(t, "history")
	lookup := f.lookupInput(input)
	selected, err := f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
	require.NoError(t, err)
	require.Len(t, selected.Recipients, 1)
	require.Equal(t, f.binding.ID, selected.Recipients[0].BindingID)
	f = f.replaceBinding(t, false)
	before := f.rows(t)
	_, err = f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a prior lookup cannot authorize admission after revocation")
	history, err := f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
	require.NoError(t, err)
	require.True(t, history.HasReceiveBindingHistory, "revoked receive permission remains listener history")
	require.False(t, history.WorkflowStarted)
	require.Equal(t, f.binding.IntegrationTargetID, history.ChannelID)
	require.Empty(t, history.Recipients)
	lookup.ProviderRef = "unregistered-thread"
	unknown, err := f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
	require.NoError(t, err)
	require.Equal(t, NilID, unknown.ChannelID)
	require.False(t, unknown.HasReceiveBindingHistory)
	require.False(t, unknown.WorkflowStarted)
	require.Empty(t, unknown.Recipients)
	require.False(t, unknown.HasMore)
	require.Equal(t, before, f.rows(t))
}

func TestChannelRecipientLookupReadOrSendPermissionDoesNotCreateReceiveHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		read, send bool
	}{
		{name: "send-only", send: true},
		{name: "read-only", read: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newBoundChannelInputFixture(t)
			input := f.event(t, "non-listener-permission")
			// The original channel has receive history for this same agent and
			// installation. The new channel has never had a receive grant.
			target, err := f.store.Integrations().CreateIntegrationTarget(t.Context(),
				integrationstore.CreateIntegrationTargetInput{
					ProjectID: testProjectID, IntegrationInstallID: f.install.ID,
					ChannelDefinitionID: createChannelTestDefinition(t, t.Context(), f.store, f.install),
					ProviderRef:         "non-listener-thread", ProviderRefKind: "thread",
				})
			require.NoError(t, err)
			binding, err := f.store.Integrations().CreateIntegrationTargetBinding(t.Context(),
				integrationstore.CreateIntegrationTargetBindingInput{
					ProjectID: testProjectID, AgentID: f.binding.AgentID, IntegrationInstallID: f.install.ID,
					IntegrationTargetID: target.ID, Source: "api", ReadAllowed: tc.read, SendAllowed: tc.send,
				})
			require.NoError(t, err)
			require.False(t, binding.ReceiveAllowed)
			lookup := f.lookupInput(input)
			original, err := f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
			require.NoError(t, err)
			require.True(t, original.HasReceiveBindingHistory)
			lookup.ProviderRef = target.ProviderRef
			before := f.rows(t)
			for _, revoked := range []bool{false, true} {
				if revoked {
					require.NoError(t, f.store.Integrations().RevokeIntegrationTargetBinding(
						t.Context(), testProjectID, binding.ID))
				}
				got, err := f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
				require.NoError(t, err)
				require.Equal(t, target.ID, got.ChannelID)
				require.False(t, got.HasReceiveBindingHistory, "read/send permission is not listener history; revoked=%t", revoked)
				require.False(t, got.WorkflowStarted)
				require.Empty(t, got.Recipients)
				require.False(t, got.HasMore)
			}
			require.Equal(t, before, f.rows(t), "lookup must not claim the thread or create any receive subscription")
		})
	}
}

func TestChannelRecipientLookupRequiresScopedAuthorityAndCurrentReceipt(t *testing.T) {
	t.Parallel()
	f := newBoundChannelInputFixture(t)
	input := f.event(t, "lookup-lease")
	lookup := f.lookupInput(input)
	_, _, _, otherInstall := createChannelLifecycleFixture(t, t.Context(), f.store, "lookup-other-install")
	before := f.rows(t)
	for _, tc := range []struct {
		name   string
		change func(*executionstore.LookupChannelRecipientsInput)
		want   error
	}{
		{"project", func(i *executionstore.LookupChannelRecipientsInput) { i.ProjectID = uuid.New() }, storeerr.ErrNotFound},
		{"installation", func(i *executionstore.LookupChannelRecipientsInput) {
			i.IntegrationInstallID = otherInstall.ID
		}, storeerr.ErrStateTransitionConflict},
		{"capability pair", func(i *executionstore.LookupChannelRecipientsInput) {
			i.Capabilities = []channelconnector.Capability{
				{ConnectorKey: testChannelConnector, Provider: "other_provider"},
				{ConnectorKey: "other_connector", Provider: testChannelProvider},
			}
		}, storeerr.ErrNotFound},
		{"lease token", func(i *executionstore.LookupChannelRecipientsInput) {
			i.Receipt.LeaseToken = uuid.New()
		}, storeerr.ErrStateTransitionConflict},
		{"lease generation", func(i *executionstore.LookupChannelRecipientsInput) {
			i.Receipt.LeaseGeneration++
		}, storeerr.ErrStateTransitionConflict},
		{"no lease", func(i *executionstore.LookupChannelRecipientsInput) {
			i.Receipt = executionstore.ChannelEventLease{}
		}, storeerr.ErrInvalidRequest},
		{"page limit", func(i *executionstore.LookupChannelRecipientsInput) { i.Limit = 101 }, storeerr.ErrInvalidRequest},
		{"reference bound", func(i *executionstore.LookupChannelRecipientsInput) {
			i.ProviderRef = strings.Repeat("x", 513)
		}, storeerr.ErrInvalidRequest},
		{"input key bound", func(i *executionstore.LookupChannelRecipientsInput) {
			i.InputKeys = []string{strings.Repeat("x", 513)}
		}, storeerr.ErrInvalidRequest},
	} {
		bad := lookup
		tc.change(&bad)
		_, err := f.store.Execution().LookupChannelRecipients(t.Context(), bad)
		require.ErrorIs(t, err, tc.want, tc.name)
	}
	_, err := f.store.pool.Exec(t.Context(),
		`UPDATE integration_event_receipts SET lease_expires_at = now() - interval '1 second' WHERE id = $1`,
		input.Receipt.ReceiptID)
	require.NoError(t, err)
	_, err = f.store.Execution().LookupChannelRecipients(t.Context(), lookup)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.Equal(t, before, f.rows(t))
}

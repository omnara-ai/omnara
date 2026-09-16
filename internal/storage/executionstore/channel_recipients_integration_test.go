//go:build integration

package executionstore_test

import (
	"bytes"
	"encoding/json"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestChannelRecipientsDeduplicateBeforePagingWithoutCombiningGrants(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newPublicChannelFixture(t, ctx, "recipient-pages")
	store := f.store.Integrations()
	grant := func(agentID uuid.UUID, source string, receive, send bool) integrationstore.IntegrationTargetBindingRecord {
		t.Helper()
		input := f.grants(receive)
		input.AgentID, input.Source, input.SendAllowed = agentID, source, send
		binding, err := store.CreateIntegrationTargetBinding(ctx, input)
		require.NoError(t, err)
		return binding
	}
	first := grant(f.agent.ID, "first", true, false)
	second := grant(f.agent.ID, "second", true, true)
	expected := []integrationstore.IntegrationTargetBindingRecord{first}
	if bytes.Compare(first.ID[:], second.ID[:]) > 0 {
		expected[0] = second
	}
	for range 2 {
		agentID := mustCreateAgent(t, ctx, f.store)
		grant(agentID, "send-only", false, true)
		expected = append(expected, grant(agentID, "receiver", true, false))
	}
	grant(mustCreateAgent(t, ctx, f.store), "only-send", false, true)
	revoked := grant(mustCreateAgent(t, ctx, f.store), "revoked", true, false)
	require.NoError(t, store.RevokeIntegrationTargetBinding(ctx, testProjectID, revoked.ID))
	archived := grant(mustCreateAgent(t, ctx, f.store), "archived", true, false)
	_, _, err := f.store.Execution().ArchiveAgent(ctx, testProjectID, archived.AgentID, userPrincipal(f.user.ID))
	require.NoError(t, err)
	sort.Slice(expected, func(i, j int) bool {
		return bytes.Compare(expected[i].AgentID[:], expected[j].AgentID[:]) < 0
	})
	page, err := store.ListChannelReceiveBindings(ctx, testProjectID, f.install.ID, f.target.ID, uuid.Nil, 2)
	require.NoError(t, err)
	require.Equal(t, expected[:2], page, "two rows mean two agents, without combining permissions from sibling grants")
	last, err := store.ListChannelReceiveBindings(ctx, testProjectID, f.install.ID, f.target.ID, page[1].AgentID, 2)
	require.NoError(t, err)
	require.Equal(t, expected[2:], last)
	empty, err := store.ListChannelReceiveBindings(ctx, testProjectID, f.install.ID, f.target.ID, last[0].AgentID, 2)
	require.NoError(t, err)
	require.Empty(t, empty)
	for _, binding := range append(page, last...) {
		require.Equal(t, uuid.Nil, binding.IntegrationRouteID, "external receive grants need neither app nor route")
	}
}

func TestChannelRecipientHistoryDoesNotAuthorizeNewInput(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newPublicChannelFixture(t, ctx, "recipient-history")
	store := f.store.Integrations()
	_, err := store.CreateIntegrationTargetBinding(ctx, f.grants(false))
	require.NoError(t, err)
	noReceivers, err := store.ListChannelReceiveBindings(ctx, testProjectID, f.install.ID, f.target.ID, uuid.Nil, 1)
	require.NoError(t, err)
	require.Empty(t, noReceivers)
	binding, err := store.CreateIntegrationTargetBinding(ctx, f.grants(true))
	require.NoError(t, err)
	page, err := store.ListChannelReceiveBindings(ctx, testProjectID, f.install.ID, f.target.ID, uuid.Nil, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, binding.ID, page[0].ID)
	require.NoError(t, store.RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))
	replacement, err := store.CreateIntegrationTargetBinding(ctx, f.grants(true))
	require.NoError(t, err)
	require.NotEqual(t, binding.ID, replacement.ID)
	identity, err := store.GetChannelBindingIdentity(ctx, testProjectID, f.install.ID, binding.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ChannelBindingIdentity{
		ID: binding.ID, ProjectID: testProjectID, AgentID: f.agent.ID,
		IntegrationInstallID: f.install.ID, IntegrationTargetID: f.target.ID,
	}, identity)
	tx, err := f.store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = store.GetActiveReceiveBindingTx(ctx, tx,
		testProjectID, f.agent.ID, f.install.ID, f.target.ID, binding.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "discovery and historical identity never substitute the replacement")
	require.NoError(t, tx.Rollback(ctx))
	for _, scope := range []struct{ project, install, target uuid.UUID }{
		{uuid.New(), f.install.ID, f.target.ID},
		{testProjectID, uuid.New(), f.target.ID},
		{testProjectID, f.install.ID, uuid.New()},
	} {
		page, err = store.ListChannelReceiveBindings(ctx, scope.project, scope.install, scope.target, uuid.Nil, 1)
		require.NoError(t, err)
		require.Empty(t, page)
		if scope.target == f.target.ID {
			_, err = store.GetChannelBindingIdentity(ctx, scope.project, scope.install, binding.ID)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		}
	}
	_, err = store.GetChannelBindingIdentity(ctx, testProjectID, f.install.ID, uuid.New())
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.NoError(t, store.DeleteIntegrationInstall(ctx, testProjectID, f.install.ID))
	retired, err := store.GetChannelBindingIdentity(ctx, testProjectID, f.install.ID, binding.ID)
	require.NoError(t, err)
	require.Equal(t, identity, retired)
}

func TestChannelRecipientsRequireLiveAuthorityButHistorySurvives(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, statement string
		scope           int
	}{
		{"app_disabled", `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, 0},
		{"app_deleted", `UPDATE integration_apps SET deleted_at = statement_timestamp() WHERE id = $1`, 0},
		{"install_disabled", `UPDATE integration_installs SET state = 'disabled' WHERE id = $1`, 1},
		{"install_deleted", `UPDATE integration_installs SET deleted_at = statement_timestamp() WHERE id = $1`, 1},
		{"target_deleted", `UPDATE integration_targets SET deleted_at = statement_timestamp() WHERE id = $1`, 2},
		{"project_deleted", `UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1`, 3},
		{"org_deleted", `UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1`, 4},
		{"route_disabled", `UPDATE integration_routes SET state = 'disabled' WHERE id = $1`, 5},
		{"route_deleted", `UPDATE integration_routes SET deleted_at = statement_timestamp() WHERE id = $1`, 5},
		{"agent_archived", `UPDATE agents SET state = 'archived', archived_at = statement_timestamp() WHERE id = $1`, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelAuthorityFixture(t, ctx, "recipients")
			store := f.Store.Integrations()
			receipt, err := store.ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
				ProjectID: testProjectID, IntegrationInstallID: f.InstallID,
				EventID: "routing", Payload: json.RawMessage(`{}`), Capabilities: testChannelCapabilities(testChannelProvider),
			})
			require.NoError(t, err)
			route, err := store.CreateIntegrationRoute(ctx, integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: f.InstallID,
				DeploymentKey: "route", BehaviorKey: "conversation", State: integrationstore.IntegrationRouteStateActive,
			})
			require.NoError(t, err)
			input := f.BindingInput("route")
			input.IntegrationRouteID, input.ReceiveAllowed = route.ID, true
			binding, err := store.CreateIntegrationTargetBinding(ctx, input)
			require.NoError(t, err)
			page, err := store.ListChannelReceiveBindings(ctx, testProjectID, f.InstallID, f.Target.ID, uuid.Nil, 1)
			require.NoError(t, err)
			require.Len(t, page, 1)
			// Change only this authority predicate; do not let cleanup revoke the
			// binding and conceal a missing parent-lifecycle check in discovery.
			ownerIDs := []uuid.UUID{f.AppID, f.InstallID, f.Target.ID, testProjectID, testOrgID, route.ID, f.AgentID}
			_, err = f.Store.pool.Exec(ctx, tc.statement, ownerIDs[tc.scope])
			require.NoError(t, err)
			page, err = store.ListChannelReceiveBindings(ctx, testProjectID, f.InstallID, f.Target.ID, uuid.Nil, 1)
			require.NoError(t, err)
			require.Empty(t, page)
			routing, err := store.LookupChannelReceiptRouting(ctx,
				testProjectID, f.InstallID, f.Target.ProviderRef, receipt.ID)
			require.NoError(t, err)
			if tc.name == "target_deleted" {
				require.Equal(t, integrationstore.ChannelReceiptRouting{}, routing,
					"a retired target is not a current channel at the provider address")
			} else {
				require.Equal(t, integrationstore.ChannelReceiptRouting{
					ChannelID: f.Target.ID, HasReceiveBindingHistory: true,
				}, routing, "retirement does not erase a current channel's prior receive history")
			}
			identity, err := store.GetChannelBindingIdentity(ctx, testProjectID, f.InstallID, binding.ID)
			require.NoError(t, err)
			require.Equal(t, binding.ID, identity.ID)
			require.Equal(t, f.AgentID, identity.AgentID)
		})
	}
}

func TestChannelRecipientInputKeyLookupBatchesExactAgentAndScope(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newPublicChannelFixture(t, ctx, "recipient-input-keys")
	agentIDs := []uuid.UUID{f.agent.ID, mustCreateAgent(t, ctx, f.store), mustCreateAgent(t, ctx, f.store)}
	var scope string
	for _, agentID := range agentIDs {
		grants := f.grants(true)
		grants.AgentID = agentID
		_, err := f.store.Integrations().CreateIntegrationTargetBinding(ctx, grants)
		require.NoError(t, err)
		for _, key := range []string{"shared", "unrequested"} {
			input := f.input()
			input.AgentID, input.IdempotencyKey = agentID, key
			saved, _, _, err := f.store.Execution().CreateAgentContentInput(ctx, input)
			require.NoError(t, err)
			scope = saved.IdempotencyScope
		}
	}
	params := dbsqlc.GetExistingChannelInputKeysForAgentsParams{
		ProjectID: testProjectID, AgentIds: []uuid.UUID{agentIDs[1], agentIDs[0], agentIDs[1]},
		IdempotencyScope: scope, InputKeys: []string{"shared", "absent", "shared"},
	}
	rows, err := f.store.q.GetExistingChannelInputKeysForAgents(ctx, params)
	require.NoError(t, err)
	expected := []dbsqlc.GetExistingChannelInputKeysForAgentsRow{
		{AgentID: agentIDs[0], InputIdempotencyKey: "shared"},
		{AgentID: agentIDs[1], InputIdempotencyKey: "shared"},
	}
	sort.Slice(expected, func(i, j int) bool {
		return bytes.Compare(expected[i].AgentID[:], expected[j].AgentID[:]) < 0
	})
	require.Equal(t, expected, rows, "only requested agents and keys, with no duplicate rows")
	for _, mutate := range []func(*dbsqlc.GetExistingChannelInputKeysForAgentsParams){
		func(p *dbsqlc.GetExistingChannelInputKeysForAgentsParams) { p.ProjectID = uuid.New() },
		func(p *dbsqlc.GetExistingChannelInputKeysForAgentsParams) { p.IdempotencyScope = "other-scope" },
		func(p *dbsqlc.GetExistingChannelInputKeysForAgentsParams) { p.AgentIds = nil },
		func(p *dbsqlc.GetExistingChannelInputKeysForAgentsParams) { p.InputKeys = nil },
	} {
		missing := params
		mutate(&missing)
		rows, err = f.store.q.GetExistingChannelInputKeysForAgents(ctx, missing)
		require.NoError(t, err)
		require.Empty(t, rows)
	}
}

func TestChannelReceiptRoutingDistinguishesPartialWorkflowFromExistingHistory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "receipt-routing")
	store := f.Store.Integrations()
	first := f.event(t, ctx, "first")
	lookup := func(receiptID uuid.UUID) integrationstore.ChannelReceiptRouting {
		t.Helper()
		observed, err := store.LookupChannelReceiptRouting(ctx,
			testProjectID, f.Identity.IntegrationInstallID, first.Target.ProviderRef, receiptID)
		require.NoError(t, err)
		return observed
	}
	require.Equal(t, integrationstore.ChannelReceiptRouting{}, lookup(first.Receipt.ReceiptID),
		"a valid receipt still returns an observation before the channel exists")
	registered, err := store.CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ChannelDefinitionID: f.Definition.ID, ProviderRef: first.Target.ProviderRef, ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	require.Equal(t, integrationstore.ChannelReceiptRouting{ChannelID: registered.ID}, lookup(first.Receipt.ReceiptID),
		"registration without grants or outcomes does not imply prior routing")
	delivered, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ChannelReceiptRouting{
		ChannelID: delivered.ChannelID, HasReceiveBindingHistory: true, WorkflowStarted: true,
	}, lookup(first.Receipt.ReceiptID), "retry can continue this receipt's remaining workflow recipients")
	otherChannel, err := store.CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ChannelDefinitionID: f.Definition.ID, ProviderRef: "other-thread", ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	_, err = store.CreateIntegrationTargetBinding(ctx, integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		AgentID: delivered.AgentInput.AgentID, IntegrationTargetID: otherChannel.ID,
		ReceiveAllowed: true, Source: "api",
	})
	require.NoError(t, err)
	other, err := store.LookupChannelReceiptRouting(ctx,
		testProjectID, f.Identity.IntegrationInstallID, otherChannel.ProviderRef, first.Receipt.ReceiptID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ChannelReceiptRouting{
		ChannelID: otherChannel.ID, HasReceiveBindingHistory: true,
	}, other,
		"same receipt and same bound agent do not move the accepted input's origin to another channel")
	second := f.event(t, ctx, "second")
	existingThread := integrationstore.ChannelReceiptRouting{
		ChannelID: delivered.ChannelID, HasReceiveBindingHistory: true,
	}
	require.Equal(t, existingThread, lookup(second.Receipt.ReceiptID),
		"another receipt's workflow outcome is not this receipt's partial fanout")
	tx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	outcome := integrationstore.IntegrationEventOutcome{
		AgentID: delivered.AgentInput.AgentID, AgentInputID: delivered.AgentInput.ID,
	}
	for _, key := range []string{"binding:" + delivered.BindingID.String(), "workflow_other:unrelated"} {
		require.NoError(t, store.CreateIntegrationEventOutcomeTx(ctx, tx, integrationstore.IntegrationEventOutcomeKey{
			ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			ReceiptID: second.Receipt.ReceiptID, DeliveryKey: key,
		}, outcome))
	}
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, existingThread, lookup(second.Receipt.ReceiptID), "only the exact workflow: namespace counts")
	for _, scope := range []struct{ project, install, receipt uuid.UUID }{
		{uuid.New(), f.Identity.IntegrationInstallID, first.Receipt.ReceiptID},
		{testProjectID, uuid.New(), first.Receipt.ReceiptID},
		{testProjectID, f.Identity.IntegrationInstallID, uuid.New()},
	} {
		_, err := store.LookupChannelReceiptRouting(
			ctx, scope.project, scope.install, first.Target.ProviderRef, scope.receipt)
		require.ErrorIs(t, err, storeerr.ErrNotFound, "receipt identity must belong to the exact project and connection")
	}
	require.NoError(t, store.RevokeIntegrationTargetBinding(ctx, testProjectID, delivered.BindingID))
	require.Equal(t, existingThread, lookup(second.Receipt.ReceiptID), "revocation remains receive binding history")
	require.Equal(t, integrationstore.ChannelReceiptRouting{
		ChannelID: delivered.ChannelID, HasReceiveBindingHistory: true, WorkflowStarted: true,
	}, lookup(first.Receipt.ReceiptID), "receipt progress uses accepted origin despite revocation")
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE integration_targets SET deleted_at = statement_timestamp() WHERE id = $1`, delivered.ChannelID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ChannelReceiptRouting{}, lookup(first.Receipt.ReceiptID),
		"a receipt's other historical origin does not mark a missing current channel as started")
}

func TestChannelReceiptRoutingTracksOnlyReceiveHistory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelAuthorityFixture(t, ctx, "receive-history")
	store := f.Store.Integrations()
	receipt, err := store.ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: f.InstallID,
		EventID: "routing", Payload: json.RawMessage(`{}`), Capabilities: testChannelCapabilities(testChannelProvider),
	})
	require.NoError(t, err)
	assertRouting := func(hasReceiveHistory bool) {
		t.Helper()
		routing, err := store.LookupChannelReceiptRouting(ctx,
			testProjectID, f.InstallID, f.Target.ProviderRef, receipt.ID)
		require.NoError(t, err)
		require.Equal(t, integrationstore.ChannelReceiptRouting{
			ChannelID: f.Target.ID, HasReceiveBindingHistory: hasReceiveHistory,
		}, routing)
	}
	assertRouting(false)
	for _, source := range []string{"send-only", "read-only"} {
		input := f.BindingInput(source)
		input.SendAllowed = source == "send-only"
		input.ReadAllowed = source == "read-only"
		binding, err := store.CreateIntegrationTargetBinding(ctx, input)
		require.NoError(t, err)
		assertRouting(false)
		require.NoError(t, store.RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))
		assertRouting(false)
	}
	input := f.BindingInput("receive-only")
	input.ReceiveAllowed = true
	binding, err := store.CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	assertRouting(true)
	require.NoError(t, store.RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))
	assertRouting(true)
	page, err := store.ListChannelReceiveBindings(ctx, testProjectID, f.InstallID, f.Target.ID, uuid.Nil, 1)
	require.NoError(t, err)
	require.Empty(t, page, "revoked receive history blocks automatic replacement without authorizing delivery")
}

func TestChannelReceiptRoutingPreservesExistingParentWithoutReceiving(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "recipient-parent")
	input := f.event(t, ctx, "child-mention")
	store := f.Store.Integrations()
	parent, err := store.CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ChannelDefinitionID: f.Definition.ID, ProviderRef: "root", ProviderRefKind: "conversation",
	})
	require.NoError(t, err)
	childInput := input.Target
	childInput.ProjectID, childInput.IntegrationInstallID = testProjectID, f.Identity.IntegrationInstallID
	childInput.ParentChannelID = parent.ID
	child, err := store.CreateIntegrationTarget(ctx, childInput)
	require.NoError(t, err)
	_, err = store.CreateIntegrationTargetBinding(ctx, integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		AgentID: mustCreateAgent(t, ctx, f.Store), IntegrationTargetID: child.ID, SendAllowed: true, Source: "api",
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		providerRef         string
		channelID, parentID uuid.UUID
	}{
		{parent.ProviderRef, parent.ID, uuid.Nil},
		{child.ProviderRef, child.ID, parent.ID},
		{"unregistered-thread", uuid.Nil, uuid.Nil},
	} {
		got, err := f.Store.Execution().LookupChannelRecipients(ctx, executionstore.LookupChannelRecipientsInput{
			ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			ProviderRef: tc.providerRef, Receipt: input.Receipt, Capabilities: f.Identity.Capabilities, Limit: 1,
		})
		require.NoError(t, err)
		require.Equal(t, tc.channelID, got.ChannelID)
		require.Equal(t, tc.parentID, got.ParentChannelID, "lookup preserves the registered parent for %s", tc.providerRef)
		require.False(t, got.HasReceiveBindingHistory)
		require.False(t, got.WorkflowStarted)
		require.Empty(t, got.Recipients, "returning a parent does not subscribe the child or inherit grants")
	}
	childInput.ParentChannelID = uuid.Nil
	_, err = store.CreateIntegrationTarget(ctx, childInput)
	require.ErrorIs(t, err, storeerr.ErrConflict, "an existing child still cannot be re-registered without its parent")
}

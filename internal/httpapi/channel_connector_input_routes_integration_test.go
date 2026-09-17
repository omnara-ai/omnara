//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func createBoundChannelHTTPRecipients(
	t *testing.T, f channelWorkflowHTTPFixture, count int,
) (integrationstore.IntegrationTargetRecord, []integrationstore.IntegrationTargetBindingRecord) {
	t.Helper()
	ctx := t.Context()
	target, err := f.project.Store.Integrations().CreateIntegrationTarget(ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID,
			ChannelDefinitionID: mustPublicHTTPID(t, publicid.KindChannelDefinition, f.definitionID),
			ProviderRef:         "bound-thread", ProviderRefKind: "thread", DisplayName: "Bound conversation",
		})
	require.NoError(t, err)
	bindings := make([]integrationstore.IntegrationTargetBindingRecord, 0, count)
	for range count {
		launch := createHTTPRuntimeAgent(t, ctx, f.project.Store, f.project.OrgUUID,
			f.project.ProjectUUID, f.project.AdminUserUUID, "bound-"+uuid.NewString())
		binding, err := f.project.Store.Integrations().CreateIntegrationTargetBinding(ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID, IntegrationTargetID: target.ID,
				AgentID: launch.Agent.ID, Source: "api", ReceiveAllowed: true,
			})
		require.NoError(t, err)
		bindings = append(bindings, binding)
	}
	return target, bindings
}

func boundChannelHTTPInput(
	t *testing.T,
	body openapi.DeliverChannelConnectorWorkflowRequest,
	binding integrationstore.IntegrationTargetBindingRecord,
) openapi.DeliverChannelConnectorInputRequest {
	t.Helper()
	return openapi.DeliverChannelConnectorInputRequest{
		BindingId: testPublicID(t, publicid.KindIntegrationBinding, binding.ID),
		Receipt:   body.Receipt, InputKey: body.InputKey, Author: body.Author,
		ContentBlocks: body.ContentBlocks, Metadata: body.Metadata,
	}
}

func TestChannelConnectorRecipientsDistinguishReceiveHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		receive bool
		revoke  bool
	}{
		{name: "send-only"},
		{name: "revoked-receive", receive: true, revoke: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelWorkflowHTTPFixture(t)
			target, _ := createBoundChannelHTTPRecipients(t, f, 0)
			launch := createHTTPRuntimeAgent(t, ctx, f.project.Store, f.project.OrgUUID,
				f.project.ProjectUUID, f.project.AdminUserUUID, "history-"+tc.name)
			binding, err := f.project.Store.Integrations().CreateIntegrationTargetBinding(ctx,
				integrationstore.CreateIntegrationTargetBindingInput{
					ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID,
					IntegrationTargetID: target.ID, AgentID: launch.Agent.ID, Source: "api",
					ReceiveAllowed: tc.receive, SendAllowed: !tc.receive,
				})
			require.NoError(t, err)
			if tc.revoke {
				require.NoError(t, f.project.Store.Integrations().RevokeAgentChannelBinding(ctx,
					f.install.ProjectID, launch.Agent.ID, binding.ID))
			}
			body := f.delivery(t, "history-"+tc.name)
			query := openapi.LookupChannelConnectorRecipientsRequest{
				ProviderRef: target.ProviderRef, Receipt: body.Receipt, InputKeys: []string{},
			}
			page := f.post(t, f.path(t, "channels/recipients"), workflowHTTPJSON(t, query),
				f.token, http.StatusOK)
			require.Equal(t, tc.receive, page["has_receive_binding_history"])
			require.NotContains(t, page, "has_binding_history")
			require.Equal(t, false, page["workflow_started"])
			require.Empty(t, page["recipients"])
			require.Equal(t, testPublicID(t, publicid.KindIntegrationTarget, target.ID), page["channel_id"])
			body.Target.ProviderRef = target.ProviderRef
			body.OnlyIfUnbound = new(true)
			status := http.StatusOK
			if tc.receive {
				status = http.StatusConflict
			}
			result := f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, status)
			if tc.receive {
				require.Equal(t, "state_transition_conflict", result["code"])
			} else {
				require.Equal(t, true, result["created_agent"])
				require.NotEqual(t, testPublicID(t, publicid.KindAgent, launch.Agent.ID), result["agent_id"])
			}
		})
	}
}

func TestChannelConnectorBoundInputRecipientJourney(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowHTTPFixture(t)
	target, bindings := createBoundChannelHTTPRecipients(t, f, 2)
	claim := f.delivery(t, "bound-receipt")
	lookupPath, deliverPath := f.path(t, "channels/recipients"), f.path(t, "channels/deliver")
	limit := 1
	query := openapi.LookupChannelConnectorRecipientsRequest{
		ProviderRef: target.ProviderRef, Receipt: claim.Receipt, InputKeys: []string{claim.InputKey}, Limit: &limit,
	}
	lookup := func(body openapi.LookupChannelConnectorRecipientsRequest) map[string]any {
		return f.post(t, lookupPath, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	}
	first := lookup(query)
	require.Equal(t, testPublicID(t, publicid.KindIntegrationTarget, target.ID), first["channel_id"])
	require.Equal(t, true, first["has_receive_binding_history"])
	require.Equal(t, false, first["workflow_started"])
	firstRecipients := testutil.RequireType[[]any](t, first["recipients"])
	require.Len(t, firstRecipients, 1)
	firstRecipient := testutil.RequireType[map[string]any](t, firstRecipients[0])
	require.Empty(t, firstRecipient["input_keys"])
	cursor := channelReceiptString(t, first, "next_cursor")
	query.Cursor = &cursor
	second := lookup(query)
	require.Nil(t, second["next_cursor"])
	secondRecipients := testutil.RequireType[[]any](t, second["recipients"])
	require.Len(t, secondRecipients, 1)
	secondRecipient := testutil.RequireType[map[string]any](t, secondRecipients[0])
	require.ElementsMatch(t, []string{
		testPublicID(t, publicid.KindAgent, bindings[0].AgentID),
		testPublicID(t, publicid.KindAgent, bindings[1].AgentID),
	}, []string{channelReceiptString(t, firstRecipient, "agent_id"), channelReceiptString(t, secondRecipient, "agent_id")})
	for _, change := range []func(*openapi.LookupChannelConnectorRecipientsRequest){
		func(q *openapi.LookupChannelConnectorRecipientsRequest) { q.ProviderRef = "another-address" },
		func(q *openapi.LookupChannelConnectorRecipientsRequest) { q.InputKeys = []string{"another-message"} },
		func(q *openapi.LookupChannelConnectorRecipientsRequest) { q.Receipt.LeaseToken = uuid.New() },
		func(q *openapi.LookupChannelConnectorRecipientsRequest) { q.Receipt.LeaseGeneration++ },
		func(q *openapi.LookupChannelConnectorRecipientsRequest) {
			q.Receipt.ReceiptId = testPublicID(t, publicid.KindIntegrationEventReceipt, uuid.New())
		},
	} {
		changed := query
		change(&changed)
		f.post(t, lookupPath, workflowHTTPJSON(t, changed), f.token, http.StatusBadRequest)
	}
	otherPath := strings.Replace(lookupPath, testPublicID(t, publicid.KindIntegrationInstall, f.install.ID),
		testPublicID(t, publicid.KindIntegrationInstall, f.otherInstall.ID), 1)
	f.post(t, otherPath, workflowHTTPJSON(t, query), f.token, http.StatusBadRequest)
	query.Cursor, query.Limit = nil, nil
	unknown := query
	unknown.ProviderRef = "never-registered"
	missing := lookup(unknown)
	require.NotContains(t, missing, "channel_id")
	require.Equal(t, false, missing["has_receive_binding_history"])
	require.Equal(t, false, missing["workflow_started"])
	require.Empty(t, missing["recipients"])
	require.Nil(t, missing["next_cursor"])

	body := boundChannelHTTPInput(t, claim, bindings[0])
	accepted := f.post(t, deliverPath, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, false, accepted["created_agent"])
	require.Equal(t, true, accepted["created_input"])
	require.Equal(t, testPublicID(t, publicid.KindAgent, bindings[0].AgentID), accepted["agent_id"])
	require.Equal(t, body.BindingId, accepted["binding_id"])
	require.Equal(t, first["channel_id"], accepted["channel_id"])
	blocks := testutil.RequireType[[]any](t, accepted["content_blocks"])
	require.Len(t, blocks, 2)
	attachment := testutil.RequireType[map[string]any](t, blocks[1])
	require.Equal(t, "media_ref", attachment["type"])
	mustPublicHTTPID(t, publicid.KindArtifact, channelReceiptString(t, attachment, "artifact_id"))
	page := lookup(query)
	require.Equal(t, false, page["workflow_started"], "bound admission cannot masquerade as workflow fanout")
	for _, value := range testutil.RequireType[[]any](t, page["recipients"]) {
		recipient := testutil.RequireType[map[string]any](t, value)
		if recipient["binding_id"] == body.BindingId {
			require.Equal(t, []any{body.InputKey}, recipient["input_keys"])
		} else {
			require.Empty(t, recipient["input_keys"])
		}
	}
	secondInput := boundChannelHTTPInput(t, claim, bindings[1])
	steering := openapi.CreateAgentInputDeliveryMode("steering")
	secondInput.DeliveryMode, secondInput.CancelOpenInteractions = &steering, new(true)
	secondAccepted := f.post(t, deliverPath, workflowHTTPJSON(t, secondInput), f.token, http.StatusOK)
	require.Equal(t, false, secondAccepted["created_agent"])
	require.NotEqual(t, accepted["agent_input_id"], secondAccepted["agent_input_id"])
	var deliveryMode string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT delivery_mode FROM agent_inputs WHERE id=$1`,
		mustPublicHTTPID(t, publicid.KindAgentInput, channelReceiptString(t, secondAccepted, "agent_input_id"))).
		Scan(&deliveryMode))
	require.Equal(t, "steering", deliveryMode)
	// Replay retains the first actor, content, artifact and origin after revocation.
	require.NoError(t, f.project.Store.Integrations().RevokeAgentChannelBinding(ctx,
		f.install.ProjectID, bindings[0].AgentID, bindings[0].ID))
	body.Author.Ref = "changed-author"
	body.ContentBlocks = workflowHTTPContent(t, "changed replay", "changed.png")
	body.InputPrecondition = &openapi.ChannelInputPrecondition{InputKey: "never-accepted", Exists: true}
	replayed := f.post(t, deliverPath, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, false, replayed["created_input"])
	for _, key := range []string{"agent_id", "agent_input_id", "channel_id", "binding_id", "content_blocks"} {
		require.Equal(t, accepted[key], replayed[key], key)
	}
	semantic := f.delivery(t, "another-callback")
	body.Receipt = semantic.Receipt
	replayed = f.post(t, deliverPath, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, accepted["agent_input_id"], replayed["agent_input_id"])
	query.Receipt = semantic.Receipt
	page = lookup(query)
	require.Equal(t, true, page["has_receive_binding_history"])
	require.Len(t, testutil.RequireType[[]any](t, page["recipients"]), 1)
	body.InputKey = "changed-receipt-key"
	replayed = f.post(t, deliverPath, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, accepted["agent_input_id"], replayed["agent_input_id"], "receipt replay retains its original input")
	unaccepted := f.delivery(t, "unaccepted-callback")
	body.Receipt, body.InputKey = unaccepted.Receipt, "fresh-revoked"
	body.InputPrecondition = nil
	f.post(t, deliverPath, workflowHTTPJSON(t, body), f.token, http.StatusNotFound)
	body.BindingId = testPublicID(t, publicid.KindIntegrationBinding, bindings[1].ID)
	body.InputPrecondition = &openapi.ChannelInputPrecondition{InputKey: "missing", Exists: true}
	conflict := f.post(t, deliverPath, workflowHTTPJSON(t, body), f.token, http.StatusConflict)
	require.Equal(t, "state_transition_conflict", conflict["code"])
	var agents, workflows, inputs, artifacts int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT
(SELECT count(*) FROM agents), (SELECT count(*) FROM integration_workflows),
(SELECT count(*) FROM agent_inputs WHERE input_idempotency_key=$1), (SELECT count(*) FROM artifacts)`,
		claim.InputKey).Scan(&agents, &workflows, &inputs, &artifacts))
	require.Equal(t, 2, agents)
	require.Zero(t, workflows)
	require.Equal(t, 2, inputs)
	require.Equal(t, 2, artifacts)
	_, _, retained := f.blobs.snapshot()
	require.Equal(t, 2, retained, "replay and denied inputs compensate only their unused uploads")
}

func TestChannelConnectorBoundInputRejectsUntrustedScopeAndInvalidPayload(t *testing.T) {
	t.Parallel()
	f := newChannelWorkflowHTTPFixture(t)
	target, bindings := createBoundChannelHTTPRecipients(t, f, 1)
	claim := f.delivery(t, "bound-validation")
	input := boundChannelHTTPInput(t, claim, bindings[0])
	query := openapi.LookupChannelConnectorRecipientsRequest{
		ProviderRef: target.ProviderRef, Receipt: claim.Receipt, InputKeys: []string{},
	}
	for _, route := range []struct {
		path string
		body any
	}{
		{f.path(t, "channels/recipients"), query}, {f.path(t, "channels/deliver"), input},
	} {
		body := workflowHTTPJSON(t, route.body)
		f.post(t, route.path, body, "", http.StatusUnauthorized)
		f.post(t, route.path, body, f.project.AdminToken, http.StatusForbidden)
		f.post(t, route.path, body, f.otherToken, http.StatusNotFound)
		wrongApp := strings.Replace(route.path, testPublicID(t, publicid.KindIntegrationApp, f.app.ID),
			testPublicID(t, publicid.KindIntegrationApp, f.otherApp.ID), 1)
		f.post(t, wrongApp, body, f.token, http.StatusNotFound)
		for _, field := range []string{"agent_id", "project_id", "channel_id", "grants"} {
			f.post(t, route.path, strings.TrimSuffix(body, "}")+`,"`+field+`":"caller-selected"}`,
				f.token, http.StatusBadRequest)
		}
	}
	for _, change := range []func(*openapi.DeliverChannelConnectorInputRequest){
		func(b *openapi.DeliverChannelConnectorInputRequest) { b.Author.Ref = " \t" },
		func(b *openapi.DeliverChannelConnectorInputRequest) { b.Author.Ref = strings.Repeat("é", 257) },
		func(b *openapi.DeliverChannelConnectorInputRequest) { b.Author.DisplayName = "bad\x00name" },
		func(b *openapi.DeliverChannelConnectorInputRequest) { b.Metadata = json.RawMessage(`{"x":"\u0000"}`) },
		func(b *openapi.DeliverChannelConnectorInputRequest) { b.CancelOpenInteractions = new(true) },
		func(b *openapi.DeliverChannelConnectorInputRequest) { b.InputKey = strings.Repeat("é", 257) },
	} {
		changed := input
		change(&changed)
		f.post(t, f.path(t, "channels/deliver"), workflowHTTPJSON(t, changed), f.token, http.StatusBadRequest)
	}
	for _, value := range []string{" ", "bad\x00address", strings.Repeat("é", 257)} {
		changed := query
		changed.ProviderRef = value
		f.post(t, f.path(t, "channels/recipients"), workflowHTTPJSON(t, changed), f.token, http.StatusBadRequest)
	}
	for _, limit := range []int{0, 101} {
		changed := query
		changed.Limit = &limit
		f.post(t, f.path(t, "channels/recipients"), workflowHTTPJSON(t, changed), f.token, http.StatusBadRequest)
	}
	foreignInstallPath := strings.Replace(f.path(t, "channels/deliver"),
		testPublicID(t, publicid.KindIntegrationInstall, f.install.ID),
		testPublicID(t, publicid.KindIntegrationInstall, f.otherInstall.ID), 1)
	f.post(t, foreignInstallPath, workflowHTTPJSON(t, input), f.token, http.StatusNotFound)
	unknownBinding := input
	unknownBinding.BindingId = testPublicID(t, publicid.KindIntegrationBinding, uuid.New())
	f.post(t, f.path(t, "channels/deliver"), workflowHTTPJSON(t, unknownBinding), f.token, http.StatusNotFound)
	input.Receipt.LeaseGeneration++
	query.Receipt.LeaseGeneration++
	f.post(t, f.path(t, "channels/deliver"), workflowHTTPJSON(t, input), f.token, http.StatusConflict)
	f.post(t, f.path(t, "channels/recipients"), workflowHTTPJSON(t, query), f.token, http.StatusConflict)
	_, _, retained := f.blobs.snapshot()
	require.Zero(t, retained)
}

func TestChannelConnectorWorkflowOnlyIfUnboundUsesReceiptScopedFanout(t *testing.T) {
	t.Parallel()
	f := newChannelWorkflowHTTPFixture(t)
	target, _ := createBoundChannelHTTPRecipients(t, f, 1)
	body := f.delivery(t, "guarded-workflow")
	body.Target.ProviderRef = target.ProviderRef
	body.OnlyIfUnbound = new(true)
	conflict := f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, http.StatusConflict)
	require.Equal(t, "state_transition_conflict", conflict["code"])
	// A different address can begin this receipt's explicit route fanout.
	body.Target.ProviderRef = "new-conversation"
	first := f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, http.StatusOK)
	routes, err := f.project.Store.Integrations().ListActiveIntegrationRoutes(
		t.Context(), f.install.ProjectID, f.install.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	secondRoute, err := f.project.Store.Integrations().CreateIntegrationRoute(t.Context(),
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID, AgentProfileID: routes[0].AgentProfileID,
			DeploymentKey: "second", BehaviorKey: "conversation",
		})
	require.NoError(t, err)
	query := openapi.LookupChannelConnectorRecipientsRequest{
		ProviderRef: body.Target.ProviderRef, Receipt: body.Receipt, InputKeys: []string{body.InputKey},
	}
	page := f.post(t, f.path(t, "channels/recipients"), workflowHTTPJSON(t, query), f.token, http.StatusOK)
	require.Equal(t, true, page["has_receive_binding_history"])
	require.Equal(t, true, page["workflow_started"])
	body.RouteId = testPublicID(t, publicid.KindIntegrationRoute, secondRoute.ID)
	second := f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.NotEqual(t, first["agent_id"], second["agent_id"])
	require.Equal(t, first["channel_id"], second["channel_id"])
	later := f.delivery(t, "later-receipt")
	query.Receipt = later.Receipt
	page = f.post(t, f.path(t, "channels/recipients"), workflowHTTPJSON(t, query), f.token, http.StatusOK)
	require.Equal(t, false, page["workflow_started"])
	require.Equal(t, true, page["has_receive_binding_history"])
	query.Receipt, query.ProviderRef = body.Receipt, target.ProviderRef
	page = f.post(t, f.path(t, "channels/recipients"), workflowHTTPJSON(t, query), f.token, http.StatusOK)
	require.Equal(t, false, page["workflow_started"], "workflow outcome for another address cannot bypass the guard")
}

//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestAppHandlerTargetsExistWithoutInputOrListener(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	resource := f.resource()
	resource.Listener, resource.Follow = nil, nil
	resource.Scope.Slack.ThreadTS = "111.222"
	resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
	definition := f.definition(t, "Fixed handler only", map[string]agentconfig.AppResourceCompiled{"handler": resource})
	config, err := f.store.Execution().CreateAgentConfig(f.ctx, definition)
	require.NoError(t, err)
	_, err = f.store.Execution().
		CreateAgentProfile(
			f.ctx,
			executionstore.CreateAgentProfileInput{
				ProjectID:       testProjectID,
				Name:            "Fixed handler",
				CurrentConfigID: config.ID,
			},
		)
	require.NoError(t, err)
	var count int
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_targets`).Scan(&count))
	require.Zero(t, count, "profile saves materialize neither targets nor listeners")
	launched, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(config.ID, "fixed-handler"))
	require.NoError(t, err)
	destinations, err := f.store.Execution().ListInteractionDestinations(f.ctx, testProjectID, launched.Agent.ID)
	require.NoError(t, err)
	require.Len(t, destinations.Destinations, 1)
	require.Equal(t, "handler", destinations.Destinations[0].Destination.ResourceKey)
	require.Equal(t, "C123:111.222", destinations.Destinations[0].Destination.Address.Ref)
	require.Equal(t, executionstore.InteractionSelection{}, destinations.Current)
	require.Empty(t, f.listeners(t, launched.Agent.ID))
	require.NoError(
		t,
		f.store.pool.QueryRow(
			f.ctx,
			`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`,
			launched.Agent.ID,
		).
			Scan(
				&count,
			),
	)
	require.Zero(t, count)
	target, err := f.store.Integrations().
		GetIntegrationTarget(f.ctx, testProjectID, destinations.Destinations[0].Destination.IntegrationTargetID)
	require.NoError(t, err)
	var role string
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT routing_role FROM integration_targets WHERE id=$1`, target.ID).Scan(&role),
	)
	require.Equal(t, string(integrationstore.TargetAttribution), role)
	require.Equal(t, uuid.Nil, target.AppID)
	f.disable(t)
	replayed, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(config.ID, "fixed-handler"))
	require.NoError(t, err)
	require.False(t, replayed.Created)
}

func TestAppHandlerActivationClearsRevokedSelectionAndPreservesCapture(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	prompt := f.question(t)
	delete(f.resources, "chat")
	f.change(t, f.resources)
	selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
	require.JSONEq(t, string(prompt.Destination), string(f.read(t, prompt.ID).Destination))
	_, err = f.store.Integrations().GetIntegrationTarget(f.ctx, testProjectID, f.a.ID)
	require.NoError(t, err, "revoking a handler preserves attribution/history")
	destinations, err := f.store.Execution().ListInteractionDestinations(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	for _, destination := range destinations.Destinations {
		require.Equal(t, "other", destination.Destination.ResourceKey)
	}
}

func TestAppHandlerInboxLaunchReusesSelectedOriginTarget(t *testing.T) {
	t.Parallel()
	f := newInboxLaunchFixture(t, false, time.Minute, "a")
	slot := f.slots["a"]
	slot.AgentID = uuid.Must(uuid.NewV7())
	resource := f.resource()
	resource.Listener, resource.Follow = nil, nil
	resource.Scope.Slack.ThreadTS = "123.456"
	resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
	definition := f.definition(
		t,
		"Selected exact handler",
		map[string]agentconfig.AppResourceCompiled{"handler": resource},
	)
	slot.Launch.DerivedConfig, slot.Launch.IdempotencyKey = &definition, "selected-exact-handler"
	// A distinct app retains independent profile selection in the same address.
	setup := f.app.Settings
	app, err := f.store.Integrations().
		CreateProjectApp(
			f.ctx,
			integrationstore.SaveProjectAppInput{
				OrgID:        testOrgID,
				ProjectID:    testProjectID,
				Name:         "other-handler-launcher",
				DefinitionID: f.app.DefinitionID,
				Settings:     setup,
				Enabled:      true,
			},
		)
	require.NoError(t, err)
	slot.Selection.AppID = app.ID
	_, _, err = f.store.Integrations().
		AcceptIntegrationReceipt(
			f.ctx,
			integrationstore.VerifiedIntegrationReceipt{
				ProjectID:    testProjectID,
				ConnectionID: f.connection.ID,
				ReceiptKey:   "handler-launch",
				Payload:      []byte(`{}`),
			},
		)
	require.NoError(t, err)
	receipt, found, err := f.store.Integrations().
		ClaimIntegrationInbox(
			f.ctx,
			integrationstore.ClaimIntegrationInboxInput{
				ProjectID:     testProjectID,
				ConnectionID:  f.connection.ID,
				LeaseDuration: time.Minute,
			},
		)
	require.NoError(t, err)
	require.True(t, found)
	plan, err := json.Marshal(map[string]executionstore.InboxLaunchSlot{"a": slot})
	require.NoError(t, err)
	require.NoError(
		t,
		f.store.Integrations().
			WithIntegrationInboxLease(
				f.ctx,
				receipt.Lease(),
				func(w *integrationstore.IntegrationInboxLeaseTx) error { return w.FreezePlan(f.ctx, plan) },
			),
	)
	result, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "a")
	require.NoError(t, err)
	require.Equal(t, integrationstore.TargetSelected, result.IntegrationTarget.RoutingRole)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_targets WHERE agent_id=$1`, slot.AgentID).
			Scan(&count),
	)
	require.Equal(t, 1, count)
	selected, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, slot.AgentID)
	require.NoError(t, err)
	require.Equal(t, "handler", selected.ResourceKey)
	require.Equal(t, result.IntegrationTarget.ID, selected.IntegrationTargetID)
}

func TestAppHandlerConversationGatePrecedesConfigAgentLock(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "handler-gates"))
	require.NoError(t, err)
	resource := f.resource()
	resource.Listener, resource.Follow = nil, nil
	resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
	input := f.changeInput(
		t,
		agent.Agent.ID,
		"Add fixed handler",
		map[string]agentconfig.AppResourceCompiled{"handler": resource},
		"handler-gates",
	)
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(
		t,
		integrationstore.LockConversationTx(
			f.ctx,
			blocker,
			testProjectID,
			f.connection.ID,
			integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
		return f.store.Execution().ChangeAgentConfig(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAppConversation", 1)
	_, err = dbsqlc.New(blocker).
		LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agent.Agent.ID})
	require.NoError(t, err)
	require.NoError(t, blocker.Commit(f.ctx))
	integrationdb.AwaitSuccess(t, done, "fixed handler activation")
}

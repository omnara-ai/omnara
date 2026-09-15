//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestChannelWorkflowSelectionDoesNotReplaceExistingRecipient(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "workflow-recipient-race")
	input := f.event(t, ctx, "incoming")
	input.OnlyIfUnbound = true
	lookupInput := executionstore.LookupChannelRecipientsInput{
		ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ProviderRef: input.Target.ProviderRef, Receipt: input.Receipt, Limit: 1, Capabilities: f.Identity.Capabilities,
	}
	before, err := f.Store.Execution().LookupChannelRecipients(ctx, lookupInput)
	require.NoError(t, err)
	require.False(t, before.HasReceiveBindingHistory)
	// The provider chose to start a workflow, but another operation establishes
	// the conversation while it prepares content outside the transaction.
	targetInput := input.Target
	targetInput.ProjectID, targetInput.IntegrationInstallID = f.Identity.ProjectID, f.Identity.IntegrationInstallID
	target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.NoError(t, err)
	agentID := mustCreateAgent(t, ctx, f.Store)
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			IntegrationTargetID: target.ID, AgentID: agentID, ReceiveAllowed: true, Source: "sender",
		})
	require.NoError(t, err)
	_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.ErrorIs(t, err, executionstore.ErrChannelRecipientsChanged)
	var workflows, provisionalAgents int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_workflows WHERE project_id=$1 AND integration_install_id=$2`,
		f.Identity.ProjectID, f.Identity.IntegrationInstallID).Scan(&workflows))
	require.Zero(t, workflows)
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1 AND id=$2`,
		f.Identity.ProjectID, input.Prepared.AgentID()).Scan(&provisionalAgents))
	require.Zero(t, provisionalAgents, "failed initial selection rolls back the provisional launch")
	after, err := f.Store.Execution().LookupChannelRecipients(ctx, lookupInput)
	require.NoError(t, err)
	require.True(t, after.HasReceiveBindingHistory)
	require.False(t, after.WorkflowStarted)
	require.Len(t, after.Recipients, 1)
	require.Equal(t, binding.ID, after.Recipients[0].BindingID)
	// Revocation must not turn the established thread into a launch trigger.
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, f.Identity.ProjectID, binding.ID))
	after, err = f.Store.Execution().LookupChannelRecipients(ctx, lookupInput)
	require.NoError(t, err)
	require.True(t, after.HasReceiveBindingHistory)
	require.Empty(t, after.Recipients)
	_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.ErrorIs(t, err, executionstore.ErrChannelRecipientsChanged)
}

func TestChannelWorkflowSelectionReobservesReceiveHistoryAfterTargetLockWait(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "workflow-target-wait")
	input := f.event(t, ctx, "incoming")
	input.OnlyIfUnbound = true
	targetInput := input.Target
	targetInput.ProjectID, targetInput.IntegrationInstallID = f.Identity.ProjectID, f.Identity.IntegrationInstallID
	target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.NoError(t, err)
	agentID := mustCreateAgent(t, ctx, f.Store)

	bindingTx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = bindingTx.Rollback(ctx) }()
	var bindingPID int32
	require.NoError(t, bindingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&bindingPID))
	binding, err := f.Store.Integrations().CreateIntegrationTargetBindingTx(ctx, bindingTx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			IntegrationTargetID: target.ID, AgentID: agentID, ReceiveAllowed: true, Source: "api",
		})
	require.NoError(t, err)
	lookupInput := executionstore.LookupChannelRecipientsInput{
		ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ProviderRef: target.ProviderRef, Receipt: input.Receipt, Limit: 1, Capabilities: f.Identity.Capabilities,
	}
	before, err := f.Store.Execution().LookupChannelRecipients(ctx, lookupInput)
	require.NoError(t, err)
	require.False(t, before.HasReceiveBindingHistory, "the pending binding is invisible to another transaction")
	require.Empty(t, before.Recipients)

	finished := make(chan error, 1)
	go func() {
		_, deliveryErr := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
		finished <- deliveryErr
	}()
	// Admission has already inserted its provisional agent/workflow and started
	// the target-lock statement while the receive grant remains uncommitted.
	// Reading history in that same statement would retain the pre-wait snapshot.
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool,
		"-- name: LockIntegrationTargetForBinding ", bindingPID)
	require.NoError(t, bindingTx.Commit(ctx))
	require.ErrorIs(t, integrationdb.Await(t, finished, "workflow after receive binding commit"),
		executionstore.ErrChannelRecipientsChanged)

	var agents, inputs, workflows, outcomes int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE project_id=$1 AND id=$2`,
		f.Identity.ProjectID, input.Prepared.AgentID()).Scan(&agents))
	require.Zero(t, agents, "the provisional launch rolls back after the target-lock wait")
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND agent_id=$2`,
		f.Identity.ProjectID, input.Prepared.AgentID()).Scan(&inputs))
	require.Zero(t, inputs)
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_workflows WHERE project_id=$1 AND integration_install_id=$2`,
		f.Identity.ProjectID, f.Identity.IntegrationInstallID).Scan(&workflows))
	require.Zero(t, workflows)
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_event_outcomes WHERE project_id=$1 AND receipt_id=$2`,
		f.Identity.ProjectID, input.Receipt.ReceiptID).Scan(&outcomes))
	require.Zero(t, outcomes)
	after, err := f.Store.Execution().LookupChannelRecipients(ctx, lookupInput)
	require.NoError(t, err)
	require.True(t, after.HasReceiveBindingHistory)
	require.False(t, after.WorkflowStarted)
	require.Len(t, after.Recipients, 1)
	require.Equal(t, binding.ID, after.Recipients[0].BindingID)
}

func TestChannelWorkflowSelectionResumesPartialFanoutForSameReceiptAndChannel(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "workflow-partial-fanout")
	first := f.event(t, ctx, "first-event")
	first.OnlyIfUnbound = true
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
	require.NoError(t, err)
	lookupInput := executionstore.LookupChannelRecipientsInput{
		ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ProviderRef: first.Target.ProviderRef, Receipt: first.Receipt, Limit: 1, Capabilities: f.Identity.Capabilities,
	}
	lookup, err := f.Store.Execution().LookupChannelRecipients(ctx, lookupInput)
	require.NoError(t, err)
	require.True(t, lookup.HasReceiveBindingHistory)
	require.True(t, lookup.WorkflowStarted, "accepted route A must not hide missing route B on retry")
	agent, err := f.Store.Execution().GetAgentInProject(ctx, f.Identity.ProjectID, accepted.AgentInput.AgentID)
	require.NoError(t, err)
	route, err := f.Store.Integrations().CreateIntegrationRoute(ctx, integrationstore.CreateIntegrationRouteInput{
		ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		AgentProfileID: agent.AgentProfileID, DeploymentKey: "second", BehaviorKey: "conversation",
		State: integrationstore.IntegrationRouteStateActive,
	})
	require.NoError(t, err)
	identity := f.Identity
	identity.IntegrationRouteID = route.ID
	second := first
	second.Prepared, err = f.Store.Execution().PrepareChannelWorkflow(ctx, identity)
	require.NoError(t, err)
	another, err := f.Store.Execution().DeliverChannelWorkflow(ctx, second)
	require.NoError(t, err)
	require.NotEqual(t, accepted.AgentInput.AgentID, another.AgentInput.AgentID)
	require.Equal(t, accepted.ChannelID, another.ChannelID)
	replayed, err := f.Store.Execution().DeliverChannelWorkflow(ctx, second)
	require.NoError(t, err)
	require.Equal(t, another.AgentInput.ID, replayed.AgentInput.ID)
	require.False(t, replayed.CreatedAgent)
	require.False(t, replayed.CreatedInput)
	// The same receipt's accepted workflow is not a grant for another address.
	lookupInput.ProviderRef = "other-thread"
	lookup, err = f.Store.Execution().LookupChannelRecipients(ctx, lookupInput)
	require.NoError(t, err)
	require.False(t, lookup.WorkflowStarted)
	require.False(t, lookup.HasReceiveBindingHistory)
}

func TestChannelWorkflowSelectionDoesNotTreatSendingAsReceiving(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "workflow-send-only")
	input := f.event(t, ctx, "mention")
	input.OnlyIfUnbound = true
	targetInput := input.Target
	targetInput.ProjectID, targetInput.IntegrationInstallID = f.Identity.ProjectID, f.Identity.IntegrationInstallID
	target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.NoError(t, err)
	senderID := mustCreateAgent(t, ctx, f.Store)
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			IntegrationTargetID: target.ID, AgentID: senderID, SendAllowed: true, Source: "announcements",
		})
	require.NoError(t, err)
	lookup, err := f.Store.Execution().LookupChannelRecipients(ctx, executionstore.LookupChannelRecipientsInput{
		ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ProviderRef: target.ProviderRef, Receipt: input.Receipt, Limit: 10, Capabilities: f.Identity.Capabilities,
	})
	require.NoError(t, err)
	require.Equal(t, target.ID, lookup.ChannelID)
	require.False(t, lookup.HasReceiveBindingHistory)
	require.Empty(t, lookup.Recipients, "sending alone never subscribes the sender to input")
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.NoError(t, err)
	require.True(t, accepted.CreatedAgent, "the configured mention workflow may start an agent")
	require.NotEqual(t, senderID, accepted.AgentInput.AgentID)
	require.Equal(t, target.ID, accepted.ChannelID)
	unchanged, err := f.Store.Integrations().GetIntegrationTargetBinding(ctx, f.Identity.ProjectID, binding.ID)
	require.NoError(t, err)
	require.True(t, unchanged.SendAllowed)
	require.False(t, unchanged.ReceiveAllowed, "mention behavior must not widen the sender's grants")
	var senderInputs int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND agent_id=$2 AND integration_target_id=$3`,
		f.Identity.ProjectID, senderID, target.ID).Scan(&senderInputs))
	require.Zero(t, senderInputs)
}

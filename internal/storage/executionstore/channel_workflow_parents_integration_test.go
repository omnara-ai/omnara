//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestChannelWorkflowParentRegisteredBeforeChildAndCommittedAtomically(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "workflow-inline-parent")
	input := f.event(t, ctx, "incoming")
	parentInput := channelWorkflowParentInput(t, f)
	// Descriptor scope never confers authority; both channels belong to the
	// prepared installation, even if a caller supplies unrelated IDs.
	parentInput.ProjectID, parentInput.IntegrationInstallID = uuid.New(), uuid.New()
	input.Target.ProjectID, input.Target.IntegrationInstallID = uuid.New(), uuid.New()
	input.ParentTarget = &parentInput
	originalParent := parentInput

	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	var blockerPID int32
	require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
	_, err := blocker.Exec(ctx,
		`SELECT id FROM integration_channel_definitions WHERE id=$1 FOR UPDATE`, f.Definition.ID)
	require.NoError(t, err)
	delivery := integrationdb.RunAsync(func() (executionstore.ChannelInputResult, error) {
		return f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool,
		"-- name: LockChannelDefinition ", blockerPID)
	// The parent has been inserted, but no part of admission is visible before
	// the child can validate its definition and the whole transaction commits.
	requireChannelParentAdmissionRolledBack(t, f, input)
	competingParent := parentInput
	competingParent.ProjectID, competingParent.IntegrationInstallID = f.Identity.ProjectID, f.Identity.IntegrationInstallID
	registration := integrationdb.RunAsync(func() (integrationstore.IntegrationTargetRecord, error) {
		return f.Store.Integrations().CreateIntegrationTarget(ctx, competingParent)
	})
	// This unique-index wait proves the parent was registered before the child
	// definition lock, rather than being deferred until after child creation.
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "InsertIntegrationTarget", 1)
	require.NoError(t, blocker.Commit(ctx))
	accepted := integrationdb.AwaitSuccess(t, delivery, "workflow parent and child commit")
	parent := integrationdb.AwaitSuccess(t, registration, "canonical concurrent parent registration")
	require.False(t, parent.Created)
	require.Equal(t, originalParent, parentInput, "admission must not mutate the caller's parent descriptor")
	require.True(t, accepted.CreatedAgent)
	require.True(t, accepted.CreatedInput)
	child, err := f.Store.Integrations().GetIntegrationTarget(ctx, f.Identity.ProjectID, accepted.ChannelID)
	require.NoError(t, err)
	require.Equal(t, parent.ID, child.ParentChannelID)
	require.Equal(t, uuid.Nil, parent.ParentChannelID)
	for _, target := range []integrationstore.IntegrationTargetRecord{parent, child} {
		require.Equal(t, f.Identity.ProjectID, target.ProjectID)
		require.Equal(t, f.Identity.IntegrationInstallID, target.IntegrationInstallID)
	}
	require.Equal(t, child.ID, accepted.AgentInput.IntegrationTargetID)
	require.Equal(t, accepted.BindingID, accepted.AgentInput.IntegrationTargetBindingID)
	var targets, parentBindings, childBindings int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM integration_targets WHERE integration_install_id=$1),
       (SELECT count(*) FROM integration_target_bindings WHERE integration_target_id=$2),
       (SELECT count(*) FROM integration_target_bindings WHERE integration_target_id=$3)`,
		f.Identity.IntegrationInstallID, parent.ID, child.ID).Scan(&targets, &parentBindings, &childBindings))
	require.Equal(t, 2, targets)
	require.Zero(t, parentBindings, "registering a parent grants no receive/read/send authority")
	require.Equal(t, 1, childBindings)
}

func TestChannelWorkflowParentInvalidAdmissionRollsBack(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"invalid_child", "invalid_content", "both_parent_forms", "nested_parent", "same_address",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelWorkflowFixture(t, ctx, "inline-parent-"+name)
			input := f.event(t, ctx, "incoming")
			parent := channelWorkflowParentInput(t, f)
			input.ParentTarget = &parent
			switch name {
			case "invalid_child":
				input.Target.ProviderRef = " "
			case "invalid_content":
				input.Content.Blocks = json.RawMessage(`[{"type":"unsupported"}]`)
			case "both_parent_forms":
				input.Target.ParentChannelID = uuid.New()
			case "nested_parent":
				parent.ParentChannelID = uuid.New()
			case "same_address":
				parent.ProviderRef = " \t" + input.Target.ProviderRef + "\n"
			}
			_, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
			if name == "invalid_content" {
				require.Error(t, err)
			} else {
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			}
			requireChannelParentAdmissionRolledBack(t, f, input)
			// The same live receipt and provisional identity can be corrected;
			// failed registration leaves neither a parent nor a replay outcome.
			parent.ParentChannelID, input.Target.ParentChannelID = uuid.Nil, uuid.Nil
			parent.ProviderRef, input.Target.ProviderRef = "parent", "thread-1"
			input.Content.Blocks = json.RawMessage(`[{"type":"text","text":"corrected"}]`)
			accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
			require.NoError(t, err)
			require.True(t, accepted.CreatedInput)
			require.True(t, accepted.CreatedAgent)
		})
	}
}

func TestChannelWorkflowParentRejectsForeignDefinition(t *testing.T) {
	t.Parallel()
	for _, otherProject := range []bool{false, true} {
		name := "other_installation"
		if otherProject {
			name = "other_project"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelWorkflowFixture(t, ctx, "parent-foreign-definition")
			install, err := f.Store.Integrations().GetIntegrationInstallByID(ctx, f.Identity.IntegrationInstallID)
			require.NoError(t, err)
			projectID := f.Identity.ProjectID
			if otherProject {
				project, err := f.Store.Identity().CreateProjectForPrincipal(ctx,
					identitystore.CreateProjectForPrincipalInput{
						OrgID: install.OrgID, Creator: install.InstalledBy,
						Name: "Other parent project", IdempotencyKey: "other-parent-project",
					})
				require.NoError(t, err)
				projectID = project.ID
			}
			foreignInstall, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx,
				integrationstore.CreateExternalIntegrationInstallInput{
					OrgID: install.OrgID, ProjectID: projectID, InstalledBy: install.InstalledBy,
				})
			require.NoError(t, err)
			definition, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx,
				integrationstore.PublishChannelDefinitionInput{
					ProjectID: projectID, IntegrationInstallID: foreignInstall.ID,
					ImplementationKey: "parent", Kind: integrationstore.ChannelKindExternal,
					SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
					Capabilities:     integrationstore.ChannelCapabilities{Send: true, Text: true},
				})
			require.NoError(t, err)
			input := f.event(t, ctx, "incoming")
			input.ParentTarget = &integrationstore.CreateIntegrationTargetInput{
				ProjectID: projectID, IntegrationInstallID: foreignInstall.ID,
				ChannelDefinitionID: definition.ID, ProviderRef: "parent", ProviderRefKind: "channel",
			}
			_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrNotFound, "descriptor scope cannot authorize a foreign definition")
			requireChannelParentAdmissionRolledBack(t, f, input)
			var foreignTargets int
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM integration_targets WHERE integration_install_id=$1`, foreignInstall.ID).
				Scan(&foreignTargets))
			require.Zero(t, foreignTargets)
		})
	}
}

func TestChannelWorkflowParentReuseReplayAndLiveCompatibility(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "parent-replay")
	parentInput := channelWorkflowParentInput(t, f)
	parent, err := f.Store.Integrations().CreateIntegrationTarget(ctx, parentInput)
	require.NoError(t, err)
	input := f.event(t, ctx, "first")
	input.ParentTarget = &parentInput
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.NoError(t, err)
	child, err := f.Store.Integrations().GetIntegrationTarget(ctx, f.Identity.ProjectID, accepted.ChannelID)
	require.NoError(t, err)
	require.Equal(t, parent.ID, child.ParentChannelID, "an existing compatible parent is reused")

	// Both immutable receipt replay and semantic input replay precede current
	// projection validation. Neither may register or refresh a different parent.
	for _, sameReceipt := range []bool{true, false} {
		replay := input
		if !sameReceipt {
			replay = f.event(t, ctx, "redelivery")
			replay.InputKey = input.InputKey
		}
		changedParent := parentInput
		changedParent.ProviderRef, changedParent.DisplayName = "changed-parent", "changed"
		changedParent.ChannelDefinitionID, changedParent.ParentChannelID = uuid.New(), uuid.New()
		replay.ParentTarget = &changedParent
		replay.Target.ParentChannelID = uuid.New()
		replay.ProviderUserID = ""
		result, err := f.Store.Execution().DeliverChannelWorkflow(ctx, replay)
		require.NoError(t, err)
		require.False(t, result.CreatedInput)
		require.Equal(t, accepted.AgentInput.ID, result.AgentInput.ID)
		require.Equal(t, accepted.ChannelID, result.ChannelID)
		require.Equal(t, accepted.BindingID, result.BindingID)
	}
	for _, name := range []string{"different_parent", "different_parent_definition"} {
		fresh := f.event(t, ctx, name)
		incompatible := parentInput
		if name == "different_parent" {
			incompatible.ProviderRef = "changed-parent"
		} else {
			incompatible.ChannelDefinitionID = f.Definition.ID
		}
		fresh.ParentTarget = &incompatible
		_, err := f.Store.Execution().DeliverChannelWorkflow(ctx, fresh)
		require.ErrorIs(t, err, storeerr.ErrConflict, "new input must validate immutable parentage and definition")
		var outcomes int
		require.NoError(t, f.Store.pool.QueryRow(ctx,
			`SELECT count(*) FROM integration_event_outcomes WHERE receipt_id=$1`, fresh.Receipt.ReceiptID).Scan(&outcomes))
		require.Zero(t, outcomes)
	}
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx, f.Identity.ProjectID, f.Identity.IntegrationInstallID, "changed-parent")
	require.ErrorIs(t, err, storeerr.ErrNotFound, "failed child admission rolls back its newly registered parent")
	storedParent, err := f.Store.Integrations().GetIntegrationTarget(ctx, f.Identity.ProjectID, parent.ID)
	require.NoError(t, err)
	require.Equal(t, parent.ChannelDefinitionID, storedParent.ChannelDefinitionID)
	require.Equal(t, parent.DisplayName, storedParent.DisplayName)
	var inputs, parentBindings int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'),
       (SELECT count(*) FROM integration_target_bindings WHERE integration_target_id=$2)`,
		accepted.AgentInput.AgentID, parent.ID).Scan(&inputs, &parentBindings))
	require.Equal(t, 1, inputs)
	require.Zero(t, parentBindings)
}

func TestChannelWorkflowParentExistingIDAllowsDeeperHierarchy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowFixture(t, ctx, "parent-existing-hierarchy")
	parentInput := channelWorkflowParentInput(t, f)
	root, err := f.Store.Integrations().CreateIntegrationTarget(ctx, parentInput)
	require.NoError(t, err)
	parentInput.ProviderRef, parentInput.ParentChannelID = "nested-parent", root.ID
	parent, err := f.Store.Integrations().CreateIntegrationTarget(ctx, parentInput)
	require.NoError(t, err)
	input := f.event(t, ctx, "incoming")
	input.Target.ParentChannelID = parent.ID
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.NoError(t, err)
	child, err := f.Store.Integrations().GetIntegrationTarget(ctx, f.Identity.ProjectID, accepted.ChannelID)
	require.NoError(t, err)
	require.Equal(t, parent.ID, child.ParentChannelID)
	require.Equal(t, root.ID, parent.ParentChannelID)
}

func channelWorkflowParentInput(t *testing.T, f channelWorkflowFixture) integrationstore.CreateIntegrationTargetInput {
	t.Helper()
	definition, err := f.Store.Integrations().PublishConnectorChannelDefinition(t.Context(),
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			ImplementationKey: "parent", Kind: integrationstore.ChannelKindDiscordChannel,
			SendParamsSchema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities:          integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: f.Identity.Capabilities,
		})
	require.NoError(t, err)
	return integrationstore.CreateIntegrationTargetInput{
		ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ChannelDefinitionID: definition.ID, ProviderRef: "parent", ProviderRefKind: "channel", DisplayName: "Parent",
	}
}

func requireChannelParentAdmissionRolledBack(
	t *testing.T, f channelWorkflowFixture, input executionstore.DeliverChannelWorkflowInput,
) {
	t.Helper()
	var agents, inputs, workflows, targets, bindings, outcomes int
	require.NoError(t, f.Store.pool.QueryRow(t.Context(), `
SELECT (SELECT count(*) FROM agents WHERE id=$1),
       (SELECT count(*) FROM agent_inputs WHERE agent_id=$1),
       (SELECT count(*) FROM integration_workflows WHERE agent_id=$1),
       (SELECT count(*) FROM integration_targets WHERE integration_install_id=$2),
       (SELECT count(*) FROM integration_target_bindings WHERE agent_id=$1),
       (SELECT count(*) FROM integration_event_outcomes WHERE receipt_id=$3)`,
		input.Prepared.AgentID(), f.Identity.IntegrationInstallID, input.Receipt.ReceiptID).
		Scan(&agents, &inputs, &workflows, &targets, &bindings, &outcomes))
	require.Equal(t, []int{0, 0, 0, 0, 0, 0}, []int{agents, inputs, workflows, targets, bindings, outcomes},
		"no provisional agent, input, workflow, parent/child, binding, or outcome may escape the transaction")
}

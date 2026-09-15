//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestChannelGrantsAreIndependentWithoutRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "independent-channel-grants")
	definitionID := createChannelTestDefinition(t, ctx, store, install)
	target, err := store.Integrations().CreateIntegrationTarget(
		ctx, integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, ChannelDefinitionID: definitionID, IntegrationInstallID: install.ID,
			ProviderRef: "conversation", ProviderRefKind: "thread",
		})
	require.NoError(t, err)
	input := integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
		IntegrationTargetID: target.ID, Source: "application", ReadAllowed: true,
	}
	reader, err := store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.True(t, reader.ReadAllowed)
	readBinding, err := store.Integrations().GetActiveReadBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.NoError(t, err)
	require.Equal(t, reader.ID, readBinding.ID)
	_, err = store.Integrations().GetActiveSendBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "reading grants no sending permission")
	_, err = store.Integrations().GetActiveReceiveBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "reading grants no incoming subscription")
	channels, err := store.Integrations().ListAgentChannelTargets(
		ctx, testProjectID, agent.ID, integrationstore.ListAgentChannelTargetsInput{Limit: 10})
	require.NoError(t, err)
	require.Len(t, channels.Targets, 1)
	require.True(t, channels.Targets[0].ReadAllowed)

	input.ReadAllowed, input.ReceiveAllowed = false, true
	receiver, err := store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err, "an explicit incoming grant does not require a built-in route")
	require.NotEqual(t, reader.ID, receiver.ID, "permission changes replace immutable binding provenance")
	receiveBinding, err := store.Integrations().GetActiveReceiveBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.NoError(t, err)
	require.Equal(t, receiver.ID, receiveBinding.ID)
	_, err = store.Integrations().GetActiveReadBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "receiving grants no history access")
	_, err = store.Integrations().GetActiveSendBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	input.ReceiveAllowed, input.SendAllowed = false, true
	sender, err := store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	_, err = store.Integrations().GetActiveReadBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "sending grants no history access")
	require.NoError(t, store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, sender.ID))
	_, err = store.Integrations().GetActiveSendBindingForTarget(ctx, testProjectID, agent.ID, target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestChannelParentageIsScopedImmutableAndGrantsNoAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "channel-parentage")
	definitionID := createChannelTestDefinition(t, ctx, store, install)
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, ChannelDefinitionID: definitionID, IntegrationInstallID: install.ID,
		ProviderRefKind: "thread", ProviderRef: "root",
	}
	root, err := store.Integrations().CreateIntegrationTarget(ctx, input)
	require.NoError(t, err)
	_, err = store.Integrations().CreateIntegrationTargetBinding(ctx, integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
		IntegrationTargetID: root.ID, ReadAllowed: true, SendAllowed: true, Source: "application",
	})
	require.NoError(t, err)
	parent := root
	for depth := range 5 {
		input.ProviderRef = fmt.Sprintf("child-%d", depth)
		input.ParentChannelID = parent.ID
		child, err := store.Integrations().CreateIntegrationTarget(ctx, input)
		require.NoError(t, err)
		require.Equal(t, parent.ID, child.ParentChannelID)
		loaded, err := store.Integrations().GetIntegrationTarget(ctx, testProjectID, child.ID)
		require.NoError(t, err)
		require.Equal(t, parent.ID, loaded.ParentChannelID)
		_, err = store.Integrations().GetActiveSendBindingForTarget(ctx, testProjectID, agent.ID, child.ID)
		require.ErrorIs(t, err, storeerr.ErrNotFound, "parent permissions do not propagate")
		_, err = store.Integrations().CreateIntegrationTargetBinding(
			ctx, integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
				IntegrationTargetID: child.ID, ReceiveAllowed: true, Source: "application",
			})
		require.NoError(t, err)
		page, err := store.Integrations().ListAgentChannelTargets(
			ctx, testProjectID, agent.ID, integrationstore.ListAgentChannelTargetsInput{
				Limit: 1, ParentChannelID: parent.ID,
			})
		require.NoError(t, err)
		require.Len(t, page.Targets, 1)
		require.Equal(t, child.ID, page.Targets[0].ID)
		require.Equal(t, parent.ID, page.Targets[0].ParentChannelID)
		require.False(t, page.Targets[0].SendAllowed)
		require.False(t, page.Targets[0].ReadAllowed)
		require.Nil(t, page.Next)
		parent = child
	}
	input.ParentChannelID = root.ID
	_, err = store.Integrations().CreateIntegrationTarget(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "same address cannot be reparented through replay")
	_, err = pool.Exec(ctx, `UPDATE integration_targets SET parent_channel_id = $1 WHERE id = $2`, root.ID, parent.ID)
	require.True(t, isPgCode(err, "25006"), "database rejects parent mutation: %v", err)
	_, _, _, otherInstall := createChannelLifecycleFixture(t, ctx, store, "channel-other-parent")
	input.ChannelDefinitionID = createChannelTestDefinition(t, ctx, store, otherInstall)
	input.IntegrationInstallID = otherInstall.ID
	input.ProviderRef = "cross-connection-child"
	_, err = store.Integrations().CreateIntegrationTarget(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "even same-project parents must belong to the same connection")
}

//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestChannelAccessIntersectsLiveGrantsAndCurrentDefinition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "channel-discovery")
	accepted, err := f.Store.Execution().DeliverChannelWorkflow(ctx, f.event(t, ctx, "first"))
	require.NoError(t, err)
	get := func() integrationstore.ChannelAccess {
		t.Helper()
		access, err := f.Store.Integrations().GetAgentChannelAccess(
			ctx, testProjectID, accepted.AgentInput.AgentID, accepted.ChannelID)
		require.NoError(t, err)
		return access
	}
	access := get()
	require.True(t, access.Active)
	require.True(t, access.ReceiveAllowed)
	require.True(t, access.Capabilities.Read)
	require.True(t, access.Capabilities.Send)
	require.False(t, access.Capabilities.Questions)
	require.Equal(t, f.Definition.ID, access.DefinitionID)
	_, err = f.Store.Integrations().GetAgentChannelAccess(ctx, uuid.New(), accepted.AgentInput.AgentID, accepted.ChannelID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.Store.Integrations().GetAgentChannelAccess(ctx, testProjectID, uuid.New(), accepted.ChannelID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	definition, err := f.Store.Integrations().PublishConnectorChannelDefinition(
		ctx, integrationstore.PublishChannelDefinitionInput{
			ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			ImplementationKey: f.Definition.ImplementationKey, Kind: f.Definition.Kind,
			Description: "Current parameters", SendParamsSchema: json.RawMessage(`{"type":"object","properties":{"notify":{"type":"boolean"}},"additionalProperties":false}`),
			Capabilities: integrationstore.ChannelCapabilities{
				Read: true, Send: true, Text: true, Questions: true, CreatesReplyChannel: true,
			},
			ConnectorCapabilities: f.Identity.Capabilities,
		})
	require.NoError(t, err)
	access = get()
	require.JSONEq(t, string(definition.SendParamsSchema), string(access.SendParamsSchema))
	require.Equal(t, definition.Description, access.Description)
	require.True(t, access.Capabilities.Questions)

	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, accepted.BindingID))
	_, err = f.Store.Integrations().GetAgentChannelAccess(
		ctx, testProjectID, accepted.AgentInput.AgentID, accepted.ChannelID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.Store.Integrations().CreateIntegrationTargetBinding(
		ctx, integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: accepted.AgentInput.AgentID,
			IntegrationInstallID: f.Identity.IntegrationInstallID, IntegrationTargetID: accepted.ChannelID,
			ReadAllowed: true, Source: "history-reader",
		})
	require.NoError(t, err)
	access = get()
	require.True(t, access.Capabilities.Read)
	require.False(t, access.ReceiveAllowed)
	require.False(t, access.Capabilities.Send)
	require.False(t, access.Capabilities.Questions)
	require.False(t, access.Capabilities.CreatesReplyChannel)

	_, err = f.Store.pool.Exec(ctx,
		`UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, access.IntegrationAppID)
	require.NoError(t, err)
	access = get()
	require.False(t, access.Active)
	require.Equal(t, integrationstore.ChannelCapabilities{}, access.Capabilities)
}

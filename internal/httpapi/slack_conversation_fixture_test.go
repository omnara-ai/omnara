//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

// These action/lifecycle tests start with an admitted conversation. The separate
// gateway journey exercises verified intake and actual Slack behavior selection.
func createSlackConversationForHTTPTest(
	t *testing.T, ctx context.Context, f slackEventsIntegrationFixture, providerRef string,
) (executionstore.AgentRecord, integrationstore.IntegrationTargetRecord) {
	t.Helper()
	store := f.Project.Store
	routes, err := store.Integrations().ListActiveIntegrationRoutes(ctx, f.Project.ProjectUUID, f.Install.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	route := routes[0]
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: f.Project.ProjectUUID, IntegrationInstallID: f.Install.ID, ImplementationKey: "slack_thread",
			Kind:             integrationstore.ChannelKindSlackThread,
			SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities: integrationstore.ChannelCapabilities{
				Send: true, Text: true, Questions: true, Permissions: true,
			},
			ConnectorCapabilities: []channelconnector.Capability{{
				ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "slack",
			}},
		})
	require.NoError(t, err)
	capability := channelconnector.Capability{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "slack"}
	_, err = store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: f.Project.ProjectUUID, IntegrationInstallID: f.Install.ID, EventID: "seed-conversation",
		Payload: json.RawMessage(`{"fixture":"conversation"}`), Capabilities: []channelconnector.Capability{capability},
	})
	require.NoError(t, err)
	receipt, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx,
		integrationstore.ClaimNextIntegrationEventInput{
			Capability: capability, LeaseDuration: time.Minute,
		})
	require.NoError(t, err)
	require.True(t, found)
	prepared, err := store.Execution().PrepareChannelWorkflow(ctx, executionstore.ChannelWorkflowIdentity{
		ProjectID: f.Project.ProjectUUID, IntegrationInstallID: f.Install.ID, IntegrationRouteID: route.ID,
		InstanceKey: providerRef, Capabilities: []channelconnector.Capability{capability},
	})
	require.NoError(t, err)
	accepted, err := store.Execution().DeliverChannelWorkflow(ctx, executionstore.DeliverChannelWorkflowInput{
		Prepared: prepared, InputKey: "seed-conversation", ProviderUserID: "U123", ActorDisplayName: "Ada",
		Receipt: executionstore.ChannelEventLease{
			ReceiptID: receipt.ID, LeaseToken: receipt.LeaseToken, LeaseGeneration: receipt.LeaseGeneration,
		},
		Target: integrationstore.CreateIntegrationTargetInput{ChannelDefinitionID: definition.ID,
			ProviderRef: providerRef, ProviderRefKind: "thread", DisplayName: "general"},
		SendAllowed: true,
		Content: executionstore.PreparedInputContent{
			Blocks: json.RawMessage(`[{"type":"text","text":"Please help"}]`),
		},
	})
	require.NoError(t, err)
	target, err := store.Integrations().GetIntegrationTarget(ctx, f.Project.ProjectUUID, accepted.ChannelID)
	require.NoError(t, err)
	agent, err := store.Execution().GetAgentInProject(ctx, f.Project.ProjectUUID, accepted.AgentInput.AgentID)
	require.NoError(t, err)
	return agent, target
}

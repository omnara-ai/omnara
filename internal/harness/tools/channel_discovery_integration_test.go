//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestGetChannelCompletionPreservesSchemaAndOmitsPrivateAddress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "channel-discovery")
	connection := createConnectorToolChannel(t, ctx, fixture, "channel-discovery")
	definition, err := fixture.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: connection.Install.ID,
			ImplementationKey: "discovery",
			Kind:              integrationstore.ChannelKindExternal,
			Description:       "Reply in this conversation.",
			SendParamsSchema:  json.RawMessage(`{"type":"object","properties":{"sequence":{"type":"integer","minimum":9007199254740993}},"additionalProperties":false}`),
			Capabilities:      integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: []channelconnector.Capability{{ConnectorKey: connection.App.ConnectorKey,
				Provider: connection.App.Provider}},
		})
	require.NoError(t, err)
	parent, err := fixture.Store.Integrations().CreateIntegrationTarget(ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: connection.Install.ID,
			ChannelDefinitionID: definition.ID, ProviderRef: "private-parent-address", ProviderRefKind: "conversation",
		})
	require.NoError(t, err)
	child, err := fixture.Store.Integrations().CreateIntegrationTarget(ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: connection.Install.ID,
			ParentChannelID: parent.ID, ChannelDefinitionID: definition.ID, DisplayName: "Public thread name",
			ProviderRef: "private-child-address", ProviderRefKind: "private-address-kind",
			ProviderMetadata: json.RawMessage(`{"private_sentinel":"must-never-reach-model"}`),
		})
	require.NoError(t, err)
	_, err = fixture.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, IntegrationInstallID: connection.Install.ID,
			IntegrationTargetID: child.ID, ReadAllowed: true, Source: "discovery-test",
		})
	require.NoError(t, err)
	_, err = fixture.Store.Integrations().GetAgentChannelAccess(ctx, toolsTestProjectID, fixture.Agent.ID, parent.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "parent ID exposure does not grant access")
	childID, err := publicid.Encode(publicid.KindIntegrationTarget, child.ID)
	require.NoError(t, err)
	parentID, err := publicid.Encode(publicid.KindIntegrationTarget, parent.ID)
	require.NoError(t, err)
	raw, err := json.Marshal(getChannelRequest{ChannelID: childID})
	require.NoError(t, err)
	call := fixture.recordToolCall(t,
		ctx,
		"inspect-channel",
		toolcatalog.ToolNameGetChannel,
		string(raw),
		fixture.Now.Add(20*time.Second))
	turn := fixture.turn()
	turn.Tools[toolcatalog.ToolNameGetChannel] = ToolSpec{
		Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
	}
	executor := Executor{Store: fixture.Store, Now: func() time.Time { return fixture.Now.Add(21 * time.Second) }}
	result, err := executor.Dispatch(ctx, turn, call)
	require.NoError(t, err)
	require.Equal(t, DispatchCompleted, result.Disposition)
	assertProjection := func(parts json.RawMessage) {
		t.Helper()
		for _, private := range []string{"private-parent-address",
			"private-child-address",
			"private-address-kind",
			"private_sentinel",
			"must-never-reach-model",
			connection.App.ID.String(),
			connection.Install.ID.String()} {
			require.NotContains(t, string(parts), private)
		}
		require.Contains(t, string(parts), "9007199254740993")
		require.NotContains(t, string(parts), "9007199254740992")
		body := toolResultMapFromTestParts(t, parts)
		require.Equal(t, childID, body["channel_id"])
		require.Equal(t, parentID, body["parent_channel_id"])
		require.Equal(t, "Public thread name", body["name"])
		capabilities, ok := body["capabilities"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, capabilities["read"])
		require.Equal(t, false, capabilities["send"])
	}
	assertProjection(result.ContentParts)
	recorded, err := fixture.Store.Execution().GetToolCall(ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		fixture.toolCallID(t,
			ctx,
			call.ID))
	require.NoError(t, err)
	assertProjection(recorded.ResultContentParts)
	// Completed-call replay returns the saved observation after grant revocation.
	require.NoError(t,
		fixture.Store.Integrations().DeleteIntegrationInstall(ctx,
			toolsTestProjectID,
			connection.Install.ID))
	replay, err := executor.Dispatch(ctx, turn, call)
	require.NoError(t, err)
	assertProjection(replay.ContentParts)
}

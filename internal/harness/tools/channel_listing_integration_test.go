//go:build integration

package tools

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestListChannelsShowsExternalInstallWithoutAppAndPreservesGrants(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		read bool
		send bool
	}{
		{name: "all-grants", read: true, send: true},
		{name: "receive-only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "list-external-"+tt.name)
			install, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx,
				integrationstore.CreateExternalIntegrationInstallInput{
					OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID,
					InstalledBy: toolsTestUserPrincipal(f.User.ID), DisplayName: "Customer connector",
				})
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationKindExternal, install.IntegrationKind)
			require.Equal(t, uuid.Nil, install.IntegrationAppID)
			definition, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx,
				integrationstore.PublishChannelDefinitionInput{
					ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID,
					ImplementationKey: "customer-room", Kind: integrationstore.ChannelKindExternal,
					SendParamsSchema: json.RawMessage(`{"type":"object"}`),
					Capabilities:     integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
				})
			require.NoError(t, err)
			channel, err := f.Store.Integrations().CreateIntegrationTarget(ctx,
				integrationstore.CreateIntegrationTargetInput{
					ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
					ProviderRef: "customer-room-1", ProviderRefKind: "room", DisplayName: "Customer room",
				})
			require.NoError(t, err)
			_, err = f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
				integrationstore.CreateIntegrationTargetBindingInput{
					ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, IntegrationInstallID: install.ID,
					IntegrationTargetID: channel.ID, ReceiveAllowed: true,
					ReadAllowed: tt.read, SendAllowed: tt.send, Source: "customer-setup",
				})
			require.NoError(t, err)
			id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.ID)
			require.NoError(t, err)
			call := f.recordToolCall(t, ctx, "list", toolcatalog.ToolNameListChannels, `{}`, f.Now)
			executor := Executor{Store: f.Store, ChannelOperations: unexpectedChannelOperations(t)}
			result, err := executor.Dispatch(ctx, managedToolTurn(f), call)
			require.NoError(t, err)
			require.Equal(t, DispatchCompleted, result.Disposition)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			channels, ok := body["channels"].([]any)
			require.True(t, ok, "list_channels result: %s", result.ContentParts)
			for _, item := range channels {
				listed, ok := item.(map[string]any)
				require.True(t, ok)
				if listed["channel_id"] != id {
					continue
				}
				require.Equal(t, "Customer room", listed["name"])
				require.Equal(t, "active", listed["state"])
				require.Equal(t, true, listed["can_receive"])
				require.Equal(t, tt.read, listed["can_read"])
				require.Equal(t, tt.send, listed["can_send"])
				return
			}
			t.Fatalf("external channel %s missing from list_channels result: %s", id, result.ContentParts)
		})
	}
}

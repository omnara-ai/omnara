//go:build integration

package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestExternalChannelAddressSurvivesRegistrationAndSendCompletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, address string }{
		{"over_old_limit", strings.Repeat("a", 513)},
		{"ascii_limit", strings.Repeat("a", 2048)},
		{"utf8_limit", strings.Repeat("é", 1024)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool := openIntegrationDB(t, t.Context())
			handler := newIntegrationServer(pool)
			project := bootstrapPublicHTTPProject(t, handler, "external-address-"+tc.name)
			root := createExternalRequestHTTPChannel(t, handler, project, "root")
			installPath := project.ProjectPath + "/integration-installs/" +
				testPublicID(t, publicid.KindIntegrationInstall, root.IntegrationInstallID)
			post := func(path string, body any) map[string]any {
				return requestJSONWithHeaders(t, handler, http.MethodPost, path, workflowHTTPJSON(t, body), "",
					http.StatusOK, authHeaders(project.AdminToken))
			}
			registered := post(installPath+"/channels", map[string]any{
				"source": "external", "definition_id": testPublicID(t, publicid.KindChannelDefinition, root.ChannelDefinitionID),
				"provider_ref": tc.address, "provider_ref_kind": "conversation", "name": "Long address",
			})
			channelID := mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelReceiptString(t, registered, "channel_id"))
			channel, err := project.Store.Integrations().GetIntegrationTarget(t.Context(), project.ProjectUUID, channelID)
			require.NoError(t, err)
			waiting := createExternalRequestHTTPWaitingTools(t, project, []integrationstore.IntegrationTargetRecord{channel})[0]
			requests := requestJSONWithHeaders(t, handler, http.MethodGet, installPath+"/requests", "", "",
				http.StatusOK, authHeaders(project.AdminToken))
			items := testutil.RequireType[[]any](t, requests["data"])
			require.Len(t, items, 1)
			request := testutil.RequireType[map[string]any](t, items[0])
			payload := testutil.RequireType[map[string]any](t, request["payload"])
			destination := testutil.RequireType[map[string]any](t, payload["destination"])
			require.Equal(t, tc.address, destination["provider_ref"])
			// Preserve the byte length but use a different child address.
			childAddress := strings.Replace(tc.address, "a", "b", 1)
			childAddress = strings.Replace(childAddress, "é", "ø", 1)
			completed := post(installPath+"/requests/"+
				testPublicID(t, publicid.KindExternalChannelRequest, waiting.request.ID)+"/result", map[string]any{
				"outcome": "completed", "payload": map[string]any{
					"publication": "published", "message_channel": "reply_channel", "message_id": "message-1",
					"reply_channel": map[string]any{
						"implementation_key": "conversation", "provider_ref": childAddress, "provider_ref_kind": "thread",
					},
				},
			})
			blocks := testutil.RequireType[[]any](t, completed["tool_result_content_blocks"])
			require.Len(t, blocks, 1)
			block := testutil.RequireType[map[string]any](t, blocks[0])
			value := testutil.RequireType[map[string]any](t, block["value"])
			message := testutil.RequireType[map[string]any](t, value["message"])
			require.Equal(t, "published", message["publication"])
			require.NotContains(t, value, "continuation_error")
			childID := mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelReceiptString(t, message, "reply_channel_id"))
			child, err := project.Store.Integrations().GetAgentChannelAccess(
				t.Context(), project.ProjectUUID, waiting.request.AgentID, childID)
			require.NoError(t, err)
			require.Equal(t, channel.ID, child.ParentChannelID)
			require.Equal(t, childAddress, child.ProviderRef)
			require.True(t, child.ReceiveAllowed)
			require.True(t, child.Capabilities.Send)
		})
	}
}

func TestExternalChannelRegistrationRejectsAddressOverByteLimit(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "external-address-over-byte-limit")
	root := createExternalRequestHTTPChannel(t, handler, project, "root")
	path := project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, root.IntegrationInstallID) + "/channels"
	// This fits the JSON Schema character bound but exceeds the UTF-8 byte bound.
	requestJSONWithHeaders(t, handler, http.MethodPost, path, workflowHTTPJSON(t, map[string]any{
		"source": "external", "definition_id": testPublicID(t, publicid.KindChannelDefinition, root.ChannelDefinitionID),
		"provider_ref": strings.Repeat("é", 2048), "provider_ref_kind": "conversation", "name": "Oversized address",
	}), "", http.StatusBadRequest, authHeaders(project.AdminToken))
}

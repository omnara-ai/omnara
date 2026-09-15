//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestPublicAgentChannelSetupAndDiscovery(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "public-channels")
	parent := createExternalRequestHTTPChannel(t, handler, project, "parent")
	other := createExternalRequestHTTPChannel(t, handler, project, "other-connection")
	installPath := project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, parent.IntegrationInstallID)
	parentID := testPublicID(t, publicid.KindIntegrationTarget, parent.ID)
	definitionID := testPublicID(t, publicid.KindChannelDefinition, parent.ChannelDefinitionID)
	post := func(path, body string, status int) map[string]any {
		return requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "", status, authHeaders(project.AdminToken))
	}
	childBody := workflowHTTPJSON(t, map[string]any{
		"definition_id": definitionID, "parent_channel_id": parentID,
		"provider_ref": "child", "provider_ref_kind": "thread", "name": "Child",
		"provider_metadata": map[string]any{"provider_only": "private"},
	})
	child := post(installPath+"/channels", childBody, http.StatusOK)
	require.Equal(t, child, post(installPath+"/channels", childBody, http.StatusOK))
	require.Equal(t, parentID, child["parent_channel_id"])
	childID := channelReceiptString(t, child, "channel_id")
	launch := createHTTPRuntimeAgent(t, ctx, project.Store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, "channel-owner")
	otherAgent := createHTTPRuntimeAgent(t, ctx, project.Store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, "other-channel-owner")
	agentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, launch.Agent.ID)
	otherAgentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, otherAgent.Agent.ID)
	parentBody := workflowHTTPJSON(t, map[string]any{
		"channel_id": parentID, "grants": openapi.ChannelGrants{Receive: true, Read: true, Send: true},
		"reply_channel_grants": openapi.ChannelGrants{Receive: true, Send: true},
	})
	binding := post(agentPath+"/channel-bindings", parentBody, http.StatusOK)
	require.Equal(t, binding, post(agentPath+"/channel-bindings", parentBody, http.StatusOK))
	bindingID := channelReceiptString(t, binding, "id")
	record, err := project.Store.Integrations().GetIntegrationTargetBinding(ctx, project.ProjectUUID,
		mustPublicHTTPID(t, publicid.KindIntegrationBinding, bindingID))
	require.NoError(t, err)
	require.Equal(t, "api", record.Source)
	require.Equal(t, uuid.Nil, record.IntegrationRouteID)
	require.Equal(t, &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true}, record.ReplyChannelGrants)
	post(agentPath+"/channel-bindings", workflowHTTPJSON(t, map[string]any{
		"channel_id": childID, "grants": openapi.ChannelGrants{Read: true},
	}), http.StatusOK)
	current, err := project.Store.Execution().GetAgentCurrentChannelID(ctx, project.ProjectUUID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, current, "binding setup does not choose a current channel")
	// Seed an existing persistent selection. Setup/discovery must preserve it,
	// including replacement and later revocation of its binding.
	_, err = pool.Exec(ctx, `UPDATE agents SET integration_target_id = $1 WHERE project_id = $2 AND id = $3`,
		parent.ID, project.ProjectUUID, launch.Agent.ID)
	require.NoError(t, err)

	token, role := createChannelHTTPKey(t, project, "viewer")
	get := func(path string, status int) map[string]any {
		return requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", status, authHeaders(token))
	}
	page := get(agentPath+"/channels?limit=1", http.StatusOK)
	require.Equal(t, parentID, page["current_channel_id"])
	items := testutil.RequireType[[]any](t, page["channels"])
	require.Len(t, items, 1)
	item := testutil.RequireType[map[string]any](t, items[0])
	require.Equal(t, childID, item["channel_id"])
	require.Equal(t, "active", item["state"], "external connections do not require a managed app")
	require.Equal(t, true, item["can_read"])
	require.Equal(t, false, item["can_send"])
	require.NotContains(t, item, "provider_ref")
	require.NotContains(t, item, "provider_metadata")
	cursor := channelReceiptString(t, page, "next_cursor")
	second := get(agentPath+"/channels?limit=1&cursor="+url.QueryEscape(cursor), http.StatusOK)
	require.Nil(t, second["next_cursor"])
	items = testutil.RequireType[[]any](t, second["channels"])
	require.Len(t, items, 1)
	require.Equal(t, parentID, testutil.RequireType[map[string]any](t, items[0])["channel_id"])
	filtered := get(agentPath+"/channels?parent_channel_id="+parentID, http.StatusOK)
	require.Len(t, testutil.RequireType[[]any](t, filtered["channels"]), 1)
	get(agentPath+"/channels?parent_channel_id="+parentID+"&cursor="+url.QueryEscape(cursor), http.StatusBadRequest)
	get(otherAgentPath+"/channels?cursor="+url.QueryEscape(cursor), http.StatusBadRequest)
	get(agentPath+"/channels?cursor=invalid", http.StatusBadRequest)
	get(agentPath+"/channels?limit=0", http.StatusBadRequest)
	detail := get(agentPath+"/channels/"+childID, http.StatusOK)
	require.Equal(t, parentID, detail["parent_channel_id"])
	require.Equal(t, "EXTERNAL", detail["kind"])
	require.Equal(t, false, detail["is_current"])
	capabilities := testutil.RequireType[map[string]any](t, detail["capabilities"])
	require.Equal(t, true, capabilities["read"])
	require.Equal(t, false, capabilities["send"])
	require.Equal(t, false, capabilities["creates_reply_channel"])
	require.NotContains(t, detail, "provider_metadata")
	get(otherAgentPath+"/channels/"+childID, http.StatusNotFound)
	get(agentPath+"/channels/"+testPublicID(t, publicid.KindIntegrationTarget, other.ID), http.StatusNotFound)
	require.Equal(t, true, get(agentPath+"/channels/"+parentID, http.StatusOK)["is_current"])

	foreignProject, err := project.Store.Identity().CreateProjectForPrincipal(ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: project.OrgUUID, Creator: identitystore.NewUserPrincipal(project.AdminUserUUID),
			Name: "Other channel project", IdempotencyKey: "other-channel-project",
		})
	require.NoError(t, err)
	foreignPath := "/api/v1/orgs/" + project.OrgID + "/projects/" +
		testPublicID(t, publicid.KindProject, foreignProject.ID)
	foreignInstallPath := foreignPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, parent.IntegrationInstallID)
	post(foreignInstallPath+"/channel-definitions", externalHTTPDefinitionBody(), http.StatusNotFound)
	post(foreignInstallPath+"/channels", childBody, http.StatusNotFound)
	foreignAgentPath := foreignPath + "/agents/" + testPublicID(t, publicid.KindAgent, launch.Agent.ID)
	post(foreignAgentPath+"/channel-bindings", parentBody, http.StatusNotFound)
	requestJSONWithHeaders(t, handler, http.MethodGet, foreignAgentPath+"/channels", "", "",
		http.StatusNotFound, authHeaders(project.AdminToken))
	requestJSONWithHeaders(t, handler, http.MethodDelete, foreignAgentPath+"/channel-bindings/"+bindingID, "", "",
		http.StatusNotFound, authHeaders(project.AdminToken))

	for _, name := range []string{"viewer", "operator"} {
		role.Role = name
		_, err = project.Store.Identity().SetOrgAPIKeyProjectRole(ctx, role)
		require.NoError(t, err)
		requestJSONWithHeaders(t, handler, http.MethodPost, installPath+"/channel-definitions",
			externalHTTPDefinitionBody(), "", http.StatusForbidden, authHeaders(token))
		requestJSONWithHeaders(t, handler, http.MethodPost, installPath+"/channels", childBody, "",
			http.StatusForbidden, authHeaders(token))
		requestJSONWithHeaders(t, handler, http.MethodPost, agentPath+"/channel-bindings", parentBody, "",
			http.StatusForbidden, authHeaders(token))
		requestJSONWithHeaders(t, handler, http.MethodDelete, agentPath+"/channel-bindings/"+bindingID, "", "",
			http.StatusForbidden, authHeaders(token))
	}
	for _, invalid := range []map[string]any{
		{"grants": openapi.ChannelGrants{}},
		{"grants": openapi.ChannelGrants{Read: true}, "reply_channel_grants": openapi.ChannelGrants{Send: true}},
		{"grants": openapi.ChannelGrants{Send: true}, "reply_channel_grants": openapi.ChannelGrants{}},
		{"grants": openapi.ChannelGrants{Send: true}, "source": "connector"},
		{"grants": openapi.ChannelGrants{Send: true}, "route_id": "caller-selected"},
	} {
		invalid["channel_id"] = parentID
		post(agentPath+"/channel-bindings", workflowHTTPJSON(t, invalid), http.StatusBadRequest)
	}
	post(installPath+"/channel-definitions",
		strings.Replace(externalHTTPDefinitionBody(), "EXTERNAL", "SLACK_CHANNEL", 1), http.StatusBadRequest)
	post(installPath+"/channels", strings.Replace(childBody, definitionID,
		testPublicID(t, publicid.KindChannelDefinition, other.ChannelDefinitionID), 1), http.StatusNotFound)
	post(installPath+"/channels", strings.Replace(childBody, parentID,
		testPublicID(t, publicid.KindIntegrationTarget, other.ID), 1), http.StatusNotFound)
	// Replacing grants creates a new immutable identity; deleting the old one
	// again cannot revoke the replacement or a binding on another agent.
	replacement := post(agentPath+"/channel-bindings", workflowHTTPJSON(t, map[string]any{
		"channel_id": parentID, "grants": openapi.ChannelGrants{Read: true},
	}), http.StatusOK)
	replacementID := channelReceiptString(t, replacement, "id")
	require.NotEqual(t, bindingID, replacementID)
	requestJSONWithHeaders(t, handler, http.MethodDelete, agentPath+"/channel-bindings/"+bindingID, "", "",
		http.StatusNoContent, authHeaders(project.AdminToken))
	for _, id := range []string{bindingID, replacementID} {
		requestJSONWithHeaders(t, handler, http.MethodDelete, otherAgentPath+"/channel-bindings/"+id, "", "",
			http.StatusNotFound, authHeaders(project.AdminToken))
	}
	get(agentPath+"/channels/"+parentID, http.StatusOK)
	for range 2 {
		requestJSONWithHeaders(t, handler, http.MethodDelete, agentPath+"/channel-bindings/"+replacementID, "", "",
			http.StatusNoContent, authHeaders(project.AdminToken))
	}
	get(agentPath+"/channels/"+parentID, http.StatusNotFound)
	current, err = project.Store.Execution().GetAgentCurrentChannelID(ctx, project.ProjectUUID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, parent.ID, current)
}

func TestPublicExternalChannelRegistrationRejectsInvalidText(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "channel-registration-validation")
	channel := createExternalRequestHTTPChannel(t, handler, project, "validation-parent")
	path := project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, channel.IntegrationInstallID) + "/channels"
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"blank ref", "provider_ref", " \t\n"},
		{"blank kind", "provider_ref_kind", " \t\n"},
		{"ref UTF-8 bytes", "provider_ref", strings.Repeat("é", 1025)},
		{"kind UTF-8 bytes", "provider_ref_kind", strings.Repeat("é", 65)},
		{"name UTF-8 bytes", "name", strings.Repeat("é", 257)},
		{"ref NUL", "provider_ref", "channel\x00invalid"},
		{"kind NUL", "provider_ref_kind", "thread\x00invalid"},
		{"name NUL", "name", "Invalid\x00channel"},
		{"metadata NUL", "provider_metadata", map[string]string{"value": "metadata\x00invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := map[string]any{
				"definition_id": testPublicID(t, publicid.KindChannelDefinition, channel.ChannelDefinitionID),
				"provider_ref":  "new-address", "provider_ref_kind": "thread", "name": "New channel",
			}
			body[tc.field] = tc.value
			requestJSONWithHeaders(t, handler, http.MethodPost, path, workflowHTTPJSON(t, body), "",
				http.StatusBadRequest, authHeaders(project.AdminToken))
		})
	}
}

func TestPublicChannelSetupKeepsManagedProviderAuthority(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "managed-channel-public")
	app, err := project.Store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: project.OrgUUID, Provider: "slack", ProviderAppRef: "public-binding-test", DisplayName: "Slack",
		ConnectorKey: "chat_sdk_v1", State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	install, err := project.Store.Integrations().UpsertIntegrationInstall(ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: app.ID,
			InstalledBy: identitystore.NewUserPrincipal(project.AdminUserUUID), Provider: "slack",
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "public-binding-team", ProviderAccountRef: "bot",
		})
	require.NoError(t, err)
	definition, err := project.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: project.ProjectUUID, IntegrationInstallID: install.ID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindSlackChannel,
			SendParamsSchema:      json.RawMessage(`{"type":"object"}`),
			Capabilities:          integrationstore.ChannelCapabilities{Send: true, Text: true},
			ConnectorCapabilities: []channelconnector.Capability{{ConnectorKey: "chat_sdk_v1", Provider: "slack"}},
		})
	require.NoError(t, err)
	target, err := project.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: project.ProjectUUID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "CCHANNEL", ProviderRefKind: "channel", DisplayName: "Managed channel",
	})
	require.NoError(t, err)
	installPath := project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, install.ID)
	requestJSONWithHeaders(t, handler, http.MethodPost, installPath+"/channel-definitions",
		externalHTTPDefinitionBody(), "", http.StatusNotFound, authHeaders(project.AdminToken))
	requestJSONWithHeaders(t, handler, http.MethodPost, installPath+"/channels", workflowHTTPJSON(t, map[string]any{
		"definition_id": testPublicID(t, publicid.KindChannelDefinition, definition.ID),
		"provider_ref":  "CNEW", "provider_ref_kind": "channel", "name": "Unauthorized registration",
	}), "", http.StatusNotFound, authHeaders(project.AdminToken))
	launch := createHTTPRuntimeAgent(t, ctx, project.Store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, "managed-binding-owner")
	agentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, launch.Agent.ID)
	targetID := testPublicID(t, publicid.KindIntegrationTarget, target.ID)
	binding := requestJSONWithHeaders(t, handler, http.MethodPost, agentPath+"/channel-bindings",
		workflowHTTPJSON(t, map[string]any{"channel_id": targetID, "grants": openapi.ChannelGrants{Send: true}}), "",
		http.StatusOK, authHeaders(project.AdminToken))
	detail := requestJSONWithHeaders(t, handler, http.MethodGet, agentPath+"/channels/"+targetID, "", "",
		http.StatusOK, authHeaders(project.AdminToken))
	require.Equal(t, "SLACK_CHANNEL", detail["kind"])
	require.Equal(t, true, detail["active"])
	requestJSONWithHeaders(t, handler, http.MethodDelete,
		agentPath+"/channel-bindings/"+channelReceiptString(t, binding, "id"), "", "",
		http.StatusNoContent, authHeaders(project.AdminToken))
}

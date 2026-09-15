//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestPublicChannelLaunchRequiresManagementOnlyForGrants(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "channel-launch-authority")
	channel := createExternalRequestHTTPChannel(t, handler, project, "launch-channel")
	profile := createPublicHTTPAgent(t, handler, project, "channel-launch", project.AdminToken)
	config := testutil.RequireType[map[string]any](t, profile["current_config"])
	request := map[string]any{"profile": profile["id"], "config": config["id"]}
	token, role := createChannelHTTPKey(t, project, "operator")
	path := project.ProjectPath + "/agents"
	requestJSONWithHeaders(t, handler, http.MethodPost, path, workflowHTTPJSON(t, request),
		"operator-no-grants", http.StatusCreated, authHeaders(token))
	request["channel_bindings"] = []openapi.AttachAgentChannelRequest{}
	requestJSONWithHeaders(t, handler, http.MethodPost, path, workflowHTTPJSON(t, request),
		"operator-empty-grants", http.StatusCreated, authHeaders(token))
	channelID := testPublicID(t, publicid.KindIntegrationTarget, channel.ID)
	request["channel_bindings"] = []openapi.AttachAgentChannelRequest{{
		ChannelId: channelID, Grants: openapi.ChannelGrants{Receive: true, Send: true},
		ReplyChannelGrants: &openapi.ChannelGrants{Receive: true},
	}}
	body := workflowHTTPJSON(t, request)
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body,
		"launch-with-grants", http.StatusForbidden, authHeaders(token))
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1 AND idempotency_key=$2`,
		project.ProjectUUID, "launch-with-grants").Scan(&count))
	require.Zero(t, count, "a rejected grant does not create an agent")
	role.Role = "developer"
	_, err := project.Store.Identity().SetOrgAPIKeyProjectRole(ctx, role)
	require.NoError(t, err)
	created := requestJSONWithHeaders(t, handler, http.MethodPost, path, body,
		"launch-with-grants", http.StatusCreated, authHeaders(token))
	agent := testutil.RequireType[map[string]any](t, created["agent"])
	agentID := mustPublicHTTPID(t, publicid.KindAgent, channelReceiptString(t, agent, "id"))
	binding, err := project.Store.Integrations().GetActiveReceiveBindingForTarget(
		ctx, project.ProjectUUID, agentID, channel.ID)
	require.NoError(t, err)
	require.Equal(t, "api", binding.Source)
	require.Equal(t, storage.NilID, binding.IntegrationRouteID)
	require.True(t, binding.SendAllowed)
	require.False(t, binding.ReadAllowed)
	require.NotNil(t, binding.ReplyChannelGrants)
	require.True(t, binding.ReplyChannelGrants.ReceiveAllowed)
	current, err := project.Store.Execution().GetAgentCurrentChannelID(ctx, project.ProjectUUID, agentID)
	require.NoError(t, err)
	require.Equal(t, storage.NilID, current, "launch grants do not select a current channel")
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+channelReceiptString(t, agent, "id")+
		"/channel-bindings/"+testPublicID(t, publicid.KindIntegrationBinding, binding.ID), "", "",
		http.StatusNoContent, authHeaders(token))
	replayed := requestJSONWithHeaders(t, handler, http.MethodPost, path, body,
		"launch-with-grants", http.StatusOK, authHeaders(token))
	require.Equal(t, agent["id"], testutil.RequireType[map[string]any](t, replayed["agent"])["id"])
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings
		WHERE project_id=$1 AND agent_id=$2 AND integration_target_id=$3 AND revoked_at IS NULL`,
		project.ProjectUUID, agentID, channel.ID).Scan(&count))
	require.Zero(t, count, "launch replay cannot restore revoked grants")
	role.Role = "operator"
	_, err = project.Store.Identity().SetOrgAPIKeyProjectRole(ctx, role)
	require.NoError(t, err)
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body,
		"launch-with-grants", http.StatusForbidden, authHeaders(token))
}

func TestPublicChannelInputReplayPreservesAcceptedOrigin(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "channel-input-replay")
	channel := createExternalRequestHTTPChannel(t, handler, project, "input-channel")
	other := createExternalRequestHTTPChannel(t, handler, project, "other-input-channel")
	launch := createHTTPRuntimeAgent(t, ctx, project.Store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, "public-input-owner")
	agentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, launch.Agent.ID)
	channelID := testPublicID(t, publicid.KindIntegrationTarget, channel.ID)
	otherID := testPublicID(t, publicid.KindIntegrationTarget, other.ID)
	token, role := createChannelHTTPKey(t, project, "operator")
	input := map[string]any{
		"channel_id":     channelID,
		"actor":          openapi.ExternalActorParams{ProviderUserId: "customer-alice"},
		"content_blocks": []map[string]string{{"type": "text", "text": "original customer message"}},
	}
	body := workflowHTTPJSON(t, input)
	path := agentPath + "/inputs"
	requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "unbound-input",
		http.StatusNotFound, authHeaders(token))
	var actors int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM actors WHERE project_id=$1 AND provider_user_id=$2`,
		project.ProjectUUID, "customer-alice").Scan(&actors))
	require.Zero(t, actors, "unauthorized input cannot create its claimed actor")
	grant := func(channel string, grants openapi.ChannelGrants) map[string]any {
		return requestJSONWithHeaders(t, handler, http.MethodPost, agentPath+"/channel-bindings",
			workflowHTTPJSON(t, openapi.AttachAgentChannelRequest{ChannelId: channel, Grants: grants}), "",
			http.StatusOK, authHeaders(project.AdminToken))
	}
	binding := grant(channelID, openapi.ChannelGrants{Receive: true, Send: true})
	accepted := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "accepted-input",
		http.StatusCreated, authHeaders(token))
	acceptedInput := testutil.RequireType[map[string]any](t, accepted["agent_input"])
	inputID := mustPublicHTTPID(t, publicid.KindAgentInput, channelReceiptString(t, acceptedInput, "id"))
	var savedChannel, savedBinding storage.ID
	require.NoError(t, pool.QueryRow(ctx, `SELECT integration_target_id, integration_target_binding_id
		FROM agent_inputs WHERE project_id=$1 AND id=$2`, project.ProjectUUID, inputID).Scan(&savedChannel, &savedBinding))
	require.Equal(t, channel.ID, savedChannel)
	require.Equal(t, channelReceiptString(t, binding, "id"),
		testPublicID(t, publicid.KindIntegrationBinding, savedBinding))
	actor := requestJSONWithHeaders(t, handler, http.MethodGet,
		project.ProjectPath+"/actors/"+channelReceiptString(t, acceptedInput, "actor_id"), "", "",
		http.StatusOK, authHeaders(token))
	require.Equal(t, "external", actor["provider"])
	require.Equal(t, "customer-alice", actor["provider_user_id"])
	current, err := project.Store.Execution().GetAgentCurrentChannelID(ctx, project.ProjectUUID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, storage.NilID, current, "queueing an input does not redirect the agent")
	work, found, err := project.Store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, work.Kind)
	require.Equal(t, launch.Agent.ID, work.RuntimeLock.AgentID)
	current, err = project.Store.Execution().GetAgentCurrentChannelID(ctx, project.ProjectUUID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, channel.ID, current)
	replacement := grant(channelID, openapi.ChannelGrants{Send: true})
	require.NotEqual(t, binding["id"], replacement["id"])
	grant(otherID, openapi.ChannelGrants{Receive: true})
	// The selector may move after admission; replay is not a new input.
	_, err = pool.Exec(ctx, `UPDATE agents SET integration_target_id=$1 WHERE project_id=$2 AND id=$3`,
		other.ID, project.ProjectUUID, launch.Agent.ID)
	require.NoError(t, err)
	fresh := newIntegrationServer(pool)
	requireReplay := func(response map[string]any) {
		replayedInput := testutil.RequireType[map[string]any](t, response["agent_input"])
		for _, field := range []string{"id", "actor_id", "content_blocks", "delivery_mode", "queued_at"} {
			require.Equal(t, acceptedInput[field], replayedInput[field], "accepted input %s is immutable", field)
		}
	}
	replay := requestJSONWithHeaders(t, fresh, http.MethodPost, path, body, "accepted-input",
		http.StatusOK, authHeaders(token))
	requireReplay(replay)
	requestJSONWithHeaders(t, fresh, http.MethodPost, path, body, "fresh-without-receive",
		http.StatusNotFound, authHeaders(token))
	for _, change := range []map[string]any{
		{"channel_id": otherID},
		{"content_blocks": []map[string]string{{"type": "text", "text": "changed customer message"}}},
		{"actor": openapi.ExternalActorParams{ProviderUserId: "customer-mallory"}},
	} {
		changed := make(map[string]any, len(input))
		for key, value := range input {
			changed[key] = value
		}
		for key, value := range change {
			changed[key] = value
		}
		requestJSONWithHeaders(t, fresh, http.MethodPost, path, workflowHTTPJSON(t, changed), "accepted-input",
			http.StatusConflict, authHeaders(token))
	}
	requestJSONWithHeaders(t, fresh, http.MethodDelete, project.ProjectPath+"/integration-installs/"+
		testPublicID(t, publicid.KindIntegrationInstall, channel.IntegrationInstallID), "", "",
		http.StatusNoContent, authHeaders(project.AdminToken))
	replay = requestJSONWithHeaders(t, fresh, http.MethodPost, path, body, "accepted-input",
		http.StatusOK, authHeaders(token))
	requireReplay(replay)
	current, err = project.Store.Execution().GetAgentCurrentChannelID(ctx, project.ProjectUUID, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, other.ID, current)
	var inputs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs
		WHERE project_id=$1 AND agent_id=$2 AND input_idempotency_key=$3`,
		project.ProjectUUID, launch.Agent.ID, "accepted-input").Scan(&inputs))
	require.Equal(t, 1, inputs)
	require.NoError(t, pool.QueryRow(ctx, `SELECT integration_target_id, integration_target_binding_id
		FROM agent_inputs WHERE project_id=$1 AND id=$2`, project.ProjectUUID, inputID).Scan(&savedChannel, &savedBinding))
	require.Equal(t, channel.ID, savedChannel)
	require.Equal(t, channelReceiptString(t, binding, "id"),
		testPublicID(t, publicid.KindIntegrationBinding, savedBinding))
	role.Role = "viewer"
	_, err = project.Store.Identity().SetOrgAPIKeyProjectRole(ctx, role)
	require.NoError(t, err)
	requestJSONWithHeaders(t, fresh, http.MethodPost, path, body, "accepted-input",
		http.StatusForbidden, authHeaders(token))
}

func createChannelHTTPKey(
	t *testing.T, project publicHTTPProject, projectRole string,
) (string, identitystore.OrgAPIKeyProjectRoleInput) {
	t.Helper()
	principal, _, err := project.Store.Identity().AuthenticateBrowserSession(t.Context(), project.AdminSession)
	require.NoError(t, err)
	key, err := project.Store.Identity().CreateOrgAPIKeyWithPlaintext(t.Context(), identitystore.CreateOrgAPIKeyInput{
		OrgID: project.OrgUUID, CreatedByUserID: project.AdminUserUUID, Name: "Channel client", OrgRole: "member",
		ActorPrincipal: principal,
	})
	require.NoError(t, err)
	role := identitystore.OrgAPIKeyProjectRoleInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, KeyID: key.Record.ID,
		ActorPrincipal: principal, Role: projectRole,
	}
	_, err = project.Store.Identity().SetOrgAPIKeyProjectRole(t.Context(), role)
	require.NoError(t, err)
	return key.Token, role
}

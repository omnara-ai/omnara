//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCronTriggerChannelBindingsRoundTrip(t *testing.T) {
	t.Parallel()
	f := newCronChannelHTTPFixture(t)
	body := f.profileSchedule("scheduled-channel-work")
	created := f.request(t, http.MethodPost, f.path, body, "create-channel-schedule", http.StatusCreated)
	path := f.path + "/" + channelReceiptString(t, created, "id")
	want := f.publicBindings()
	require.Equal(t, want, created["channel_bindings"])
	require.Equal(t, map[string]any{"type": "profile", "agent_profile_id": f.profileID}, created["target"])
	replayed := f.request(t, http.MethodPost, f.path, body, "create-channel-schedule", http.StatusOK)
	require.Equal(t, created, replayed)
	assertReads := func(bindings []any) {
		loaded := f.request(t, http.MethodGet, path, nil, "", http.StatusOK)
		require.Equal(t, bindings, loaded["channel_bindings"])
		listed := f.request(t, http.MethodGet, f.path+"?limit=1", nil, "", http.StatusOK)
		rows := testutil.RequireType[[]any](t, listed["data"])
		require.Len(t, rows, 1)
		require.Equal(t, loaded, rows[0])
		require.Nil(t, listed["next_cursor"])
	}
	assertReads(want)
	patched := f.request(t, http.MethodPatch, path,
		map[string]any{"message_template": "Changed message; same channel authority."}, "", http.StatusOK)
	require.Equal(t, want, patched["channel_bindings"], "omitting channel_bindings preserves the accepted grants")
	require.Equal(t, "Changed message; same channel authority.", patched["message_template"])
	assertReads(want)
	for range 2 {
		cleared := f.request(t, http.MethodPatch, path,
			map[string]any{"channel_bindings": []openapi.AttachAgentChannelRequest{}}, "", http.StatusOK)
		require.Equal(t, []any{}, cleared["channel_bindings"], "an explicit empty array clears rather than omits grants")
		assertReads([]any{})
	}
	var agents, bindings, configured int
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM integration_target_bindings WHERE project_id=$1),
		(SELECT count(*) FROM cron_trigger_channel_bindings WHERE project_id=$1)`,
		f.project.ProjectUUID).Scan(&agents, &bindings, &configured))
	require.Zero(t, agents, "schedule configuration does not launch an agent")
	require.Zero(t, bindings, "schedule configuration does not grant an existing agent access")
	require.Zero(t, configured)
}

func TestCronTriggerChannelBindingsRejectForeignChannelAndAgentTarget(t *testing.T) {
	t.Parallel()
	f := newCronChannelHTTPFixture(t)
	other, err := f.project.Store.Identity().CreateProjectForPrincipal(t.Context(),
		identitystore.CreateProjectForPrincipalInput{
			OrgID: f.project.OrgUUID, Creator: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
			Name: "Other cron channel project", IdempotencyKey: "other-cron-channel-project",
		})
	require.NoError(t, err)
	foreignProject := f.project
	foreignProject.ProjectUUID, foreignProject.ProjectID = other.ID, testPublicID(t, publicid.KindProject, other.ID)
	foreignProject.ProjectPath = "/api/v1/orgs/" + f.project.OrgID + "/projects/" + foreignProject.ProjectID
	foreign := createExternalRequestHTTPChannel(t, f.handler, foreignProject, "foreign-cron-parent")
	foreignBindings := f.bindings()
	foreignBindings[0].ChannelId = testPublicID(t, publicid.KindIntegrationTarget, foreign.ID)
	body := f.profileSchedule("foreign-channel-schedule")
	body["channel_bindings"] = foreignBindings
	f.request(t, http.MethodPost, f.path, body, "rejected-foreign-channel", http.StatusNotFound)
	created := f.request(t, http.MethodPost, f.path, f.profileSchedule("valid-channel-schedule"),
		"valid-channel-schedule", http.StatusCreated)
	path := f.path + "/" + channelReceiptString(t, created, "id")
	f.request(t, http.MethodPatch, path,
		map[string]any{"channel_bindings": foreignBindings, "name": "Must not change"}, "", http.StatusNotFound)
	require.Equal(t, created, f.request(t, http.MethodGet, path, nil, "", http.StatusOK))
	launch := f.request(t, http.MethodPost, f.project.ProjectPath+"/agents",
		map[string]any{"profile": f.profileID, "config": f.configID}, "cron-existing-agent", http.StatusCreated)
	agent := testutil.RequireType[map[string]any](t, launch["agent"])
	body = f.profileSchedule("existing-agent-schedule")
	body["target"] = map[string]any{"type": "agent", "agent_id": agent["id"]}
	f.request(t, http.MethodPost, f.path, body, "existing-agent-schedule", http.StatusBadRequest)
	body["channel_bindings"] = []openapi.AttachAgentChannelRequest{}
	agentSchedule := f.request(t, http.MethodPost, f.path, body, "existing-agent-schedule", http.StatusCreated)
	require.Equal(t, []any{}, agentSchedule["channel_bindings"])
	agentPath := f.path + "/" + channelReceiptString(t, agentSchedule, "id")
	f.request(t, http.MethodPatch, agentPath,
		map[string]any{"channel_bindings": f.bindings()}, "", http.StatusBadRequest)
	require.Equal(t, agentSchedule, f.request(t, http.MethodGet, agentPath, nil, "", http.StatusOK))
	listed := f.request(t, http.MethodGet, f.path, nil, "", http.StatusOK)
	require.Len(t, testutil.RequireType[[]any](t, listed["data"]), 2, "rejected creates leave no schedules")
}

func TestCronTriggerChannelBindingsRequireProjectManage(t *testing.T) {
	t.Parallel()
	f := newCronChannelHTTPFixture(t)
	created := f.request(t, http.MethodPost, f.path, f.profileSchedule("managed-schedule"),
		"managed-schedule", http.StatusCreated)
	path := f.path + "/" + channelReceiptString(t, created, "id")
	token, role := createChannelHTTPKey(t, f.project, "viewer")
	for _, name := range []string{"viewer", "operator"} {
		role.Role = name
		_, err := f.project.Store.Identity().SetOrgAPIKeyProjectRole(t.Context(), role)
		require.NoError(t, err)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, f.path,
			workflowHTTPJSON(t, f.profileSchedule("unauthorized-schedule")), "unauthorized-schedule",
			http.StatusForbidden, authHeaders(token))
		for _, bindings := range [][]openapi.AttachAgentChannelRequest{f.bindings(), {}} {
			requestJSONWithHeaders(t, f.handler, http.MethodPatch, path,
				workflowHTTPJSON(t, map[string]any{"channel_bindings": bindings}), "", http.StatusForbidden, authHeaders(token))
		}
		loaded := requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, authHeaders(token))
		require.Equal(t, created, loaded)
		listed := requestJSONWithHeaders(t, f.handler, http.MethodGet, f.path, "", "", http.StatusOK, authHeaders(token))
		require.Equal(t, []any{created}, listed["data"])
	}
	role.Role = "developer"
	_, err := f.project.Store.Identity().SetOrgAPIKeyProjectRole(t.Context(), role)
	require.NoError(t, err)
	managed := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.path,
		workflowHTTPJSON(t, f.profileSchedule("unauthorized-schedule")), "unauthorized-schedule",
		http.StatusCreated, authHeaders(token))
	require.Equal(t, f.publicBindings(), managed["channel_bindings"])
	cleared := requestJSONWithHeaders(t, f.handler, http.MethodPatch, path,
		`{"channel_bindings":[]}`, "", http.StatusOK, authHeaders(token))
	require.Equal(t, []any{}, cleared["channel_bindings"])
	require.Equal(t, cleared, f.request(t, http.MethodGet, path, nil, "", http.StatusOK))
}

type cronChannelHTTPFixture struct {
	handler                              http.Handler
	project                              publicHTTPProject
	profileID, configID, channelID, path string
}

func newCronChannelHTTPFixture(t *testing.T) cronChannelHTTPFixture {
	t.Helper()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "cron-channels")
	parent := createExternalRequestHTTPChannel(t, handler, project, "scheduled-parent")
	profile := createPublicHTTPAgent(t, handler, project, "scheduled-profile", project.AdminToken)
	config := testutil.RequireType[map[string]any](t, profile["current_config"])
	return cronChannelHTTPFixture{
		handler: handler, project: project, path: project.ProjectPath + "/cron-triggers",
		channelID: testPublicID(t, publicid.KindIntegrationTarget, parent.ID),
		profileID: channelReceiptString(t, profile, "id"), configID: channelReceiptString(t, config, "id"),
	}
}

func (f cronChannelHTTPFixture) bindings() []openapi.AttachAgentChannelRequest {
	return []openapi.AttachAgentChannelRequest{{
		ChannelId:          f.channelID,
		Grants:             openapi.ChannelGrants{Send: true},
		ReplyChannelGrants: &openapi.ChannelGrants{Receive: true, Send: true},
	}}
}

func (f cronChannelHTTPFixture) publicBindings() []any {
	return []any{map[string]any{
		"channel_id":           f.channelID,
		"grants":               map[string]any{"receive": false, "read": false, "send": true},
		"reply_channel_grants": map[string]any{"receive": true, "read": false, "send": true},
	}}
}

func (f cronChannelHTTPFixture) profileSchedule(name string) map[string]any {
	return map[string]any{
		"name": name, "target": map[string]any{"type": "profile", "agent_profile_id": f.profileID},
		"cron": "0 9 * * *", "message_template": "Send the daily report.", "channel_bindings": f.bindings(),
	}
}

func (f cronChannelHTTPFixture) request(
	t *testing.T, method, path string, body any, idempotencyKey string, status int,
) map[string]any {
	t.Helper()
	var raw string
	if body != nil {
		raw = workflowHTTPJSON(t, body)
	}
	return requestJSONWithHeaders(t, f.handler, method, path, raw, idempotencyKey, status,
		authHeaders(f.project.AdminToken))
}

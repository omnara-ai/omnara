//go:build integration

package httpapi

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func subscriptionHTTPAgent(t *testing.T, f integrationLaunchHTTPFixture) string {
	t.Helper()
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectIntegrationHTTPJSON(t, map[string]any{"config": f.configID, "name": "Subscribed agent"}),
		"subscription-agent", http.StatusCreated, authHeaders(f.project.AdminToken))
	return testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
}

func subscriptionHTTPBody(agentID, channel string) map[string]any {
	return map[string]any{
		"agent_id": agentID, "conversation": map[string]any{"channel_id": channel},
	}
}

func TestIntegrationSubscriptionsHTTPPaginationAndDetachReplay(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "subscription-pages")
	agentID := subscriptionHTTPAgent(t, f)
	path := f.project.ProjectPath + "/integrations/" + f.integrationID + "/subscriptions"
	headers := authHeaders(f.project.AdminToken)
	create := func(channel string) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
			projectIntegrationHTTPJSON(t, subscriptionHTTPBody(agentID, channel)), "", http.StatusCreated, headers)
	}
	first := create("C100")
	firstID := testutil.RequireType[string](t, first["id"])
	mustPublicHTTPID(t, publicid.KindIntegrationSubscription, firstID)
	require.Equal(t, f.project.ProjectID, first["project_id"])
	require.Equal(t, f.integrationID, first["integration_id"])
	require.Equal(t, agentID, first["agent_id"])
	require.Equal(t, "Subscribed agent", first["agent_name"])
	require.Equal(t, map[string]any{"channel_id": "C100"}, first["conversation"])
	_, err := time.Parse(time.RFC3339Nano, testutil.RequireType[string](t, first["created_at"]))
	require.NoError(t, err)
	require.Equal(t, first, create("C100"), "repeated attach preserves identity and timestamps")
	second, third := create("C200"), create("C300")
	page := requestJSONWithHeaders(t, f.handler, http.MethodGet, path+"?limit=2", "", "", http.StatusOK, headers)
	data := testutil.RequireType[[]any](t, page["data"])
	require.Equal(t, []any{third, second}, data)
	cursor := testutil.RequireType[string](t, page["next_cursor"])
	decoded, err := decodeKeysetCursor(cursor)
	require.NoError(t, err)
	mustPublicHTTPID(t, publicid.KindIntegrationSubscription, decoded.ID)
	last := requestJSONWithHeaders(t, f.handler, http.MethodGet, path+"?limit=2&cursor="+url.QueryEscape(cursor),
		"", "", http.StatusOK, headers)
	require.Equal(t, []any{first}, last["data"])
	require.Nil(t, last["next_cursor"])
	wrongKind, err := encodeKeysetCursor(time.Now(), agentID)
	require.NoError(t, err)
	for _, query := range []string{
		"?limit=0", "?limit=101", "?limit=garbage", "?cursor=bad", "?cursor=" + url.QueryEscape(wrongKind),
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodGet, path+query, "", "", http.StatusBadRequest, headers)
	}
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path+"/"+firstID, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path+"/"+firstID, "", "", http.StatusNoContent, headers)
	replacement := create("C100")
	require.NotEqual(t, firstID, replacement["id"])
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path+"/"+firstID, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+
		"/integrations/"+f.integrationID+"/disconnect",
		"", "", http.StatusOK, headers)
	page = requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.ElementsMatch(t, []any{replacement, second, third}, page["data"],
		"disconnect preserves subscriptions and old DELETE cannot revoke reattachment")
	for _, entry := range []map[string]any{replacement, second, third} {
		requestJSONWithHeaders(t, f.handler, http.MethodDelete, path+"/"+testutil.RequireType[string](t, entry["id"]),
			"", "", http.StatusNoContent, headers)
	}
	page = requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Equal(t, []any{}, page["data"])
	require.Nil(t, page["next_cursor"])
	agent := requestJSONWithHeaders(t, f.handler, http.MethodGet, f.project.ProjectPath+"/agents/"+agentID,
		"", "", http.StatusOK, headers)
	require.Equal(t, f.configID, testutil.RequireType[map[string]any](t, agent["agent"])["current_config_id"],
		"attachment changes preserve the pinned config")
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, f.project.ProjectPath+"/integrations/"+f.integrationID,
		"", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusNotFound, headers)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path+"/"+firstID, "", "", http.StatusNotFound, headers)
}

func TestIntegrationSubscriptionsHTTPPermissionsAndIsolation(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "subscription-auth")
	agentID := subscriptionHTTPAgent(t, f)
	path := f.project.ProjectPath + "/integrations/" + f.integrationID + "/subscriptions"
	body := projectIntegrationHTTPJSON(t, subscriptionHTTPBody(agentID, "C123"))
	headers := authHeaders(f.project.AdminToken)
	attached := requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusCreated, headers)
	id := testutil.RequireType[string](t, attached["id"])
	operations := []struct{ method, path, body string }{
		{http.MethodGet, path, ""}, {http.MethodPost, path, body}, {http.MethodDelete, path + "/" + id, ""},
	}
	_, unassigned := createHTTPOrgMemberToken(t, t.Context(), integrationPoolForHandler(t, f.handler),
		f.project.Store, f.project.OrgUUID, "subscription-unassigned")
	for _, op := range operations {
		requestJSONWithHeaders(t, f.handler, op.method, op.path, op.body, "", http.StatusUnauthorized, nil)
		requestJSONWithHeaders(t, f.handler, op.method, op.path, op.body, "", http.StatusNotFound, authHeaders(unassigned))
	}
	for _, role := range []string{"viewer", "operator"} {
		token := customIntegrationHTTPKey(t, f.handler, f.project, "subscription-"+role, role)
		for _, op := range operations {
			status := http.StatusForbidden
			if op.method == http.MethodGet {
				status = http.StatusOK
			}
			requestJSONWithHeaders(t, f.handler, op.method, op.path, op.body, "", status, authHeaders(token))
		}
	}
	other := projectIntegrationHTTPSecondProject(t, f.handler, f.project)
	otherIntegration := createSlackHTTPIntegration(t, t.Context(), other, "AOTHER", "TOTHER", "Other")
	otherIntegrationID := testPublicID(t, publicid.KindProjectIntegration, otherIntegration.ID)
	otherPath := other.ProjectPath + "/integrations/" + otherIntegrationID + "/subscriptions"
	for _, prefix := range []string{
		other.ProjectPath + "/integrations/" + f.integrationID, f.project.ProjectPath + "/integrations/" + otherIntegrationID,
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			foreignPath, requestBody := prefix+"/subscriptions", ""
			if method == http.MethodPost {
				requestBody = body
			}
			if method == http.MethodDelete {
				foreignPath += "/" + id
			}
			requestJSONWithHeaders(t, f.handler, method, foreignPath, requestBody, "", http.StatusNotFound, headers)
		}
	}
	requestJSONWithHeaders(t, f.handler, http.MethodPost, otherPath, body, "", http.StatusNotFound, headers)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, otherPath+"/"+id, "", "", http.StatusNoContent, headers)
	secondIntegration := createSlackHTTPIntegration(t, t.Context(), f.project, "ASECOND", "TSECOND", "Second")
	secondID := testPublicID(t, publicid.KindProjectIntegration, secondIntegration.ID)
	secondPath := f.project.ProjectPath + "/integrations/" + secondID + "/subscriptions"
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, secondPath+"/"+id, "", "", http.StatusNoContent, headers)
	for _, emptyPath := range []string{otherPath, secondPath} {
		page := requestJSONWithHeaders(t, f.handler, http.MethodGet, emptyPath, "", "", http.StatusOK, headers)
		require.Empty(t, page["data"])
	}
	page := requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Equal(t, []any{attached}, page["data"], "foreign integration DELETE cannot delete a scoped subscription")
}

func TestIntegrationSubscriptionsHTTPValidation(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "subscription-invalid")
	agentID := subscriptionHTTPAgent(t, f)
	path := f.project.ProjectPath + "/integrations/" + f.integrationID + "/subscriptions"
	headers := authHeaders(f.project.AdminToken)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing-agent", func(b map[string]any) { delete(b, "agent_id") }},
		{"raw-agent-id", func(b map[string]any) { b["agent_id"] = uuid.NewString() }},
		{"wrong-id-kind", func(b map[string]any) { b["agent_id"] = f.integrationID }},
		{"unexpected-property", func(b map[string]any) { b["unexpected"] = true }},
		{"empty-conversation", func(b map[string]any) { b["conversation"] = map[string]any{} }},
		{"wrong-provider", func(b map[string]any) {
			b["conversation"] = map[string]any{"repository_id": 1, "pull_request": 2}
		}},
		{"unknown-conversation-field", func(b map[string]any) {
			b["conversation"] = map[string]any{"channel_id": "C123", "state": map[string]any{}}
		}},
		{"missing-conversation", func(b map[string]any) { delete(b, "conversation") }},
	} {
		t.Logf("invalid subscription case: %s", tc.name)
		body := subscriptionHTTPBody(agentID, "C123")
		tc.edit(body)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
			projectIntegrationHTTPJSON(t, body), "", http.StatusBadRequest, headers)
	}
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path+"/"+agentID, "", "", http.StatusBadRequest, headers)
	requestJSONWithHeaders(t, f.handler, http.MethodGet,
		f.project.ProjectPath+"/integrations/"+agentID+"/subscriptions", "", "", http.StatusBadRequest, headers)
	page := requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Empty(t, page["data"])
}

func TestIntegrationSubscriptionsHTTPGitHubAddressReplay(t *testing.T) {
	t.Parallel()
	f := newGitHubHTTPJourney(t, "subscription-github-address")
	config := createPublicHTTPAgentConfig(t, f.handler, f.project, "subscription-config", "json",
		projectIntegrationHTTPJSON(t, projectIntegrationHTTPSource(nil)), f.project.AdminToken, http.StatusCreated)
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		projectIntegrationHTTPJSON(t, map[string]any{"config": config["id"]}),
		"subscription-agent", http.StatusCreated, authHeaders(f.project.AdminToken))
	agentID := testutil.RequireType[map[string]any](t, launched["agent"])["id"]
	path := f.project.ProjectPath +
		"/integrations/" + testPublicID(t, publicid.KindProjectIntegration, f.integration.ID) + "/subscriptions"
	body := map[string]any{
		"agent_id":     agentID,
		"conversation": map[string]any{"repository_id": int64(9007199254740993), "pull_request": 42},
	}
	headers := authHeaders(f.project.AdminToken)
	first := requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
		projectIntegrationHTTPJSON(t, body), "", http.StatusCreated, headers)
	var ref string
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT scope_ref FROM integration_subscriptions WHERE id=$1`,
		mustPublicHTTPID(t, publicid.KindIntegrationSubscription, testutil.RequireType[string](t, first["id"]))).Scan(&ref))
	require.Equal(t, "9007199254740993#42", ref, "provider IDs must not round through float64 at the HTTP boundary")
	duplicate := requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
		projectIntegrationHTTPJSON(t, body), "", http.StatusCreated, headers)
	require.Equal(t, first, duplicate)
	page := requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Equal(t, []any{first}, page["data"], "repeated attachment preserves a single routing subscription")
}

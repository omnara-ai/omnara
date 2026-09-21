//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func projectAppHTTPJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func projectAppHTTPBody(name, appType string) map[string]any {
	return map[string]any{"name": name, "app_type": appType, "settings": map[string]any{}}
}

func projectAppHTTPSource(capabilities map[string]any) map[string]any {
	source := map[string]any{
		"instruction": "Help with this project app.",
		"model":       map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
	}
	for key, value := range capabilities {
		source[key] = value
	}
	return source
}

func projectAppHTTPSecondProject(t *testing.T, handler http.Handler, project publicHTTPProject) publicHTTPProject {
	t.Helper()
	created := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects", `{"name":"Other app project"}`, "",
		http.StatusCreated, authHeaders(project.AdminToken))
	second := project
	second.ProjectID = testutil.RequireType[string](t, created["id"])
	second.ProjectUUID = mustPublicHTTPID(t, publicid.KindProject, second.ProjectID)
	second.ProjectPath = "/api/v1/orgs/" + second.OrgID + "/projects/" + second.ProjectID
	grantDefaultPublicHTTPModelToProject(t, handler, project, second.ProjectID, project.AdminToken)
	return second
}

func TestProjectAppHTTPCRUDAndPagination(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "app-crud")
	headers, path := authHeaders(project.AdminToken), project.ProjectPath+"/apps"
	body := projectAppHTTPBody("Alpha", "slack_thread")
	create := func(status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodPost, path,
			projectAppHTTPJSON(t, body), "", status, headers)
	}
	alpha := create(http.StatusCreated)
	alphaID := testutil.RequireType[string](t, alpha["id"])
	mustPublicHTTPID(t, publicid.KindProjectApp, alphaID)
	require.Equal(t, project.ProjectID, alpha["project_id"])
	require.Equal(t, "disconnected", alpha["state"])
	require.Equal(t, "slack_thread", alpha["app_type"])
	require.Equal(t, body["settings"], alpha["settings"])
	for _, key := range []string{"created_at", "updated_at"} {
		_, err := time.Parse(time.RFC3339Nano, testutil.RequireType[string](t, alpha[key]))
		require.NoError(t, err)
	}
	fetched := requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusOK, headers)
	require.Equal(t, alpha, fetched)
	create(http.StatusConflict)
	body["name"] = "Beta"
	beta := create(http.StatusCreated)
	body["name"] = "Gamma"
	gamma := create(http.StatusCreated)
	page := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=2", "", "", http.StatusOK, headers)
	data := testutil.RequireType[[]any](t, page["data"])
	require.Len(t, data, 2)
	require.Equal(t, gamma["id"], testutil.RequireType[map[string]any](t, data[0])["id"])
	require.Equal(t, beta["id"], testutil.RequireType[map[string]any](t, data[1])["id"])
	cursor := testutil.RequireType[string](t, page["next_cursor"])
	require.NotEmpty(t, cursor)
	last := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=2&cursor="+url.QueryEscape(cursor),
		"", "", http.StatusOK, headers)
	require.Len(t, testutil.RequireType[[]any](t, last["data"]), 1)
	require.Nil(t, last["next_cursor"])
	for _, edit := range []map[string]any{
		projectAppHTTPBody("Renamed", "slack_thread"), projectAppHTTPBody("Alpha", "discord_thread"),
	} {
		requestJSONWithHeaders(t, handler, http.MethodPut, path+"/"+alphaID,
			projectAppHTTPJSON(t, edit), "", http.StatusBadRequest, headers)
	}
	body = projectAppHTTPBody("Alpha", "slack_thread")
	updated := requestJSONWithHeaders(t, handler, http.MethodPut, path+"/"+alphaID,
		projectAppHTTPJSON(t, body), "", http.StatusOK, headers)
	require.Equal(t, alpha["setup_revision"], updated["setup_revision"], "metadata edits do not restart provider sessions")
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	replacement := create(http.StatusCreated)
	require.NotEqual(t, alphaID, replacement["id"], "name reuse creates a distinct identity")
}

func TestProjectAppHTTPManagementAuthorizationAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-auth")
	path := project.ProjectPath + "/apps"
	body := projectAppHTTPJSON(t, projectAppHTTPBody("Managed", "slack_thread"))
	app := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "",
		http.StatusCreated, authHeaders(project.AdminToken))
	id := testutil.RequireType[string](t, app["id"])
	operations := []struct{ method, path, body string }{
		{http.MethodGet, path, ""}, {http.MethodGet, path + "/" + id, ""},
		{http.MethodGet, project.ProjectPath + "/app-definitions", ""},
		{http.MethodPost, path, body}, {http.MethodPut, path + "/" + id, body}, {http.MethodDelete, path + "/" + id, ""},
	}
	_, unassigned := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "app-unassigned")
	for _, op := range operations {
		requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", http.StatusUnauthorized, nil)
		requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", http.StatusNotFound, authHeaders(unassigned))
	}
	for _, role := range []string{"viewer", "operator"} {
		user, token := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "app-"+role)
		_, err := project.Store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: user.ID, Role: role,
		})
		require.NoError(t, err)
		for _, op := range operations {
			status := http.StatusForbidden
			if op.method == http.MethodGet {
				status = http.StatusOK
			}
			requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", status, authHeaders(token))
		}
	}
	other := projectAppHTTPSecondProject(t, handler, project)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		requestBody := ""
		if method == http.MethodPut {
			requestBody = body
		}
		requestJSONWithHeaders(t, handler, method, other.ProjectPath+"/apps/"+id, requestBody, "",
			http.StatusNotFound, authHeaders(project.AdminToken))
	}
	list := requestJSONWithHeaders(t, handler, http.MethodGet, other.ProjectPath+"/apps", "", "",
		http.StatusOK, authHeaders(project.AdminToken))
	require.Empty(t, testutil.RequireType[[]any](t, list["data"]))
}

func TestProjectAppHTTPCatalogAndValidation(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "app-catalog")
	headers := authHeaders(project.AdminToken)
	catalog := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/app-definitions",
		"", "", http.StatusOK, headers)
	definitions := testutil.RequireType[[]any](t, catalog["data"])
	require.Len(t, definitions, 3)
	var appTypes []string
	for _, value := range definitions {
		definition := testutil.RequireType[map[string]any](t, value)
		appTypes = append(appTypes, testutil.RequireType[string](t, definition["app_type"]))
		require.Contains(t, definition, "capabilities")
	}
	require.ElementsMatch(t, []string{"slack_thread", "discord_thread", "github_pr"}, appTypes)
	for _, name := range []string{"", "space name", "a__b", "1bot", "abcdefghijklmnopqrstuvwxyz1234567"} {
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/apps",
			projectAppHTTPJSON(t, projectAppHTTPBody(name, "slack_thread")), "", http.StatusBadRequest, headers)
	}
	for _, unknown := range []string{"", "slack", "slack_unregistered", "customer.external"} {
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/apps",
			projectAppHTTPJSON(t, projectAppHTTPBody("unknown", unknown)), "", http.StatusBadRequest, headers)
	}
}

func TestProjectAppHTTPCompiledIdentitySurvivesNameReuse(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	handler := newIntegrationServer(openIntegrationDB(t, ctx))
	project := bootstrapPublicHTTPProject(t, handler, "app-pinned")
	app := createSlackHTTPApp(t, ctx, project, "A123", "T123", "Support")
	name := "app__" + app.Name + "__read"
	source := projectAppHTTPJSON(t, projectAppHTTPSource(map[string]any{
		"tools":                map[string]any{name: map[string]any{}},
		"interaction_handlers": map[string]any{app.Name: map[string]any{}},
	}))
	config := createPublicHTTPAgentConfig(t, handler, project, "pinned", "json", source,
		project.AdminToken, http.StatusCreated)
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, testutil.RequireType[string](t, config["id"]))
	stored, found, err := project.Store.Execution().GetAgentConfig(ctx, project.ProjectUUID, configID)
	require.NoError(t, err)
	require.True(t, found)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(stored.CompiledDefinition, &compiled))
	appID := testPublicID(t, publicid.KindProjectApp, app.ID)
	require.Equal(t, appID, compiled.Tools[name].AppID)
	require.Equal(t, appID, compiled.InteractionHandlers[app.Name].AppID)
	requestJSONWithHeaders(t, handler, http.MethodDelete, project.ProjectPath+"/apps/"+appID,
		"", "", http.StatusNoContent, authHeaders(project.AdminToken))
	replacement := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/apps",
		projectAppHTTPJSON(t, projectAppHTTPBody(app.Name, "slack_thread")), "",
		http.StatusCreated, authHeaders(project.AdminToken))
	require.NotEqual(t, appID, replacement["id"])
	after, found, err := project.Store.Execution().GetAgentConfig(ctx, project.ProjectUUID, configID)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, string(stored.CompiledDefinition), string(after.CompiledDefinition))
	createPublicHTTPAgentConfig(t, handler, project, "disconnected", "json", source,
		project.AdminToken, http.StatusCreated)
	other := projectAppHTTPSecondProject(t, handler, project)
	createPublicHTTPAgentConfig(t, handler, other, "foreign", "json", source, project.AdminToken, http.StatusBadRequest)
}

func TestProjectAppHTTPLauncherReferencesAreProjectScoped(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "app-launcher")
	other := projectAppHTTPSecondProject(t, handler, project)
	own := createPublicHTTPAgent(t, handler, project, "own", project.AdminToken)
	foreign := createPublicHTTPAgent(t, handler, other, "foreign", project.AdminToken)
	for _, tc := range []struct {
		profile any
		status  int
	}{
		{foreign["id"], http.StatusNotFound}, {own["id"], http.StatusCreated},
	} {
		body := projectAppHTTPBody("reviewer", "slack_thread")
		body["settings"] = map[string]any{"launcher": map[string]any{
			"trigger": "mention", "scope_kind": "workspace", "scope_ref": "T123",
			"slots": []any{map[string]any{"key": "default", "agent_profile_id": tc.profile}},
		}}
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/apps",
			projectAppHTTPJSON(t, body), "", tc.status, authHeaders(project.AdminToken))
	}
}

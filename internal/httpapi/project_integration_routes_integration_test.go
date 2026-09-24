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

func projectIntegrationHTTPJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func projectIntegrationHTTPBody(name, integrationType string) map[string]any {
	return map[string]any{"name": name, "integration_type": integrationType, "settings": map[string]any{}}
}

func projectIntegrationHTTPSource(capabilities map[string]any) map[string]any {
	source := map[string]any{
		"instruction": "Help with this project integration.",
		"model":       map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
	}
	for key, value := range capabilities {
		source[key] = value
	}
	return source
}

func projectIntegrationHTTPSecondProject(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
) publicHTTPProject {
	t.Helper()
	created := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects", `{"name":"Other integration project"}`, "",
		http.StatusCreated, authHeaders(project.AdminToken))
	second := project
	second.ProjectID = testutil.RequireType[string](t, created["id"])
	second.ProjectUUID = mustPublicHTTPID(t, publicid.KindProject, second.ProjectID)
	second.ProjectPath = "/api/v1/orgs/" + second.OrgID + "/projects/" + second.ProjectID
	grantDefaultPublicHTTPModelToProject(t, handler, project, second.ProjectID, project.AdminToken)
	return second
}

func TestProjectIntegrationHTTPCRUDAndPagination(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "integration-crud")
	headers, path := authHeaders(project.AdminToken), project.ProjectPath+"/integrations"
	body := projectIntegrationHTTPBody("Alpha", "slack_thread")
	create := func(status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodPost, path,
			projectIntegrationHTTPJSON(t, body), "", status, headers)
	}
	alpha := create(http.StatusCreated)
	alphaID := testutil.RequireType[string](t, alpha["id"])
	mustPublicHTTPID(t, publicid.KindProjectIntegration, alphaID)
	require.Equal(t, project.ProjectID, alpha["project_id"])
	require.Equal(t, "disconnected", alpha["state"])
	require.Equal(t, "slack_thread", alpha["integration_type"])
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
		projectIntegrationHTTPBody("Renamed", "slack_thread"), projectIntegrationHTTPBody("Alpha", "discord_thread"),
	} {
		requestJSONWithHeaders(t, handler, http.MethodPut, path+"/"+alphaID,
			projectIntegrationHTTPJSON(t, edit), "", http.StatusBadRequest, headers)
	}
	body = projectIntegrationHTTPBody("Alpha", "slack_thread")
	updated := requestJSONWithHeaders(t, handler, http.MethodPut, path+"/"+alphaID,
		projectIntegrationHTTPJSON(t, body), "", http.StatusOK, headers)
	require.Equal(t, alpha["setup_revision"], updated["setup_revision"], "metadata edits do not restart provider sessions")
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	replacement := create(http.StatusCreated)
	require.NotEqual(t, alphaID, replacement["id"], "name reuse creates a distinct identity")
}

func TestProjectIntegrationHTTPManagementAuthorizationAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "integration-auth")
	path := project.ProjectPath + "/integrations"
	body := projectIntegrationHTTPJSON(t, projectIntegrationHTTPBody("Managed", "slack_thread"))
	integration := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "",
		http.StatusCreated, authHeaders(project.AdminToken))
	id := testutil.RequireType[string](t, integration["id"])
	operations := []struct{ method, path, body string }{
		{http.MethodGet, path, ""}, {http.MethodGet, path + "/" + id, ""},
		{http.MethodGet, project.ProjectPath + "/integration-definitions", ""},
		{http.MethodPost, path, body}, {http.MethodPut, path + "/" + id, body}, {http.MethodDelete, path + "/" + id, ""},
	}
	_, unassigned := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "integration-unassigned")
	for _, op := range operations {
		requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", http.StatusUnauthorized, nil)
		requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", http.StatusNotFound, authHeaders(unassigned))
	}
	for _, role := range []string{"viewer", "operator"} {
		user, token := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "integration-"+role)
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
	other := projectIntegrationHTTPSecondProject(t, handler, project)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		requestBody := ""
		if method == http.MethodPut {
			requestBody = body
		}
		requestJSONWithHeaders(t, handler, method, other.ProjectPath+"/integrations/"+id, requestBody, "",
			http.StatusNotFound, authHeaders(project.AdminToken))
	}
	list := requestJSONWithHeaders(t, handler, http.MethodGet, other.ProjectPath+"/integrations", "", "",
		http.StatusOK, authHeaders(project.AdminToken))
	require.Empty(t, testutil.RequireType[[]any](t, list["data"]))
}

func TestProjectIntegrationHTTPCatalogAndValidation(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "integration-catalog")
	headers := authHeaders(project.AdminToken)
	catalog := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/integration-definitions",
		"", "", http.StatusOK, headers)
	definitions := testutil.RequireType[[]any](t, catalog["data"])
	require.Len(t, definitions, 3)
	var integrationTypes []string
	for _, value := range definitions {
		definition := testutil.RequireType[map[string]any](t, value)
		integrationTypes = append(integrationTypes, testutil.RequireType[string](t, definition["integration_type"]))
		require.Contains(t, definition, "capabilities")
	}
	require.ElementsMatch(t, []string{"slack_thread", "discord_thread", "github_pr"}, integrationTypes)
	for _, name := range []string{"", "space name", "a__b", "1bot", "abcdefghijklmnopqrstuvwxyz1234567"} {
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
			projectIntegrationHTTPJSON(t, projectIntegrationHTTPBody(name, "slack_thread")), "", http.StatusBadRequest, headers)
	}
	for _, unknown := range []string{"", "slack", "slack_unregistered", "customer.external"} {
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
			projectIntegrationHTTPJSON(t, projectIntegrationHTTPBody("unknown", unknown)), "", http.StatusBadRequest, headers)
	}
}

func TestProjectIntegrationHTTPCompiledIdentitySurvivesNameReuse(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	handler := newIntegrationServer(openIntegrationDB(t, ctx))
	project := bootstrapPublicHTTPProject(t, handler, "integration-pinned")
	integration := createSlackHTTPIntegration(t, ctx, project, "A123", "T123", "Support")
	name := "int__" + integration.Name + "__read"
	source := projectIntegrationHTTPJSON(t, projectIntegrationHTTPSource(map[string]any{
		"tools":                map[string]any{name: map[string]any{}},
		"interaction_handlers": map[string]any{integration.Name: map[string]any{}},
	}))
	config := createPublicHTTPAgentConfig(t, handler, project, "pinned", "json", source,
		project.AdminToken, http.StatusCreated)
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, testutil.RequireType[string](t, config["id"]))
	stored, found, err := project.Store.Execution().GetAgentConfig(ctx, project.ProjectUUID, configID)
	require.NoError(t, err)
	require.True(t, found)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(stored.CompiledDefinition, &compiled))
	integrationID := testPublicID(t, publicid.KindProjectIntegration, integration.ID)
	require.Equal(t, integration.ID, compiled.Tools[name].IntegrationID)
	require.Equal(t, integration.ID, compiled.InteractionHandlers[integration.Name].IntegrationID)
	publicCompiled := testutil.RequireType[map[string]any](t, config["compiled_definition"])
	publicTools := testutil.RequireType[map[string]any](t, publicCompiled["tools"])
	publicHandlers := testutil.RequireType[map[string]any](t, publicCompiled["interaction_handlers"])
	require.Equal(t, integrationID, testutil.RequireType[map[string]any](t, publicTools[name])["integration_id"])
	require.Equal(
		t,
		integrationID,
		testutil.RequireType[map[string]any](t, publicHandlers[integration.Name])["integration_id"],
	)
	requestJSONWithHeaders(t, handler, http.MethodDelete, project.ProjectPath+"/integrations/"+integrationID,
		"", "", http.StatusNoContent, authHeaders(project.AdminToken))
	replacement := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
		projectIntegrationHTTPJSON(t, projectIntegrationHTTPBody(integration.Name, "slack_thread")), "",
		http.StatusCreated, authHeaders(project.AdminToken))
	require.NotEqual(t, integrationID, replacement["id"])
	after, found, err := project.Store.Execution().GetAgentConfig(ctx, project.ProjectUUID, configID)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, string(stored.CompiledDefinition), string(after.CompiledDefinition))
	fetched := requestJSONWithHeaders(t, handler, http.MethodGet,
		project.ProjectPath+"/agent-configs/"+testPublicID(t, publicid.KindAgentConfig, configID),
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	require.Equal(t, publicCompiled, fetched["compiled_definition"])
	createPublicHTTPAgentConfig(t, handler, project, "disconnected", "json", source,
		project.AdminToken, http.StatusCreated)
	other := projectIntegrationHTTPSecondProject(t, handler, project)
	createPublicHTTPAgentConfig(t, handler, other, "foreign", "json", source, project.AdminToken, http.StatusBadRequest)
}

func TestProjectIntegrationHTTPLauncherReferencesAreProjectScoped(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "integration-launcher")
	other := projectIntegrationHTTPSecondProject(t, handler, project)
	own := createPublicHTTPAgent(t, handler, project, "own", project.AdminToken)
	foreign := createPublicHTTPAgent(t, handler, other, "foreign", project.AdminToken)
	for _, tc := range []struct {
		profile any
		status  int
	}{
		{foreign["id"], http.StatusNotFound}, {own["id"], http.StatusCreated},
	} {
		body := projectIntegrationHTTPBody("reviewer", "slack_thread")
		body["settings"] = map[string]any{"launcher": map[string]any{
			"trigger": "mention", "scope_kind": "workspace", "scope_ref": "T123",
			"slots": []any{map[string]any{"key": "default", "agent_profile_id": tc.profile}},
		}}
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
			projectIntegrationHTTPJSON(t, body), "", tc.status, authHeaders(project.AdminToken))
	}
}

func TestProjectIntegrationHTTPDiscordLaunchesAcrossServers(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "discord-server-launcher")
	profile := createPublicHTTPAgent(t, handler, project, "discord", project.AdminToken)
	launcher := map[string]any{
		"trigger": "mention",
		"slots":   []any{map[string]any{"key": "default", "agent_profile_id": profile["id"]}},
	}
	body := projectIntegrationHTTPBody("discord", "discord_thread")
	body["settings"] = map[string]any{"launcher": launcher}
	integration := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
		projectIntegrationHTTPJSON(t, body), "", http.StatusCreated, authHeaders(project.AdminToken))
	integrationPath := project.ProjectPath + "/integrations/" + testutil.RequireType[string](t, integration["id"])
	integration = requestJSONWithHeaders(t, handler, http.MethodPut, integrationPath,
		projectIntegrationHTTPJSON(t, body), "", http.StatusOK, authHeaders(project.AdminToken))
	savedLauncher := testutil.RequireType[map[string]any](t,
		testutil.RequireType[map[string]any](t, integration["settings"])["launcher"])
	require.NotContains(t, savedLauncher, "scope_kind")
	require.NotContains(t, savedLauncher, "scope_ref")
	for _, scope := range []map[string]any{
		{"scope_kind": "guild", "scope_ref": "123"},
		{"scope_kind": "channel", "scope_ref": "456"},
		{"scope_kind": "thread", "scope_ref": "456:789"},
		{"scope_kind": "guild"}, {"scope_ref": "123"}, {"scope_kind": " "},
		{"scope_kind": ""}, {"scope_ref": ""},
	} {
		delete(launcher, "scope_kind")
		delete(launcher, "scope_ref")
		for key, value := range scope {
			launcher[key] = value
		}
		for _, request := range []struct{ method, path string }{
			{http.MethodPost, project.ProjectPath + "/integrations"}, {http.MethodPut, integrationPath},
		} {
			requestJSONWithHeaders(t, handler, request.method, request.path,
				projectIntegrationHTTPJSON(t, body), "", http.StatusBadRequest, authHeaders(project.AdminToken))
		}
	}
	stored := requestJSONWithHeaders(t, handler, http.MethodGet, integrationPath,
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	require.Equal(t, integration["settings"], stored["settings"])
	delete(launcher, "scope_kind")
	delete(launcher, "scope_ref")
	for _, integrationType := range []string{"slack_thread", "github_pr"} {
		body["integration_type"] = integrationType
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
			projectIntegrationHTTPJSON(t, body), "", http.StatusBadRequest, authHeaders(project.AdminToken))
	}
}

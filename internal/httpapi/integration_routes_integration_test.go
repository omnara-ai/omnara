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

func integrationHTTPJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func integrationHTTPBody(name, integrationKind string) map[string]any {
	return map[string]any{"name": name, "integration_kind": integrationKind, "settings": map[string]any{}}
}

func integrationHTTPSource(capabilities map[string]any) map[string]any {
	source := map[string]any{
		"instruction": "Help with this project integration.",
		"model":       map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
	}
	for key, value := range capabilities {
		source[key] = value
	}
	return source
}

func integrationHTTPSecondProject(
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

func TestIntegrationHTTPCRUDAndPagination(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "integration-crud")
	headers, path := authHeaders(project.AdminToken), project.ProjectPath+"/integrations"
	body := integrationHTTPBody("Alpha", "slack_thread")
	create := func(status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodPost, path,
			integrationHTTPJSON(t, body), "", status, headers)
	}
	alpha := create(http.StatusCreated)
	alphaID := testutil.RequireType[string](t, alpha["id"])
	mustPublicHTTPID(t, publicid.KindIntegration, alphaID)
	require.Equal(t, project.ProjectID, alpha["project_id"])
	require.Equal(t, "disconnected", alpha["state"])
	require.Equal(t, "slack_thread", alpha["integration_kind"])
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
		integrationHTTPBody("Renamed", "slack_thread"), integrationHTTPBody("Alpha", "discord_thread"),
	} {
		requestJSONWithHeaders(t, handler, http.MethodPut, path+"/"+alphaID,
			integrationHTTPJSON(t, edit), "", http.StatusBadRequest, headers)
	}
	body = integrationHTTPBody("Alpha", "slack_thread")
	updated := requestJSONWithHeaders(t, handler, http.MethodPut, path+"/"+alphaID,
		`{"settings":{}}`, "", http.StatusOK, headers)
	require.Equal(t, alpha["setup_revision"], updated["setup_revision"], "metadata edits do not restart provider sessions")
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	replacement := create(http.StatusCreated)
	require.NotEqual(t, alphaID, replacement["id"], "name reuse creates a distinct identity")
}

func TestIntegrationHTTPManagementAuthorizationAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "integration-auth")
	path := project.ProjectPath + "/integrations"
	body := integrationHTTPJSON(t, integrationHTTPBody("Managed", "slack_thread"))
	integration := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "",
		http.StatusCreated, authHeaders(project.AdminToken))
	id := testutil.RequireType[string](t, integration["id"])
	operations := []struct{ method, path, body string }{
		{http.MethodGet, path, ""}, {http.MethodGet, path + "/" + id, ""},
		{http.MethodGet, project.ProjectPath + "/integration-definitions", ""},
		{http.MethodPost, path, body},
		{http.MethodPut, path + "/" + id, `{"settings":{}}`}, {http.MethodDelete, path + "/" + id, ""},
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
	other := integrationHTTPSecondProject(t, handler, project)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		requestBody := ""
		if method == http.MethodPut {
			requestBody = `{"settings":{}}`
		}
		requestJSONWithHeaders(t, handler, method, other.ProjectPath+"/integrations/"+id, requestBody, "",
			http.StatusNotFound, authHeaders(project.AdminToken))
	}
	list := requestJSONWithHeaders(t, handler, http.MethodGet, other.ProjectPath+"/integrations", "", "",
		http.StatusOK, authHeaders(project.AdminToken))
	require.Empty(t, testutil.RequireType[[]any](t, list["data"]))
}

func TestIntegrationHTTPCatalogAndValidation(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "integration-catalog")
	headers := authHeaders(project.AdminToken)
	catalog := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/integration-definitions",
		"", "", http.StatusOK, headers)
	definitions := testutil.RequireType[[]any](t, catalog["data"])
	require.Len(t, definitions, 3)
	var integrationKinds []string
	for _, value := range definitions {
		definition := testutil.RequireType[map[string]any](t, value)
		integrationKinds = append(integrationKinds, testutil.RequireType[string](t, definition["integration_kind"]))
		require.Contains(t, definition, "capabilities")
	}
	require.ElementsMatch(t, []string{"slack_thread", "discord_thread", "github_pr"}, integrationKinds)
	for _, name := range []string{"", "space name", "a__b", "1bot", "abcdefghijklmnopqrstuvwxyz1234567"} {
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
			integrationHTTPJSON(t, integrationHTTPBody(name, "slack_thread")), "", http.StatusBadRequest, headers)
	}
	for _, unknown := range []string{"", "slack", "slack_unregistered", "customer.external"} {
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
			integrationHTTPJSON(t, integrationHTTPBody("unknown", unknown)), "", http.StatusBadRequest, headers)
	}
}

func TestIntegrationHTTPCompiledIdentitySurvivesNameReuse(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	handler := newIntegrationServer(openIntegrationDB(t, ctx))
	project := bootstrapPublicHTTPProject(t, handler, "integration-pinned")
	integration := createSlackHTTPIntegration(t, ctx, project, "A123", "T123", "Support")
	name := "int__" + integration.Name + "__read"
	source := integrationHTTPJSON(t, integrationHTTPSource(map[string]any{
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
	integrationID := testPublicID(t, publicid.KindIntegration, integration.ID)
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
		integrationHTTPJSON(t, integrationHTTPBody(integration.Name, "slack_thread")), "",
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
	other := integrationHTTPSecondProject(t, handler, project)
	createPublicHTTPAgentConfig(t, handler, other, "foreign", "json", source, project.AdminToken, http.StatusBadRequest)
}

func TestIntegrationHTTPLauncherReferencesResolveAtRuntime(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "integration-launcher")
	other := integrationHTTPSecondProject(t, handler, project)
	own := createPublicHTTPAgent(t, handler, project, "own", project.AdminToken)
	foreign := createPublicHTTPAgent(t, handler, other, "foreign", project.AdminToken)
	for i, profile := range []any{foreign["id"], own["id"]} {
		name := "own"
		if i == 0 {
			name = "foreign-reference"
		}
		body := integrationHTTPBody(name, "slack_thread")
		body["settings"] = map[string]any{"launcher": map[string]any{"profiles": []any{profile}}}
		created := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
			integrationHTTPJSON(t, body), "", http.StatusCreated, authHeaders(project.AdminToken))
		require.Equal(t, body["settings"], created["settings"],
			"settings preserve public IDs; resources are authorized at launch")
	}
	ownID := mustPublicHTTPID(t, publicid.KindAgentProfile, testutil.RequireType[string](t, own["id"]))
	require.NoError(t, project.Store.Execution().DeleteAgentProfile(t.Context(), project.ProjectUUID, ownID))
}

func TestIntegrationHTTPDiscordLaunchesAcrossServers(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "discord-server-launcher")
	profile := createPublicHTTPAgent(t, handler, project, "discord", project.AdminToken)
	launcher := map[string]any{
		"profiles": []any{profile["id"]},
	}
	body := integrationHTTPBody("discord", "discord_thread")
	body["settings"] = map[string]any{"launcher": launcher}
	integration := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
		integrationHTTPJSON(t, body), "", http.StatusCreated, authHeaders(project.AdminToken))
	integrationPath := project.ProjectPath + "/integrations/" + testutil.RequireType[string](t, integration["id"])
	integration = requestJSONWithHeaders(t, handler, http.MethodPut, integrationPath,
		integrationHTTPJSON(t, map[string]any{"settings": body["settings"]}),
		"", http.StatusOK, authHeaders(project.AdminToken))
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
			requestBody := body
			if request.method == http.MethodPut {
				requestBody = map[string]any{"settings": body["settings"]}
			}
			requestJSONWithHeaders(t, handler, request.method, request.path,
				integrationHTTPJSON(t, requestBody), "", http.StatusBadRequest, authHeaders(project.AdminToken))
		}
	}
	stored := requestJSONWithHeaders(t, handler, http.MethodGet, integrationPath,
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	require.Equal(t, integration["settings"], stored["settings"])
	delete(launcher, "scope_kind")
	delete(launcher, "scope_ref")
	body["integration_kind"] = "github_pr"
	requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integrations",
		integrationHTTPJSON(t, body), "", http.StatusBadRequest, authHeaders(project.AdminToken))
}

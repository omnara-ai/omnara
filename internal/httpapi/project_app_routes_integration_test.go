//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func projectAppHTTPJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func projectAppHTTPBody(name string, resource map[string]any) map[string]any {
	return map[string]any{"name": name, "settings": map[string]any{"resource": resource}}
}

func projectAppHTTPSource(resource map[string]any) map[string]any {
	return map[string]any{
		"instruction":   "Help with this project app.",
		"model":         map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
		"app_resources": map[string]any{"support": resource},
	}
}

func projectAppHTTPConnection(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed string,
) (connectionID, profileID string) {
	t.Helper()
	profile := createPublicHTTPAgent(t, handler, project, seed, project.AdminToken)
	profileID = testutil.RequireType[string](t, profile["id"])
	connection := createSlackHTTPConnection(
		t,
		t.Context(),
		project,
		"A-"+seed,
		"T"+strings.ToUpper(strings.ReplaceAll(seed, "-", "")),
		seed,
	)
	return testPublicID(t, publicid.KindIntegrationConnection, connection.ID), profileID
}

func projectAppHTTPSecondProject(t *testing.T, handler http.Handler, project publicHTTPProject) publicHTTPProject {
	t.Helper()
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Other app project"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	second := project
	second.ProjectID = testutil.RequireType[string](t, created["id"])
	second.ProjectUUID = mustPublicHTTPID(t, publicid.KindProject, second.ProjectID)
	second.ProjectPath = "/api/v1/orgs/" + second.OrgID + "/projects/" + second.ProjectID
	grantDefaultPublicHTTPModelToProject(t, handler, project, second.ProjectID, project.AdminToken)
	return second
}

func TestProjectAppHTTPCRUDAndPagination(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-crud")
	connectionID, _ := projectAppHTTPConnection(t, handler, project, "app-crud-hosted")
	headers := authHeaders(project.AdminToken)
	path := project.ProjectPath + "/apps"
	body := projectAppHTTPBody("Alpha", map[string]any{"definition": "omnara.slack", "connection": connectionID})
	alpha := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	alphaID := testutil.RequireType[string](t, alpha["id"])
	mustPublicHTTPID(t, publicid.KindProjectApp, alphaID)
	require.Equal(t, project.ProjectID, alpha["project_id"])
	require.Equal(t, true, alpha["enabled"])
	require.Equal(t, body["settings"], alpha["settings"])
	for _, key := range []string{"created_at", "updated_at"} {
		_, err := time.Parse(time.RFC3339Nano, testutil.RequireType[string](t, alpha[key]))
		require.NoError(t, err)
	}
	fetched := requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusOK, headers)
	require.Equal(t, alpha, fetched)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusConflict,
		headers,
	)
	body["name"] = "Beta"
	beta := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	body["name"] = "Gamma"
	gamma := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	page := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=2", "", "", http.StatusOK, headers)
	data := testutil.RequireType[[]any](t, page["data"])
	require.Len(t, data, 2)
	require.Equal(t, gamma["id"], testutil.RequireType[map[string]any](t, data[0])["id"])
	require.Equal(t, beta["id"], testutil.RequireType[map[string]any](t, data[1])["id"])
	cursor := testutil.RequireType[string](t, page["next_cursor"])
	require.NotEmpty(t, cursor)
	last := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"?limit=2&cursor="+url.QueryEscape(cursor),
		"",
		"",
		http.StatusOK,
		headers,
	)
	lastData := testutil.RequireType[[]any](t, last["data"])
	require.Len(t, lastData, 1)
	require.Equal(t, alphaID, testutil.RequireType[map[string]any](t, lastData[0])["id"])
	require.Nil(t, last["next_cursor"])
	for _, query := range []string{"?limit=0", "?limit=101", "?cursor=invalid-cursor"} {
		requestJSONWithHeaders(t, handler, http.MethodGet, path+query, "", "", http.StatusBadRequest, headers)
	}
	body = projectAppHTTPBody(
		"Alpha renamed",
		map[string]any{
			"definition": "omnara.slack", "connection": connectionID,
			"scope": map[string]any{"slack": map[string]any{"channel_id": "C123"}},
		},
	)
	body["enabled"] = false
	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+alphaID,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(t, alphaID, updated["id"])
	require.Equal(t, alpha["created_at"], updated["created_at"])
	require.Equal(t, "Alpha renamed", updated["name"])
	require.Equal(t, false, updated["enabled"])
	require.Equal(t, body["settings"], updated["settings"])
	fetched = requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusOK, headers)
	require.Equal(t, updated, fetched)
	body["name"] = "Beta"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+alphaID,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusConflict,
		headers,
	)
	fetched = requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusOK, headers)
	require.Equal(t, "Alpha renamed", fetched["name"])
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+alphaID,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+alphaID, "", "", http.StatusNotFound, headers)
	listed := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Len(t, testutil.RequireType[[]any](t, listed["data"]), 2)
	body["name"] = "Alpha renamed"
	replacement := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	require.NotEqual(t, alphaID, replacement["id"])
}

func TestProjectAppHTTPManagementAuthorizationAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-auth")
	connectionID, _ := projectAppHTTPConnection(t, handler, project, "app-auth-hosted")
	path := project.ProjectPath + "/apps"
	body := projectAppHTTPJSON(t, projectAppHTTPBody("Managed setup", map[string]any{
		"definition": "omnara.slack", "connection": connectionID,
	}))
	app := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	id := testutil.RequireType[string](t, app["id"])
	operations := []struct{ method, path, body string }{
		{http.MethodGet, path, ""},
		{http.MethodGet, path + "/" + id, ""},
		{http.MethodPost, path, body},
		{http.MethodPut, path + "/" + id, body},
		{http.MethodDelete, path + "/" + id, ""},
	}
	for _, op := range operations {
		requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", http.StatusUnauthorized, nil)
	}
	_, unassignedToken := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "app-unassigned")
	for _, op := range operations {
		rejected := requestJSONWithHeaders(
			t,
			handler,
			op.method,
			op.path,
			op.body,
			"",
			http.StatusNotFound,
			authHeaders(unassignedToken),
		)
		require.Equal(t, "not_found", rejected["code"])
	}
	for _, role := range []string{"viewer", "operator"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			user, token := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "app-"+role)
			_, err := project.Store.Identity().AddProjectMembership(
				ctx,
				identitystore.AddProjectMembershipInput{
					OrgID:     project.OrgUUID,
					ProjectID: project.ProjectUUID,
					UserID:    user.ID,
					Role:      role,
				},
			)
			require.NoError(t, err)
			for _, op := range operations {
				status := http.StatusForbidden
				if op.method == http.MethodGet {
					status = http.StatusOK
				}
				response := requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", status, authHeaders(token))
				if status == http.StatusForbidden {
					require.Equal(t, "forbidden", response["code"])
				}
			}
			createPublicHTTPAgentConfig(
				t,
				handler,
				project,
				"reader-compile",
				"json",
				projectAppHTTPJSON(t, projectAppHTTPSource(map[string]any{"app_instance": id})),
				token,
				http.StatusForbidden,
			)
		})
	}
	second := projectAppHTTPSecondProject(t, handler, project)
	otherPath := second.ProjectPath + "/apps"
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		requestBody := ""
		if method == http.MethodPut {
			requestBody = body
		}
		requestJSONWithHeaders(
			t,
			handler,
			method,
			otherPath+"/"+id,
			requestBody,
			"",
			http.StatusNotFound,
			authHeaders(project.AdminToken),
		)
	}
	list := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		otherPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	require.Empty(t, testutil.RequireType[[]any](t, list["data"]))
	// An authorized manager of both projects still cannot resolve setup across them.
	denied := createPublicHTTPAgentConfig(
		t,
		handler,
		second,
		"cross-project-app",
		"json",
		projectAppHTTPJSON(t, projectAppHTTPSource(map[string]any{"app_instance": id})),
		project.AdminToken,
		http.StatusBadRequest,
	)
	require.Equal(t, "invalid_request", denied["code"])
	original := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"/"+id,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	require.Equal(t, app, original)
}

func TestProjectAppHTTPReferencedCompilationSurvivesSetupChanges(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-compiled")
	headers := authHeaders(project.AdminToken)
	path := project.ProjectPath + "/apps"
	connectionID, _ := projectAppHTTPConnection(t, handler, project, "app-compiled")
	ticket := map[string]any{
		"type":         "custom",
		"description":  "Read a support ticket.",
		"input_schema": map[string]any{"type": "object", "properties": map[string]any{"ticket": map[string]any{"type": "string"}}, "required": []any{"ticket"}, "additionalProperties": false},
	}
	resource := map[string]any{"definition": "omnara.slack", "connection": connectionID,
		"scope":    map[string]any{"slack": map[string]any{"channel_id": "C123", "thread_ts": "111.222"}},
		"tools":    map[string]any{"slack_read": map[string]any{}, "slack_post_message": map[string]any{}, "ticket_lookup": ticket},
		"listener": map[string]any{"events": []any{"message"}}, "interaction_handler": map[string]any{"definition": "omnara.slack.interactions"},
	}
	body := projectAppHTTPBody("Support", resource)
	app := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	appID := testutil.RequireType[string](t, app["id"])
	require.Equal(t, body["settings"], app["settings"])
	selected := map[string]any{
		"app_instance": appID,
		"scope":        map[string]any{"slack": map[string]any{"channel_id": "C456", "thread_ts": "222.333"}},
		"tools":        map[string]any{"slack_read": map[string]any{}, "ticket_lookup": map[string]any{}},
	}
	source := projectAppHTTPJSON(t, projectAppHTTPSource(selected))
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"app-compiled",
		"json",
		source,
		project.AdminToken,
		http.StatusCreated,
	)
	configID := testutil.RequireType[string](t, config["id"])
	stored, found, err := project.Store.Execution().GetAgentConfig(
		ctx,
		project.ProjectUUID,
		mustPublicHTTPID(t, publicid.KindAgentConfig, configID),
	)
	require.NoError(t, err)
	require.True(t, found)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(stored.CompiledDefinition, &compiled))
	attachment := compiled.AppResources["support"]
	require.Equal(t, appID, attachment.AppInstanceID)
	require.Equal(t, connectionID, attachment.ConnectionID)
	require.Equal(t, "C456", attachment.Scope.Slack.ChannelID)
	require.Equal(t, "222.333", attachment.Scope.Slack.ThreadTS)
	require.ElementsMatch(t, []string{"slack_read", "ticket_lookup"}, attachment.Tools)
	require.Nil(t, attachment.Listener)
	require.Nil(t, attachment.InteractionHandler)
	require.NotContains(t, compiled.Tools, "slack_post_message")
	require.Equal(t, "custom", compiled.Tools["ticket_lookup"].Type)
	require.JSONEq(t, projectAppHTTPJSON(t, ticket["input_schema"]), string(compiled.Tools["ticket_lookup"].InputSchema))
	require.Equal(t, []string{"support"}, compiled.Tools["ticket_lookup"].AppOrigin.ResourceKeys)
	// Updating setup affects new compilation, never this already-stored snapshot.
	ticket["description"] = "Read the revised support ticket format."
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+appID,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		headers,
	)
	newer := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"app-recompiled",
		"json",
		source,
		project.AdminToken,
		http.StatusCreated,
	)
	require.NotEqual(t, config["effective_definition_hash"], newer["effective_definition_hash"])
	body["enabled"] = false
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+appID,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		headers,
	)
	denied := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"disabled-app",
		"json",
		source,
		project.AdminToken,
		http.StatusBadRequest,
	)
	require.Contains(t, denied["error"], "disabled")
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+appID, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+appID, "", "", http.StatusNotFound, headers)
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"deleted-app",
		"json",
		source,
		project.AdminToken,
		http.StatusBadRequest,
	)
	fetched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agent-configs/"+configID,
		"",
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(t, config["effective_definition_hash"], fetched["effective_definition_hash"])
	require.Equal(t, config["source"], fetched["source"])
	retained, found, err := project.Store.Execution().GetAgentConfig(ctx, project.ProjectUUID, stored.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, stored.CompiledDefinition, retained.CompiledDefinition)
	_, err = agentconfig.RuntimeContractFromCompiled(
		retained.CompiledDefinition,
		retained.CompilerVersion,
		retained.EffectiveDefinitionHash,
	)
	require.NoError(t, err)
	launched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"config":"`+configID+`"}`,
		"app-snapshot-launch",
		http.StatusCreated,
		headers,
	)
	agent := testutil.RequireType[map[string]any](t, launched["agent"])
	require.Equal(t, configID, agent["current_config_id"])
	require.Equal(t, "active", agent["state"])
	connections := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/integration-connections",
		"",
		"",
		http.StatusOK,
		headers,
	)
	entries := testutil.RequireType[[]any](t, connections["data"])
	require.Len(t, entries, 1)
	require.Equal(t, "active", testutil.RequireType[map[string]any](t, entries[0])["state"])
}

func TestProjectAppHTTPValidatesExportedDefinitionsAndSelections(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-validation")
	headers := authHeaders(project.AdminToken)
	path := project.ProjectPath + "/apps"
	connectionID, _ := projectAppHTTPConnection(t, handler, project, "app-validation")
	for _, test := range []struct {
		name     string
		resource map[string]any
	}{
		{"unknown definition", map[string]any{"definition": "unknown.provider"}},
		{
			"unknown built-in",
			map[string]any{
				"definition": "omnara.slack", "connection": connectionID,
				"tools": map[string]any{"invented_builtin": map[string]any{}},
			},
		},
		{
			"incomplete custom",
			map[string]any{
				"definition": "omnara.slack", "connection": connectionID,
				"tools": map[string]any{"ticket": map[string]any{"type": "custom"}},
			},
		},
		{"incomplete MCP", map[string]any{
			"definition": "omnara.slack", "connection": connectionID,
			"mcp": map[string]any{"crm": map[string]any{}},
		}},
		{
			"embedded credentials",
			map[string]any{
				"definition": "omnara.slack", "connection": connectionID,
				"mcp": map[string]any{"crm": map[string]any{"url": "https://user:password@example.com/mcp"}},
			},
		},
		{
			"wrong provider tool",
			map[string]any{
				"definition": "omnara.slack",
				"connection": connectionID,
				"tools":      map[string]any{"github_read": map[string]any{}},
			},
		},
		{
			"missing connection",
			map[string]any{"definition": "omnara.slack", "tools": map[string]any{"slack_read": map[string]any{}}},
		},
		{
			"wrong provider scope",
			map[string]any{
				"definition": "omnara.slack",
				"connection": connectionID,
				"scope":      map[string]any{"discord": map[string]any{"channel_id": "123"}},
			},
		},
		{
			"placeholder scope",
			map[string]any{
				"definition": "omnara.slack",
				"connection": connectionID,
				"scope":      map[string]any{"slack": map[string]any{"channel_id": "C123", "thread_ts": "${thread}"}},
			},
		},
		{
			"unknown listener event",
			map[string]any{
				"definition": "omnara.slack",
				"connection": connectionID,
				"listener":   map[string]any{"events": []any{"commit"}},
			},
		},
		{
			"wrong interaction handler",
			map[string]any{
				"definition":          "omnara.slack",
				"connection":          connectionID,
				"interaction_handler": map[string]any{"definition": "omnara.discord.interactions"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rejected := requestJSONWithHeaders(
				t,
				handler,
				http.MethodPost,
				path,
				projectAppHTTPJSON(t, projectAppHTTPBody("Invalid export", test.resource)),
				"",
				http.StatusBadRequest,
				headers,
			)
			require.Equal(t, "invalid_request", rejected["code"])
			require.NotEqual(t, "invalid request", rejected["error"], "the validation error must explain what is invalid")
		})
	}
	unknownField := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(
			t,
			projectAppHTTPBody("Unknown field", map[string]any{
				"definition": "omnara.slack", "connection": connectionID, "unrecognized": true,
			}),
		),
		"",
		http.StatusBadRequest,
		headers,
	)
	require.Equal(t, "validation_failed", unknownField["code"])
	require.Contains(t, unknownField["error"], "unrecognized")
	// Hosted apps can also export ordinary custom and MCP tools without a launcher.
	resource := map[string]any{
		"definition": "omnara.slack", "connection": connectionID,
		"tools": map[string]any{"ticket": map[string]any{"type": "custom", "description": "Read a ticket.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}},
		"mcp":   map[string]any{"crm": map[string]any{"url": "https://example.com/mcp"}},
	}
	app := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, projectAppHTTPBody("Exports", resource)),
		"",
		http.StatusCreated,
		headers,
	)
	id := testutil.RequireType[string](t, app["id"])
	for _, test := range []struct {
		name, path, message string
		selection           map[string]any
	}{
		{
			"unexported tool",
			"/tools/unexported",
			"not exported",
			map[string]any{"tools": map[string]any{"unexported": map[string]any{}}},
		},
		{
			"changed description",
			"/tools/ticket",
			"description conflicts",
			map[string]any{"tools": map[string]any{"ticket": map[string]any{"description": "Replace the bundled definition"}}},
		},
		{
			"changed schema",
			"/tools/ticket",
			"input_schema conflicts",
			map[string]any{"tools": map[string]any{"ticket": map[string]any{"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"injected": map[string]any{"type": "string"}},
			}}}},
		},
		{
			"unexported MCP",
			"/mcp/unexported",
			"not exported",
			map[string]any{"mcp": map[string]any{"unexported": map[string]any{}}},
		},
		{
			"changed MCP URL",
			"/mcp/crm",
			"MCP connection conflicts",
			map[string]any{"mcp": map[string]any{"crm": map[string]any{"url": "https://other.example.com/mcp"}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.selection["app_instance"] = id
			rejected := createPublicHTTPAgentConfig(
				t,
				handler,
				project,
				"invalid-selection",
				"json",
				projectAppHTTPJSON(t, projectAppHTTPSource(test.selection)),
				project.AdminToken,
				http.StatusBadRequest,
			)
			require.Equal(t, "invalid_request", rejected["code"])
			issues := testutil.RequireType[[]any](t, rejected["issues"])
			require.Len(t, issues, 1)
			issue := testutil.RequireType[map[string]any](t, issues[0])
			require.Equal(t, "/app_resources/support"+test.path, issue["path"])
			require.Contains(t, issue["message"], test.message)
		})
	}
	// A saved app cannot recursively import another project app.
	rejected := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, projectAppHTTPBody("Nested setup", map[string]any{"app_instance": id})),
		"",
		http.StatusBadRequest,
		headers,
	)
	require.Equal(t, "invalid_request", rejected["code"])
	listed := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Len(t, testutil.RequireType[[]any](t, listed["data"]), 1)
}

func TestProjectAppHTTPLauncherAndConnectionReferenceIsolation(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-launcher")
	headers := authHeaders(project.AdminToken)
	path := project.ProjectPath + "/apps"
	connectionID, profileID := projectAppHTTPConnection(t, handler, project, "app-launcher")
	second := projectAppHTTPSecondProject(t, handler, project)
	otherConnection, otherProfile := projectAppHTTPConnection(t, handler, second, "app-other")
	// A profile-only launcher may supply conversation scope at event time.
	body := projectAppHTTPBody(
		"Mention launcher",
		map[string]any{
			"definition": "omnara.slack",
			"connection": connectionID,
			"tools":      map[string]any{"slack_read": map[string]any{}},
		},
	)
	launcher := map[string]any{
		"trigger":    "mention",
		"scope_kind": "workspace",
		"scope_ref":  "TAPPLAUNCHER",
		"slots":      []any{map[string]any{"key": "reviewer", "agent_profile_id": profileID}},
	}
	settings := testutil.RequireType[map[string]any](t, body["settings"])
	settings["launcher"] = launcher
	app := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	require.Equal(t, body["settings"], app["settings"])
	resource := testutil.RequireType[map[string]any](t, settings["resource"])
	body["name"] = "Cross-project connection"
	resource["connection"] = otherConnection
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	resource["connection"] = connectionID
	body["name"] = "Cross-project profile"
	launcher["slots"] = []any{map[string]any{"key": "reviewer", "agent_profile_id": otherProfile}}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	for _, slots := range []any{
		[]any{map[string]any{"key": "missing"}},
		[]any{
			map[string]any{"key": "same", "agent_profile_id": profileID},
			map[string]any{"key": "same", "agent_profile_id": profileID},
		},
	} {
		launcher["slots"] = slots
		body["name"] = "Invalid slots"
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			path,
			projectAppHTTPJSON(t, body),
			"",
			http.StatusBadRequest,
			headers,
		)
	}
	launcher["slots"] = []any{map[string]any{"key": "reviewer", "agent_profile_id": profileID}}
	launcher["trigger"] = "pull_request_opened"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		headers,
	)
	// A Slack connection cannot authorize a GitHub resource even in the same project.
	wrongProvider := projectAppHTTPBody(
		"Provider mismatch",
		map[string]any{"definition": "omnara.github", "connection": connectionID},
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, wrongProvider),
		"",
		http.StatusForbidden,
		headers,
	)
	id := testutil.RequireType[string](t, app["id"])
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+id, "", "", http.StatusNoContent, headers)
	require.NotEmpty(
		t,
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			project.ProjectPath+"/agent-profiles/"+profileID,
			"",
			"",
			http.StatusOK,
			headers,
		),
	)
}

func TestProjectAppHTTPDisableUnchangedBrokenDependencies(t *testing.T) {
	t.Parallel()
	for _, dependency := range []string{"archived-agent", "disabled-connection", "deleted-connection"} {
		t.Run(dependency, func(t *testing.T) {
			t.Parallel()
			pool := openIntegrationDB(t, t.Context())
			handler := newIntegrationServer(pool)
			project := bootstrapPublicHTTPProject(t, handler, "app-disable-"+dependency)
			headers := authHeaders(project.AdminToken)
			connection, profile := projectAppHTTPConnection(t, handler, project, "app-disable")
			slot := map[string]any{"key": "reviewer", "agent_profile_id": profile}
			var agentID string
			if dependency == "archived-agent" {
				profileSetup := requestJSONWithHeaders(
					t,
					handler,
					http.MethodGet,
					project.ProjectPath+"/agent-profiles/"+profile,
					"",
					"",
					http.StatusOK,
					headers,
				)
				launched := requestJSONWithHeaders(
					t,
					handler,
					http.MethodPost,
					project.ProjectPath+"/agents",
					projectAppHTTPJSON(t, map[string]any{"profile": profile, "config": profileSetup["current_config_id"]}),
					"app-disable-agent",
					http.StatusCreated,
					headers,
				)
				agentID = testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
				slot = map[string]any{"key": "reviewer", "agent_id": agentID}
			}
			body := projectAppHTTPBody("Disable safely", map[string]any{"definition": "omnara.slack", "connection": connection})
			settings := testutil.RequireType[map[string]any](t, body["settings"])
			launcher := map[string]any{
				"trigger":    "mention",
				"scope_kind": "workspace",
				"scope_ref":  "TAPPDISABLE",
				"slots":      []any{slot},
			}
			settings["launcher"] = launcher
			app := requestJSONWithHeaders(
				t,
				handler,
				http.MethodPost,
				project.ProjectPath+"/apps",
				projectAppHTTPJSON(t, body),
				"",
				http.StatusCreated,
				headers,
			)
			path := project.ProjectPath + "/apps/" + testutil.RequireType[string](t, app["id"])
			wantInvalid := http.StatusNotFound
			switch dependency {
			case "archived-agent":
				requestJSONWithHeaders(
					t,
					handler,
					http.MethodPost,
					project.ProjectPath+"/agents/"+agentID+"/archive",
					"",
					"",
					http.StatusOK,
					headers,
				)
				wantInvalid = http.StatusConflict
			case "disabled-connection":
				flow := uuid.Nil
				changed, err := project.Store.Integrations().DisableIntegrationConnection(
					t.Context(),
					integrationstore.DisableIntegrationConnectionInput{
						ProjectID:           project.ProjectUUID,
						ID:                  mustPublicHTTPID(t, publicid.KindIntegrationConnection, connection),
						ExpectedOAuthFlowID: &flow,
					},
				)
				require.NoError(t, err)
				require.True(t, changed)
				wantInvalid = http.StatusForbidden
			case "deleted-connection":
				requestJSONWithHeaders(
					t,
					handler,
					http.MethodDelete,
					project.ProjectPath+"/integration-connections/"+connection,
					"",
					"",
					http.StatusNoContent,
					headers,
				)
			}
			body["enabled"] = false
			for range 2 {
				disabled := requestJSONWithHeaders(
					t,
					handler,
					http.MethodPut,
					path,
					projectAppHTTPJSON(t, body),
					"",
					http.StatusOK,
					headers,
				)
				require.Equal(t, false, disabled["enabled"])
				require.Equal(t, app["settings"], disabled["settings"])
			}
			body["enabled"] = true
			requestJSONWithHeaders(t, handler, http.MethodPut, path, projectAppHTTPJSON(t, body), "", wantInvalid, headers)
			body["enabled"] = false
			launcher["scope_ref"] = "TCHANGED"
			requestJSONWithHeaders(t, handler, http.MethodPut, path, projectAppHTTPJSON(t, body), "", wantInvalid, headers)
			fetched := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, headers)
			require.Equal(t, false, fetched["enabled"])
			require.Equal(t, app["settings"], fetched["settings"], "failed re-enable/edit must preserve disabled setup")
		})
	}
}

func TestProjectAppHTTPMCPSecretAuthorizationAndDisable(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-secret")
	connectionID, _ := projectAppHTTPConnection(t, handler, project, "app-secret-hosted")
	other := projectAppHTTPSecondProject(t, handler, project)
	headers := authHeaders(project.AdminToken)
	secretPath := "/api/v1/orgs/" + project.OrgID + "/secrets"
	createSecret := func(name, projectID string, material map[string]any) string {
		t.Helper()
		secret := requestJSONWithHeaders(t, handler, http.MethodPost, secretPath, projectAppHTTPJSON(t, map[string]any{
			"name": name, "owner": map[string]any{"kind": "project", "project_id": projectID}, "material": material,
		}), "", http.StatusCreated, headers)
		return testutil.RequireType[string](t, secret["id"])
	}
	bearer := createSecret("bearer", project.ProjectID, map[string]any{"kind": "generic", "value": "private-app-bearer"})
	oauth := createSecret(
		"oauth",
		project.ProjectID,
		map[string]any{"kind": "oauth_token_set", "access_token": "private-app-oauth"},
	)
	aws := createSecret(
		"aws",
		project.ProjectID,
		map[string]any{"kind": "aws_credentials", "access_key_id": "AKIAEXAMPLE", "secret_access_key": "private-app-aws"},
	)
	foreign := createSecret(
		"foreign",
		other.ProjectID,
		map[string]any{"kind": "generic", "value": "private-foreign-bearer"},
	)
	bodyFor := func(name, authType, secret string) map[string]any {
		auth := map[string]any{"type": authType, "secret_id": secret}
		if authType == "sigv4" {
			auth["service"], auth["region"] = "execute-api", "us-east-1"
		}
		return projectAppHTTPBody(
			name,
			map[string]any{
				"definition": "omnara.slack", "connection": connectionID,
				"mcp": map[string]any{"crm": map[string]any{"url": "https://example.com/mcp", "auth": auth}},
			},
		)
	}
	for _, test := range []struct{ authType, secret string }{{"bearer", bearer}, {"oauth", oauth}, {"sigv4", aws}} {
		body := bodyFor("Valid "+test.authType, test.authType, test.secret)
		app := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			project.ProjectPath+"/apps",
			projectAppHTTPJSON(t, body),
			"",
			http.StatusCreated,
			headers,
		)
		require.Equal(t, body["settings"], app["settings"])
		require.NotContains(t, projectAppHTTPJSON(t, app), "private-app-")
	}
	for _, test := range []struct{ authType, secret string }{{"bearer", aws}, {"oauth", bearer}, {"sigv4", bearer}} {
		rejected := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			project.ProjectPath+"/apps",
			projectAppHTTPJSON(t, bodyFor("Wrong kind", test.authType, test.secret)),
			"",
			http.StatusBadRequest,
			headers,
		)
		require.Equal(t, "invalid_request", rejected["code"])
		require.Contains(t, rejected["error"], "kind")
	}
	body := bodyFor("Granted export", "bearer", foreign)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/apps",
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	grant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		secretPath+"/"+foreign+"/grants",
		projectAppHTTPJSON(t, map[string]any{"target_project_id": project.ProjectID}),
		"",
		http.StatusCreated,
		headers,
	)
	app := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/apps",
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	appPath := project.ProjectPath + "/apps/" + testutil.RequireType[string](t, app["id"])
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		secretPath+"/"+foreign+"/grants/"+testutil.RequireType[string](t, grant["id"]),
		"",
		"",
		http.StatusNoContent,
		headers,
	)
	body["enabled"] = false
	disabled := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		appPath,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(t, app["settings"], disabled["settings"])
	body["enabled"] = true
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		appPath,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	body["enabled"] = false
	body["name"] = "New disabled invalid setup"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/apps",
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		appPath,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	// The same disable-only contract applies after a direct secret is deleted.
	body = bodyFor("Deleted secret", "bearer", bearer)
	app = requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/apps",
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	appPath = project.ProjectPath + "/apps/" + testutil.RequireType[string](t, app["id"])
	requestJSONWithHeaders(t, handler, http.MethodDelete, secretPath+"/"+bearer, "", "", http.StatusNoContent, headers)
	body["enabled"] = false
	requestJSONWithHeaders(t, handler, http.MethodPut, appPath, projectAppHTTPJSON(t, body), "", http.StatusOK, headers)
	body["enabled"] = true
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		appPath,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
}

func TestProjectAppHTTPDisableDetectsConcurrentSettingsChange(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-disable-race")
	connectionID, _ := projectAppHTTPConnection(t, handler, project, "app-disable-race-hosted")
	headers := authHeaders(project.AdminToken)
	body := projectAppHTTPBody("Concurrent setup", map[string]any{
		"definition": "omnara.slack", "connection": connectionID,
	})
	app := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/apps",
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	id := testutil.RequireType[string](t, app["id"])
	path := project.ProjectPath + "/apps/" + id
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	changed := map[string]any{"resource": map[string]any{
		"definition": "omnara.slack", "connection": connectionID,
		"scope": map[string]any{"slack": map[string]any{"channel_id": "C999"}},
	}}
	_, err = tx.Exec(
		ctx,
		`UPDATE project_apps SET settings=$3::jsonb WHERE project_id=$1 AND id=$2`,
		project.ProjectUUID,
		mustPublicHTTPID(t, publicid.KindProjectApp, id),
		projectAppHTTPJSON(t, changed),
	)
	require.NoError(t, err)
	body["enabled"] = false
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(projectAppHTTPJSON(t, body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		done <- response
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DisableUnchangedProjectApp", 1)
	require.NoError(t, tx.Commit(ctx))
	response := <-done
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	var rejected map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &rejected))
	require.Equal(t, "conflict", rejected["code"])
	fetched := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Equal(t, true, fetched["enabled"])
	require.Equal(t, changed, fetched["settings"], "the disable cannot overwrite a concurrently edited dependency")
}

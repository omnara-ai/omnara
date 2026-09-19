//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestResolveAgentConfigTools(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, context.Background())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "resolve-tools")
	other := bootstrapPublicHTTPProject(t, handler, "resolve-tools-other")
	request := func(body map[string]any, token string, status int) map[string]any {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return requestJSONWithHeaders(
			t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/tools", string(raw), "", status, authHeaders(token),
		)
	}
	yaml := "machine_sources: [{machine_pool_name: not-created-yet}]\n" +
		"subagents: {worker: {type: profile, profile: not-created-yet}}\n" +
		"tools: {run_command: {enabled: false, permission: {mode: always_ask}}}\n"
	jsonSource := `{"machine_sources":[{"machine_pool_name":"not-created-yet"}],` +
		`"subagents":{"worker":{"type":"profile","profile":"not-created-yet"}},` +
		`"tools":{"run_command":{"enabled":false,"permission":{"mode":"always_ask"}}}}`
	preview := request(map[string]any{"source": yaml, "source_format": "yaml"}, project.AdminToken, http.StatusOK)
	jsonPreview := request(
		map[string]any{"source": jsonSource, "source_format": "json"}, project.AdminToken, http.StatusOK,
	)
	if diff := cmp.Diff(preview, jsonPreview); diff != "" {
		t.Fatal(diff)
	}
	tools := testutil.RequireType[[]any](t, preview["tools"])
	if len(tools) != 18 {
		t.Fatalf("expected 11 pool tools, 5 subagent tools, and 2 retrieval tools, got %v", tools)
	}
	for _, item := range tools {
		tool := testutil.RequireType[map[string]any](t, item)
		if tool["name"] == "run_command" {
			permission := testutil.RequireType[map[string]any](t, tool["permission"])
			if tool["enabled"] != false || permission["mode"] != "always_ask" {
				t.Fatalf("lost explicit override: %v", tool)
			}
		}
	}
	empty := map[string]any{"source": `{"instruction":"","model":{}}`, "source_format": "json"}
	response := request(empty, project.AdminToken, http.StatusOK)
	if len(testutil.RequireType[[]any](t, response["tools"])) != 0 {
		t.Fatalf("unexpected contextual tools: %v", response)
	}
	mcp := request(map[string]any{
		"source": `{"mcp":{"docs":{"url":"https://example.com/mcp","default_enabled":false}}}`, "source_format": "json",
	}, project.AdminToken, http.StatusOK)
	if len(testutil.RequireType[[]any](t, mcp["tools"])) != 2 {
		t.Fatalf("MCP-only config missing retrieval tools: %v", mcp)
	}
	for _, body := range []map[string]any{
		{}, {"source": "{}"}, {"source_format": "json"},
		{"source": "tools: {run_command: {enabled: nope}}", "source_format": "yaml"},
		{"source": "{}", "source_format": "xml"},
		{"source": "{}", "source_format": "json", "config_id": "unused"},
		{"source": "{}", "source_format": "json", "agent_id": "unused"},
	} {
		request(body, project.AdminToken, http.StatusBadRequest)
	}
	request(empty, "", http.StatusUnauthorized)
	request(empty, other.AdminToken, http.StatusNotFound)
}

func TestResolveAgentConfigToolsWithProjectApp(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "preview-app")
	connection, _ := projectAppHTTPConnection(t, handler, project, "preview-app")
	body := projectAppHTTPBody("Preview app", map[string]any{
		"definition": "omnara.slack", "connection": connection,
		"tools": map[string]any{"slack_read": map[string]any{}, "slack_post_message": map[string]any{}},
	})
	app := requestJSONWithHeaders(
		t, handler, http.MethodPost, project.ProjectPath+"/apps", projectAppHTTPJSON(t, body),
		"", http.StatusCreated, authHeaders(project.AdminToken),
	)
	resource := map[string]any{
		"app_instance": app["id"], "tools": map[string]any{"slack_read": map[string]any{}},
		"scope": map[string]any{"slack": map[string]any{"channel_id": "C123", "thread_ts": "123.456"}},
	}
	source := map[string]any{"app_resources": map[string]any{"chat": resource}}
	preview := func(scope publicHTTPProject, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(
			t, handler, http.MethodPost, scope.ProjectPath+"/agent-configs/tools",
			projectAppHTTPJSON(t, map[string]any{"source_format": "json", "source": projectAppHTTPJSON(t, source)}),
			"", status, authHeaders(scope.AdminToken),
		)
	}
	toolsByName := func(response map[string]any) map[string]map[string]any {
		t.Helper()
		tools := make(map[string]map[string]any)
		for _, item := range testutil.RequireType[[]any](t, response["tools"]) {
			tool := testutil.RequireType[map[string]any](t, item)
			tools[testutil.RequireType[string](t, tool["name"])] = tool
		}
		return tools
	}
	tools := toolsByName(preview(project, http.StatusOK))
	require.Equal(t, true, tools["slack_read"]["enabled"])
	require.NotContains(t, tools, "slack_post_message", "unselected bundled tools must not leak into preview")
	source["tools"] = map[string]any{
		"slack_read": map[string]any{"enabled": false, "permission": map[string]any{"mode": "always_deny"}},
	}
	tools = toolsByName(preview(project, http.StatusOK))
	require.Equal(t, false, tools["slack_read"]["enabled"])
	require.Equal(t, "always_deny", testutil.RequireType[map[string]any](t, tools["slack_read"]["permission"])["mode"])
	other := projectAppHTTPSecondProject(t, handler, project)
	rejected := preview(other, http.StatusBadRequest)
	require.Equal(t, "invalid_request", rejected["code"])
	require.Contains(t, projectAppHTTPJSON(t, rejected), "/app_resources/chat")
	requestJSONWithHeaders(
		t, handler, http.MethodDelete, project.ProjectPath+"/integration-connections/"+connection,
		"", "", http.StatusNoContent, authHeaders(project.AdminToken),
	)
	rejected = preview(project, http.StatusBadRequest)
	require.Equal(t, "invalid_request", rejected["code"])
	require.Contains(t, projectAppHTTPJSON(t, rejected), "/app_resources/chat")
}

func TestResolveAgentConfigToolsInactiveCustomAppPolicy(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "preview-inactive-policy")
	connectionID, _ := projectAppHTTPConnection(t, handler, project, "preview-policy-hosted")
	resources := map[string]any{}
	for _, mode := range []string{"always_allow", "always_ask"} {
		tool := map[string]any{
			"type": "custom", "description": "Read a ticket.", "deferred": true,
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{}, "additionalProperties": false,
			},
			"permission": map[string]any{"mode": mode},
		}
		app := requestJSONWithHeaders(
			t, handler, http.MethodPost, project.ProjectPath+"/apps",
			projectAppHTTPJSON(t, projectAppHTTPBody(mode, map[string]any{
				"definition": "omnara.slack", "connection": connectionID, "tools": map[string]any{"ticket": tool},
				"scope": map[string]any{"slack": map[string]any{"channel_id": "C123"}},
			})), "", http.StatusCreated, authHeaders(project.AdminToken),
		)
		resources[mode] = map[string]any{
			"app_instance": app["id"], "enabled": false, "tools": map[string]any{"ticket": map[string]any{}},
		}
	}
	policy := map[string]any{"deferred": false, "permission": map[string]any{"mode": "always_deny"}}
	source := map[string]any{"app_resources": resources, "tools": map[string]any{"ticket": policy}}
	preview := func(status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(
			t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/tools",
			projectAppHTTPJSON(t, map[string]any{"source_format": "json", "source": projectAppHTTPJSON(t, source)}),
			"", status, authHeaders(project.AdminToken),
		)
	}
	tools := testutil.RequireType[[]any](t, preview(http.StatusOK)["tools"])
	require.Empty(t, tools, "retained policy must not grant a tool or imply discovery/retrieval tools")
	for _, resource := range resources {
		testutil.RequireType[map[string]any](t, resource)["enabled"] = true
	}
	for _, enabled := range []bool{true, false} {
		policy["enabled"] = enabled
		tools = testutil.RequireType[[]any](t, preview(http.StatusOK)["tools"])
		found := false
		for _, value := range tools {
			tool := testutil.RequireType[map[string]any](t, value)
			require.NotEqual(t, "tool_search", tool["name"], "global deferred false overrides bundled true")
			if tool["name"] == "ticket" {
				found = true
				require.Equal(t, enabled, tool["enabled"])
				require.Equal(t, "always_deny", testutil.RequireType[map[string]any](t, tool["permission"])["mode"])
			}
		}
		require.True(t, found, "preview exposes disabled policy once an active attachment declares the tool")
	}
	delete(source, "tools")
	rejected := preview(http.StatusBadRequest)
	require.Equal(t, "invalid_request", rejected["code"])
	require.Contains(t, projectAppHTTPJSON(t, rejected), "conflicting app tool definitions or permissions")
	require.Contains(t, projectAppHTTPJSON(t, rejected), "/app_resources/always_ask/tools/ticket")
}

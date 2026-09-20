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
	if len(tools) != 20 {
		t.Fatalf("expected 11 pool tools, 5 subagent tools, 2 retrieval tools, and 2 interaction tools, got %v", tools)
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
	if len(testutil.RequireType[[]any](t, response["tools"])) != 2 {
		t.Fatalf("unexpected contextual tools: %v", response)
	}
	mcp := request(map[string]any{
		"source": `{"mcp":{"docs":{"url":"https://example.com/mcp","default_enabled":false}}}`, "source_format": "json",
	}, project.AdminToken, http.StatusOK)
	if len(testutil.RequireType[[]any](t, mcp["tools"])) != 4 {
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
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "preview-app")
	app := createSlackHTTPApp(t, t.Context(), project, "A123", "T123", "Support")
	name := "app__" + app.Name + "__read"
	entry := map[string]any{"config": map[string]any{"channel_id": "C123", "thread_ts": "123.456"}}
	source := map[string]any{"tools": map[string]any{name: entry}}
	preview := func(scope publicHTTPProject, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodPost, scope.ProjectPath+"/agent-configs/tools",
			projectAppHTTPJSON(t, map[string]any{"source_format": "json", "source": projectAppHTTPJSON(t, source)}),
			"", status, authHeaders(scope.AdminToken))
	}
	toolsByName := func(response map[string]any) map[string]map[string]any {
		t.Helper()
		result := map[string]map[string]any{}
		for _, item := range testutil.RequireType[[]any](t, response["tools"]) {
			tool := testutil.RequireType[map[string]any](t, item)
			result[testutil.RequireType[string](t, tool["name"])] = tool
		}
		return result
	}
	tools := toolsByName(preview(project, http.StatusOK))
	require.Equal(t, true, tools[name]["enabled"])
	require.NotContains(t, tools, "app__"+app.Name+"__post_message")
	entry["enabled"] = false
	entry["permission"] = map[string]any{"mode": "always_deny"}
	tools = toolsByName(preview(project, http.StatusOK))
	require.Equal(t, false, tools[name]["enabled"])
	require.Equal(t, "always_deny", testutil.RequireType[map[string]any](t, tools[name]["permission"])["mode"])
	other := projectAppHTTPSecondProject(t, handler, project)
	rejected := preview(other, http.StatusBadRequest)
	require.Contains(t, projectAppHTTPJSON(t, rejected), "/tools/"+name)
	entry["config"] = map[string]any{"unknown": true}
	rejected = preview(project, http.StatusBadRequest)
	require.Contains(t, projectAppHTTPJSON(t, rejected), "/tools/"+name)
}

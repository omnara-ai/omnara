//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
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
	for _, source := range []any{jsonSource, json.RawMessage(jsonSource)} {
		jsonPreview := request(
			map[string]any{"source": source, "source_format": "json"}, project.AdminToken, http.StatusOK,
		)
		if diff := cmp.Diff(preview, jsonPreview); diff != "" {
			t.Fatal(diff)
		}
	}
	tools := testutil.RequireType[[]any](t, preview["tools"])
	if len(tools) != 19 {
		t.Fatalf("expected 11 pool tools, 5 subagent tools, and 3 retrieval tools, got %v", tools)
	}
	for _, item := range tools {
		tool := testutil.RequireType[map[string]any](t, item)
		require.NotEqual(t, toolcatalog.ToolNameListInteractionHandlers, tool["name"], "interaction tools are explicit")
		require.NotEqual(t, toolcatalog.ToolNameSetInteractionHandler, tool["name"], "interaction tools are explicit")
		if tool["name"] == "run_command" {
			permission := testutil.RequireType[map[string]any](t, tool["permission"])
			if tool["enabled"] != false || permission["mode"] != "always_ask" {
				t.Fatalf("lost explicit override: %v", tool)
			}
		}
	}
	empty := map[string]any{
		"source":        map[string]any{"instruction": "", "model": map[string]any{}},
		"source_format": "json",
	}
	response := request(empty, project.AdminToken, http.StatusOK)
	require.Empty(t, testutil.RequireType[[]any](t, response["tools"]), "ordinary configs grant no interaction tools")
	mcp := request(map[string]any{
		"source":        json.RawMessage(`{"mcp":{"docs":{"url":"https://example.com/mcp","default_enabled":false}}}`),
		"source_format": "json",
	}, project.AdminToken, http.StatusOK)
	var mcpToolNames []string
	for _, item := range testutil.RequireType[[]any](t, mcp["tools"]) {
		tool := testutil.RequireType[map[string]any](t, item)
		mcpToolNames = append(mcpToolNames, testutil.RequireType[string](t, tool["name"]))
	}
	require.ElementsMatch(t, []string{
		toolcatalog.ToolNameListFiles, toolcatalog.ToolNameReadFile, toolcatalog.ToolNameSearchFiles,
	}, mcpToolNames)
	for _, body := range []map[string]any{
		{}, {"source": "{}"}, {"source_format": "json"},
		{"source": "tools: {run_command: {enabled: nope}}", "source_format": "yaml"},
		{"source": "{}", "source_format": "xml"},
		{"source": map[string]any{}, "source_format": "yaml"},
		{"source": []any{}, "source_format": "json"},
		{"source": map[string]any{}, "source_format": "json", "config_id": "unused"},
		{"source": map[string]any{}, "source_format": "json", "agent_id": "unused"},
	} {
		request(body, project.AdminToken, http.StatusBadRequest)
	}
	request(empty, "", http.StatusUnauthorized)
	request(empty, other.AdminToken, http.StatusNotFound)
}

func TestResolveAgentConfigToolsWithIntegration(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "preview-integration")
	integration := createSlackHTTPIntegration(t, t.Context(), project, "A123", "T123", "Support")
	name := "int__" + integration.Name + "__read"
	entry := map[string]any{}
	source := map[string]any{"tools": map[string]any{name: entry}}
	preview := func(scope publicHTTPProject, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodPost, scope.ProjectPath+"/agent-configs/tools",
			integrationHTTPJSON(
				t,
				map[string]any{"source_format": "json", "source": integrationHTTPJSON(t, source)},
			),
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
	require.NotContains(t, tools, "int__"+integration.Name+"__post_message")
	entry["enabled"] = false
	entry["permission"] = map[string]any{"mode": "always_deny"}
	tools = toolsByName(preview(project, http.StatusOK))
	require.Equal(t, false, tools[name]["enabled"])
	require.Equal(t, "always_deny", testutil.RequireType[map[string]any](t, tools[name]["permission"])["mode"])
	other := integrationHTTPSecondProject(t, handler, project)
	rejected := preview(other, http.StatusBadRequest)
	require.Contains(t, integrationHTTPJSON(t, rejected), "/tools/"+name)
}

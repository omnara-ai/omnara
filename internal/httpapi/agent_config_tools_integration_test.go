//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/testutil"
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

//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/testutil"
)

type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	cloned := r.Clone(r.Context())
	cloned.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(cloned)
}

func TestAPIMCPRoute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "api-mcp")

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint := server.URL + apimcp.Path

	t.Run("stateless client", func(t *testing.T) {
		t.Parallel()
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: &http.Client{Transport: bearerTransport{token: project.AdminToken, next: http.DefaultTransport}},
		}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = session.Close() })

		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		if len(listed.Tools) != len(apimcp.Tools) {
			t.Fatalf("listed %d tools, want %d", len(listed.Tools), len(apimcp.Tools))
		}

		me, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
		if err != nil {
			t.Fatalf("call whoami: %v", err)
		}
		if me.IsError {
			t.Fatalf("whoami returned error: %s", testutil.RequireType[*mcp.TextContent](t, me.Content[0]).Text)
		}
		current := testutil.RequireType[map[string]any](t, me.StructuredContent)
		user := testutil.RequireType[map[string]any](t, current["user"])
		if user["id"] != project.AdminUserID {
			t.Fatalf("whoami returned %v, want user %s", user["id"], project.AdminUserID)
		}

		orgs, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "orgs_list"})
		if err != nil {
			t.Fatalf("call orgs_list: %v", err)
		}
		if orgs.IsError {
			t.Fatalf("orgs_list returned error: %s", testutil.RequireType[*mcp.TextContent](t, orgs.Content[0]).Text)
		}
		orgPage := testutil.RequireType[map[string]any](t, orgs.StructuredContent)
		orgData := testutil.RequireType[[]any](t, orgPage["data"])
		if len(orgData) != 1 {
			t.Fatalf("orgs_list returned %d orgs, want 1", len(orgData))
		}
		if org := testutil.RequireType[map[string]any](t, orgData[0]); org["id"] != project.OrgID {
			t.Fatalf("orgs_list returned org %v, want %s", org["id"], project.OrgID)
		}

		agents, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "agents_list", Arguments: map[string]any{
			"orgID":     project.OrgID,
			"projectID": project.ProjectID,
			"limit":     5,
		}})
		if err != nil {
			t.Fatalf("call agents_list: %v", err)
		}
		if agents.IsError {
			t.Fatalf("agents_list returned error: %s", testutil.RequireType[*mcp.TextContent](t, agents.Content[0]).Text)
		}
		page := testutil.RequireType[map[string]any](t, agents.StructuredContent)
		if _, ok := page["data"]; !ok {
			t.Fatalf("agents_list page missing data: %v", page)
		}

		forbidden, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "agents_list", Arguments: map[string]any{
			"orgID":     "org_00000000000000000000000000",
			"projectID": project.ProjectID,
		}})
		if err != nil {
			t.Fatalf("call agents_list with foreign org: %v", err)
		}
		if !forbidden.IsError {
			t.Fatal("foreign org should surface as a tool error")
		}
	})

	t.Run("viewer key only discovers read tools", func(t *testing.T) {
		t.Parallel()
		created := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			"/api/v1/orgs/"+project.OrgID+"/api-keys",
			`{"name":"viewer key","org_role":"member"}`,
			"idem-api-mcp-viewer-key",
			http.StatusCreated,
			project.adminBrowserAuthHeaders(),
		)
		viewerToken := testutil.RequireType[string](t, created["token"])
		apiKey := testutil.RequireType[map[string]any](t, created["api_key"])
		keyID := testutil.RequireType[string](t, apiKey["id"])
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			"/api/v1/orgs/"+project.OrgID+"/api-keys/"+keyID+"/projects/"+project.ProjectID,
			`{"role":"viewer"}`,
			"",
			http.StatusOK,
			project.adminBrowserAuthHeaders(),
		)

		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: &http.Client{Transport: bearerTransport{token: viewerToken, next: http.DefaultTransport}},
		}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = session.Close() })

		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		names := make(map[string]bool, len(listed.Tools))
		for _, tool := range listed.Tools {
			names[tool.Name] = true
		}
		for _, want := range []string{"orgs_list", "agents_list", "agents_get", "projects_list", "profiles_list", "machines_get"} {
			if !names[want] {
				t.Errorf("viewer key should see %s, got %v", want, names)
			}
		}
		hidden := []string{
			"whoami", "agents_launch", "agents_input", "agents_cancel", "agents_archive",
			"machines_update", "machines_delete", "grant_machines_add", "grant_machines_delete",
		}
		for _, hidden := range hidden {
			if names[hidden] {
				t.Errorf("viewer key should not see %s, got %v", hidden, names)
			}
		}

		orgs, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "orgs_list"})
		if err != nil {
			t.Fatalf("call orgs_list: %v", err)
		}
		if orgs.IsError {
			t.Fatalf("orgs_list returned error: %s", testutil.RequireType[*mcp.TextContent](t, orgs.Content[0]).Text)
		}
		orgPage := testutil.RequireType[map[string]any](t, orgs.StructuredContent)
		orgData := testutil.RequireType[[]any](t, orgPage["data"])
		if len(orgData) != 1 {
			t.Fatalf("orgs_list returned %d orgs for an api key, want 1", len(orgData))
		}
		keyOrg := testutil.RequireType[map[string]any](t, orgData[0])
		if keyOrg["id"] != project.OrgID || keyOrg["role"] != "member" {
			t.Fatalf("orgs_list returned %v, want org %s with role member", keyOrg, project.OrgID)
		}

		_, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "agents_launch", Arguments: map[string]any{
			"orgID": project.OrgID, "projectID": project.ProjectID,
		}})
		if err == nil {
			t.Fatal("viewer key must not be able to call agents_launch")
		}
		if !strings.Contains(err.Error(), `unknown tool "agents_launch"`) {
			t.Fatalf("hidden tool error = %v, want the unknown tool error", err)
		}
	})

	t.Run("legacy initialize handshake", func(t *testing.T) {
		t.Parallel()
		initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"legacy","version":"0"}}}`
		response := postMCP(t, endpoint, project.AdminToken, initialize)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("initialize status %d", response.StatusCode)
		}
		if response.Header.Get("Mcp-Session-Id") != "" {
			t.Fatal("stateless server must not assign a session id")
		}
		var initResult struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if err := json.NewDecoder(response.Body).Decode(&initResult); err != nil {
			t.Fatalf("decode initialize: %v", err)
		}
		if initResult.Result.ProtocolVersion != "2025-06-18" {
			t.Fatalf("negotiated %q, want the client's legacy version", initResult.Result.ProtocolVersion)
		}

		tools := postMCP(t, endpoint, project.AdminToken, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		if tools.StatusCode != http.StatusOK {
			t.Fatalf("tools/list without a session id returned %d", tools.StatusCode)
		}
	})

	t.Run("rejects non-post methods", func(t *testing.T) {
		t.Parallel()
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+project.AdminToken)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET status %d, want 405", response.StatusCode)
		}
	})

	t.Run("requires bearer auth", func(t *testing.T) {
		t.Parallel()
		response := postMCP(t, endpoint, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated status %d, want 401", response.StatusCode)
		}
	})
}

func postMCP(t *testing.T, endpoint, token, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

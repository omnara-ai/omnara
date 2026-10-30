//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestAgentInputSourceRecordsSendingSurface(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "input-source")
	profile := createPublicHTTPAgent(t, handler, project, "input-source", project.AdminToken)

	launch := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents",
		`{"profile":"`+testutil.RequireType[string](t, profile["id"])+`","message":"launch"}`,
		"input-source-launch", http.StatusCreated, project.adminBrowserAuthHeaders())
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])
	requireAgentInputSource(t, pool, launch, "web")

	path := project.ProjectPath + "/agents/" + agentID + "/inputs"
	body := `{"content_blocks":[{"type":"text","text":"hello"}]}`
	web := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "input-source-web",
		http.StatusCreated, project.adminBrowserAuthHeaders())
	requireAgentInputSource(t, pool, web, "web")
	api := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "input-source-api",
		http.StatusCreated, authHeaders(project.AdminToken))
	requireAgentInputSource(t, pool, api, "api")

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{
			Endpoint:   server.URL + apimcp.Path,
			HTTPClient: &http.Client{Transport: bearerTransport{token: project.AdminToken, next: http.DefaultTransport}},
		}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	sent, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "agents_input", Arguments: map[string]any{
		"orgID":          project.OrgID,
		"projectID":      project.ProjectID,
		"agentID":        agentID,
		"content_blocks": []map[string]any{{"type": "text", "text": "hello"}},
	}})
	if err != nil {
		t.Fatalf("call agents_input: %v", err)
	}
	if sent.IsError {
		t.Fatalf("agents_input returned error: %s", testutil.RequireType[*mcp.TextContent](t, sent.Content[0]).Text)
	}
	requireAgentInputSource(t, pool, testutil.RequireType[map[string]any](t, sent.StructuredContent), "mcp")
}

func requireAgentInputSource(t *testing.T, pool *pgxpool.Pool, response map[string]any, want string) {
	t.Helper()
	input := testutil.RequireType[map[string]any](t, response["agent_input"])
	id := mustPublicHTTPID(t, publicid.KindAgentInput, testutil.RequireType[string](t, input["id"]))
	var source string
	if err := pool.QueryRow(t.Context(),
		`SELECT coalesce(source, '') FROM agent_inputs WHERE id = $1`, id).Scan(&source); err != nil {
		t.Fatalf("read agent input source: %v", err)
	}
	if source != want {
		t.Fatalf("agent input source = %q, want %q", source, want)
	}
}

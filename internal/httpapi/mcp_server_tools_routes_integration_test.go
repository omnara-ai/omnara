//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestListMCPServerToolsUsesProjectSecretForBearerAuth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(
		pool,
		WithAgentConfigOptions(agentconfig.CompileOptions{AllowInsecureLocalMCPHTTP: true}),
	)
	project := bootstrapPublicHTTPProject(t, handler, "mcp-server-tools")
	upstream := httptest.NewServer(fakeMCPToolServer{
		t:        t,
		wantAuth: "Bearer mcp-token",
		tools:    []map[string]any{{"name": "search", "inputSchema": map[string]any{"type": "object"}}},
	}.handler())
	defer upstream.Close()

	secret := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"name":"mcp-token","material":{"kind":"generic","value":"mcp-token"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	secretID := testutil.RequireType[string](t, secret["id"])
	awsSecret := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"name":"aws","material":{"kind":"aws_credentials","access_key_id":"AKIAEXAMPLE","secret_access_key":"secret"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	awsSecretID := testutil.RequireType[string](t, awsSecret["id"])

	response := requestJSONWithHeaders(t, handler, http.MethodPost,
		project.ProjectPath+"/mcp-servers/tools",
		`{"url":"`+upstream.URL+`/mcp","auth":{"type":"bearer","secret_id":"`+secretID+`"}}`,
		"", http.StatusOK, authHeaders(project.AdminToken))
	tools := testutil.RequireType[[]any](t, response["tools"])
	if len(tools) != 1 || testutil.RequireType[map[string]any](t, tools[0])["name"] != "search" {
		t.Fatalf("tools = %+v", response)
	}
	if testutil.RequireType[map[string]any](t, response["server_info"])["name"] != "weather" {
		t.Fatalf("server_info = %+v", response["server_info"])
	}
	var cachedTools string
	var cachedWithSecret bool
	if err := pool.QueryRow(ctx, `
SELECT catalog.tools_snapshot::text, catalog.secret_id IS NOT NULL
FROM mcp_server_catalogs catalog
WHERE catalog.endpoint_url = $1
`, upstream.URL+"/mcp").Scan(&cachedTools, &cachedWithSecret); err != nil {
		t.Fatalf("tool discovery did not populate the shared catalog: %v", err)
	}
	if !strings.Contains(cachedTools, `"search"`) || !cachedWithSecret {
		t.Fatalf("cached catalog = %s (secret keyed %t)", cachedTools, cachedWithSecret)
	}

	unauthenticated := requestJSONWithHeaders(t, handler, http.MethodPost,
		project.ProjectPath+"/mcp-servers/tools",
		`{"url":"`+upstream.URL+`/mcp","auth":{"type":"none"}}`,
		"", http.StatusUnprocessableEntity, authHeaders(project.AdminToken))
	if unauthenticated["code"] != "unprocessable" ||
		testutil.RequireType[map[string]any](t, unauthenticated["auth"])["type"] != "bearer" {
		t.Fatalf("unauthenticated response = %+v", unauthenticated)
	}

	wrongKind := requestJSONWithHeaders(t, handler, http.MethodPost,
		project.ProjectPath+"/mcp-servers/tools",
		`{"url":"`+upstream.URL+`/mcp","auth":{"type":"bearer","secret_id":"`+awsSecretID+`"}}`,
		"", http.StatusBadRequest, authHeaders(project.AdminToken))
	if wrongKind["code"] != "invalid_request" {
		t.Fatalf("wrong kind response = %+v", wrongKind)
	}

	missingSecretID, err := publicid.Encode(publicid.KindSecret, uuid.New())
	if err != nil {
		t.Fatalf("encode secret id: %v", err)
	}
	requestJSONWithHeaders(t, handler, http.MethodPost,
		project.ProjectPath+"/mcp-servers/tools",
		`{"url":"`+upstream.URL+`/mcp","auth":{"type":"bearer","secret_id":"`+missingSecretID+`"}}`,
		"", http.StatusNotFound, authHeaders(project.AdminToken))
}

func TestListMCPServerToolsUsesProjectSecretForOAuthAuth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(
		pool,
		WithAgentConfigOptions(agentconfig.CompileOptions{AllowInsecureLocalMCPHTTP: true}),
	)
	project := bootstrapPublicHTTPProject(t, handler, "mcp-server-tools-oauth")
	upstream := httptest.NewServer(fakeMCPToolServer{
		t:        t,
		wantAuth: "Bearer oauth-access",
		tools:    []map[string]any{{"name": "search", "inputSchema": map[string]any{"type": "object"}}},
	}.handler())
	defer upstream.Close()

	secret := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"name":"mcp-oauth","material":{"kind":"oauth_token_set","access_token":"oauth-access","mcp_url":"`+upstream.URL+`/mcp"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	secretID := testutil.RequireType[string](t, secret["id"])

	response := requestJSONWithHeaders(t, handler, http.MethodPost,
		project.ProjectPath+"/mcp-servers/tools",
		`{"url":"`+upstream.URL+`/mcp","auth":{"type":"oauth","secret_id":"`+secretID+`"}}`,
		"", http.StatusOK, authHeaders(project.AdminToken))
	tools := testutil.RequireType[[]any](t, response["tools"])
	if len(tools) != 1 || testutil.RequireType[map[string]any](t, tools[0])["name"] != "search" {
		t.Fatalf("tools = %+v", response)
	}
}

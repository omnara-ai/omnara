package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

type fakeMCPToolServer struct {
	t        *testing.T
	wantAuth string
	tools    []map[string]any
	pageSize int
}

func (f fakeMCPToolServer) toolsPage(cursor string) map[string]any {
	if f.pageSize == 0 {
		return map[string]any{"tools": f.tools}
	}
	start := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "page-%d", &start); err != nil {
			f.t.Errorf("unexpected cursor %q", cursor)
		}
	}
	end := min(start+f.pageSize, len(f.tools))
	page := map[string]any{"tools": f.tools[start:end]}
	if end < len(f.tools) {
		page["nextCursor"] = fmt.Sprintf("page-%d", end)
	}
	return page
}

func (f fakeMCPToolServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != f.wantAuth {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			f.t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch message.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			writeJSON(w, http.StatusOK, map[string]any{
				"jsonrpc": "2.0", "id": message.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "weather", "version": "1.2.3", "title": "Weather"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				f.t.Errorf("tools/list missing session header")
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"jsonrpc": "2.0", "id": message.ID,
				"result": f.toolsPage(message.Params.Cursor),
			})
		default:
			f.t.Errorf("unexpected method %q", message.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
}

func mcpServerToolsTestContext() context.Context {
	return withProjectScope(
		context.Background(),
		identitystore.OrgRecord{ID: uuid.New()},
		identitystore.ProjectRecord{ID: uuid.New()},
	)
}

func mcpServerToolsTestServer(t *testing.T) *Server {
	t.Helper()
	return mustNewUnitServer(t, WithAgentConfigOptions(agentconfig.CompileOptions{AllowInsecureLocalMCPHTTP: true}))
}

func mcpServerAuthNone(t *testing.T) openapi.MCPServerAuth {
	t.Helper()
	var auth openapi.MCPServerAuth
	if err := auth.FromMCPServerAuthNone(openapi.MCPServerAuthNone{Type: "none"}); err != nil {
		t.Fatalf("build auth: %v", err)
	}
	return auth
}

func TestListMCPServerToolsReturnsDiscoveredTools(t *testing.T) {
	upstream := httptest.NewServer(fakeMCPToolServer{
		t:        t,
		pageSize: 1,
		tools: []map[string]any{
			{
				"name":        "get_forecast",
				"title":       "Get forecast",
				"description": "Forecast for a city.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
				"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false},
			},
			{"name": "bare", "inputSchema": map[string]any{"type": "object"}},
		},
	}.handler())
	defer upstream.Close()

	response, err := strictOpenAPIServer{server: mcpServerToolsTestServer(t)}.ListMCPServerTools(
		mcpServerToolsTestContext(),
		openapi.ListMCPServerToolsRequestObject{
			Body: &openapi.MCPServerToolsRequest{Url: upstream.URL + "/mcp", Auth: mcpServerAuthNone(t)},
		},
	)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	result, ok := response.(openapi.ListMCPServerTools200JSONResponse)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	if result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocol_version = %q", result.ProtocolVersion)
	}
	if result.ServerInfo.Name != "weather" || result.ServerInfo.Version != "1.2.3" ||
		result.ServerInfo.Title == nil || *result.ServerInfo.Title != "Weather" {
		t.Fatalf("server_info = %+v", result.ServerInfo)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("tools = %+v", result.Tools)
	}
	forecast := result.Tools[0]
	if forecast.Name != "get_forecast" || forecast.Title == nil || *forecast.Title != "Get forecast" ||
		forecast.Description == nil || *forecast.Description != "Forecast for a city." {
		t.Fatalf("forecast tool = %+v", forecast)
	}
	if forecast.InputSchema["type"] != "object" {
		t.Fatalf("input_schema = %+v", forecast.InputSchema)
	}
	if forecast.Annotations == nil || forecast.Annotations.ReadOnlyHint == nil || !*forecast.Annotations.ReadOnlyHint ||
		forecast.Annotations.DestructiveHint == nil || *forecast.Annotations.DestructiveHint {
		t.Fatalf("annotations = %+v", forecast.Annotations)
	}
	bare := result.Tools[1]
	if bare.Title != nil || bare.Description != nil || bare.Annotations != nil || bare.OutputSchema != nil {
		t.Fatalf("bare tool = %+v", bare)
	}
}

func TestListMCPServerToolsRejectsInvalidURL(t *testing.T) {
	for name, rawURL := range map[string]string{
		"scheme":      "ftp://example.com/mcp",
		"credentials": "https://user:pass@example.com/mcp",
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := strictOpenAPIServer{server: mcpServerToolsTestServer(t)}.ListMCPServerTools(
				mcpServerToolsTestContext(),
				openapi.ListMCPServerToolsRequestObject{
					Body: &openapi.MCPServerToolsRequest{Url: rawURL, Auth: mcpServerAuthNone(t)},
				},
			)
			var apiErr apierror.ResponseError
			if !errors.As(err, &apiErr) || apiErr.Code != openapi.ErrorCodeInvalidRequest {
				t.Fatalf("err = %v, want invalid_request", err)
			}
		})
	}
}

func TestListMCPServerToolsReportsBearerRequiredWithoutOAuthMetadata(t *testing.T) {
	upstream := httptest.NewServer(fakeMCPToolServer{t: t, wantAuth: "Bearer expected"}.handler())
	defer upstream.Close()

	response, err := strictOpenAPIServer{server: mcpServerToolsTestServer(t)}.ListMCPServerTools(
		mcpServerToolsTestContext(),
		openapi.ListMCPServerToolsRequestObject{
			Body: &openapi.MCPServerToolsRequest{Url: upstream.URL + "/mcp", Auth: mcpServerAuthNone(t)},
		},
	)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	hint, ok := response.(openapi.ListMCPServerTools422JSONResponse)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	if hint.Auth.Type != openapi.MCPServerAuthHintTypeBearer || hint.Code != "unprocessable" || hint.Error == "" {
		t.Fatalf("response = %+v, want bearer hint", hint)
	}
}

func TestListMCPServerToolsReportsOAuthRequiredFromServerMetadata(t *testing.T) {
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	issuer := upstream.URL + "/issuer"
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+upstream.URL+`/prm"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/prm", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"resource":              upstream.URL + "/mcp",
			"authorization_servers": []string{issuer},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                           issuer,
			"authorization_endpoint":           upstream.URL + "/authorize",
			"token_endpoint":                   upstream.URL + "/token",
			"response_types_supported":         []string{"code"},
			"code_challenge_methods_supported": []string{"S256"},
		})
	})

	response, err := strictOpenAPIServer{server: mcpServerToolsTestServer(t)}.ListMCPServerTools(
		mcpServerToolsTestContext(),
		openapi.ListMCPServerToolsRequestObject{
			Body: &openapi.MCPServerToolsRequest{Url: upstream.URL + "/mcp", Auth: mcpServerAuthNone(t)},
		},
	)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	hint, ok := response.(openapi.ListMCPServerTools422JSONResponse)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	if hint.Auth.Type != openapi.MCPServerAuthHintTypeOauth ||
		hint.Auth.AuthorizationServer == nil || *hint.Auth.AuthorizationServer != issuer {
		t.Fatalf("response = %+v, want oauth hint for %s", hint, issuer)
	}
}

func TestListMCPServerToolsDoesNotGuessBearerWhenProbeReturnsForbidden(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	_, err := strictOpenAPIServer{server: mcpServerToolsTestServer(t)}.ListMCPServerTools(
		mcpServerToolsTestContext(),
		openapi.ListMCPServerToolsRequestObject{
			Body: &openapi.MCPServerToolsRequest{Url: upstream.URL + "/mcp", Auth: mcpServerAuthNone(t)},
		},
	)
	var apiErr apierror.ResponseError
	if !errors.As(err, &apiErr) || apiErr.Code != openapi.ErrorCodeUpstreamError {
		t.Fatalf("err = %v, want upstream_error", err)
	}
}

func TestListMCPServerToolsReportsUpstreamFailureForNonAuthErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	_, err := strictOpenAPIServer{server: mcpServerToolsTestServer(t)}.ListMCPServerTools(
		mcpServerToolsTestContext(),
		openapi.ListMCPServerToolsRequestObject{
			Body: &openapi.MCPServerToolsRequest{Url: upstream.URL + "/mcp", Auth: mcpServerAuthNone(t)},
		},
	)
	var apiErr apierror.ResponseError
	if !errors.As(err, &apiErr) || apiErr.Code != openapi.ErrorCodeUpstreamError {
		t.Fatalf("err = %v, want upstream_error", err)
	}
}

func TestListMCPServerToolsRequiresStoreForSecretAuth(t *testing.T) {
	var auth openapi.MCPServerAuth
	if err := auth.FromMCPServerAuthBearer(openapi.MCPServerAuthBearer{
		Type: "bearer", SecretId: "sec_aaaaaaaaaaaaaaaaaaaaaaaaaa",
	}); err != nil {
		t.Fatalf("build auth: %v", err)
	}
	_, err := strictOpenAPIServer{server: mcpServerToolsTestServer(t)}.ListMCPServerTools(
		mcpServerToolsTestContext(),
		openapi.ListMCPServerToolsRequestObject{
			Body: &openapi.MCPServerToolsRequest{Url: "https://example.com/mcp", Auth: auth},
		},
	)
	var apiErr apierror.ResponseError
	if !errors.As(err, &apiErr) || apiErr.Code != openapi.ErrorCodeServiceUnavailable {
		t.Fatalf("err = %v, want service_unavailable", err)
	}
}

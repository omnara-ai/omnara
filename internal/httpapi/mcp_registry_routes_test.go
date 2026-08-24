package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/mcpregistry"
)

func TestListMCPServersProxiesFiltersAndShape(t *testing.T) {
	var gotQuery string
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[{"name":"com.github/github-mcp-server","title":"GitHub MCP Server",
			"description":"Official.","version":"2.1.0","website_url":"https://github.com","status":"active",
			"updated_at":"2026-08-20T00:00:00Z",
			"icons":[{"src":"https://github.com/icon.png","mime_type":"image/png"}],
			"remotes":[{"type":"streamable-http","url":"https://api.githubcopilot.com/mcp/",
			  "headers":[{"name":"Authorization","description":"Bearer token","is_required":true,"is_secret":true}]}]}],
			"next_cursor":"MjU"}`))
	}))
	defer registry.Close()
	client, err := mcpregistry.NewClient(registry.URL, registry.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	server := mustNewUnitServer(t, WithMCPRegistryClient(client))
	query, remoteURL := "github", "https://api.githubcopilot.com/mcp/"
	limit, cursor := openapi.PageLimit(25), openapi.PageCursor("MA")
	response, err := strictOpenAPIServer{server: server}.ListMCPServers(
		context.Background(),
		openapi.ListMCPServersRequestObject{
			Params: openapi.ListMCPServersParams{Q: &query, RemoteUrl: &remoteURL, Limit: &limit, Cursor: &cursor},
		},
	)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotQuery != "cursor=MA&limit=25&q=github&remote_url=https%3A%2F%2Fapi.githubcopilot.com%2Fmcp%2F" {
		t.Fatalf("upstream query = %q", gotQuery)
	}
	page, ok := response.(openapi.ListMCPServers200JSONResponse)
	if !ok {
		t.Fatalf("response type = %T", response)
	}
	if len(page.Data) != 1 {
		t.Fatalf("data = %+v", page.Data)
	}
	got := page.Data[0]
	if got.Name != "com.github/github-mcp-server" || got.Title == nil || *got.Title != "GitHub MCP Server" {
		t.Fatalf("server = %+v", got)
	}
	if got.WebsiteUrl == nil || *got.WebsiteUrl != "https://github.com" {
		t.Fatalf("website_url = %v", got.WebsiteUrl)
	}
	if len(got.Remotes) != 1 || got.Remotes[0].Headers == nil || (*got.Remotes[0].Headers)[0].Name != "Authorization" {
		t.Fatalf("remotes = %+v", got.Remotes)
	}
	if len(got.Icons) != 1 || got.Icons[0].Src != "https://github.com/icon.png" || got.Icons[0].Theme != nil {
		t.Fatalf("icons = %+v", got.Icons)
	}
	next, err := page.NextCursor.Get()
	if err != nil || next != "MjU" {
		t.Fatalf("next_cursor = %q, err = %v", next, err)
	}
}

func TestListMCPServersWithoutRegistryIsUnavailable(t *testing.T) {
	server := mustNewUnitServer(t)
	_, err := strictOpenAPIServer{server: server}.ListMCPServers(
		context.Background(),
		openapi.ListMCPServersRequestObject{},
	)
	var responseErr apierror.ResponseError
	if !errors.As(err, &responseErr) || responseErr.Code != openapi.ErrorCodeServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestListMCPServersMapsUpstreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   openapi.ErrorCode
	}{
		{
			name: "bad request", status: http.StatusBadRequest,
			body: `{"error":"invalid cursor"}`, code: openapi.ErrorCodeInvalidRequest,
		},
		{
			name: "upstream failure", status: http.StatusInternalServerError,
			body: `{}`, code: openapi.ErrorCodeUpstreamError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer registry.Close()
			client, err := mcpregistry.NewClient(registry.URL, registry.Client())
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			server := mustNewUnitServer(t, WithMCPRegistryClient(client))
			_, err = strictOpenAPIServer{server: server}.ListMCPServers(
				context.Background(),
				openapi.ListMCPServersRequestObject{},
			)
			var responseErr apierror.ResponseError
			if !errors.As(err, &responseErr) || responseErr.Code != tc.code {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
		})
	}
}

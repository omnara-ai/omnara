package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/mcpregistry"
)

func testMCPRegistry(t *testing.T) *mcpregistry.Registry {
	t.Helper()
	updatedAt := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	registry, err := mcpregistry.NewRegistry(mcpregistry.BuildSnapshot([]mcpregistry.Server{
		{
			Name: "com.github/github-mcp-server", Title: "GitHub MCP Server", Description: "Official.",
			Version: "2.1.0", WebsiteURL: "https://github.com", Status: "active", UpdatedAt: updatedAt,
			Icons: []mcpregistry.Icon{{Src: "https://github.com/icon.png", MimeType: "image/png"}},
			Remotes: []mcpregistry.Remote{{
				Type: "streamable-http", URL: "https://api.githubcopilot.com/mcp/",
				Headers: []mcpregistry.Header{{
					Name: "Authorization", Description: "Bearer token", IsRequired: true, IsSecret: true,
				}},
			}},
		},
		{
			Name: "io.github.alice/weather-tools", Title: "Weather Tools", Description: "Forecasts and alerts.",
			Version: "1.0.0", Status: "active", UpdatedAt: updatedAt,
			Remotes: []mcpregistry.Remote{{Type: "streamable-http", URL: "https://weather.example/mcp"}},
		},
	}, updatedAt))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func TestListMCPServersFiltersAndShape(t *testing.T) {
	server := mustNewUnitServer(t, WithMCPRegistry(testMCPRegistry(t)))
	query, remoteURL := "github", "https://api.githubcopilot.com/mcp/"
	limit := openapi.PageLimit(25)
	response, err := strictOpenAPIServer{server: server}.ListMCPServers(
		context.Background(),
		openapi.ListMCPServersRequestObject{
			Params: openapi.ListMCPServersParams{Q: &query, RemoteUrl: &remoteURL, Limit: &limit},
		},
	)
	if err != nil {
		t.Fatalf("list: %v", err)
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
	if !page.NextCursor.IsNull() {
		next, _ := page.NextCursor.Get()
		t.Fatalf("next_cursor = %q, want null", next)
	}
}

func TestListMCPServersPaginates(t *testing.T) {
	server := mustNewUnitServer(t, WithMCPRegistry(testMCPRegistry(t)))
	limit := openapi.PageLimit(1)
	first, err := strictOpenAPIServer{server: server}.ListMCPServers(
		context.Background(),
		openapi.ListMCPServersRequestObject{Params: openapi.ListMCPServersParams{Limit: &limit}},
	)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	firstPage, ok := first.(openapi.ListMCPServers200JSONResponse)
	if !ok {
		t.Fatalf("first response type = %T", first)
	}
	cursor, err := firstPage.NextCursor.Get()
	if err != nil || cursor == "" {
		t.Fatalf("next_cursor = %q, err = %v", cursor, err)
	}
	second, err := strictOpenAPIServer{server: server}.ListMCPServers(
		context.Background(),
		openapi.ListMCPServersRequestObject{Params: openapi.ListMCPServersParams{Limit: &limit, Cursor: &cursor}},
	)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	secondPage, ok := second.(openapi.ListMCPServers200JSONResponse)
	if !ok {
		t.Fatalf("second response type = %T", second)
	}
	if len(firstPage.Data) != 1 || len(secondPage.Data) != 1 || firstPage.Data[0].Name == secondPage.Data[0].Name {
		t.Fatalf("pages = %+v / %+v", firstPage.Data, secondPage.Data)
	}
	if !secondPage.NextCursor.IsNull() {
		t.Fatalf("second page should be last")
	}
}

func TestListMCPServersWithoutSnapshotIsInternalError(t *testing.T) {
	server := mustNewUnitServer(t)
	_, err := strictOpenAPIServer{server: server}.ListMCPServers(
		context.Background(),
		openapi.ListMCPServersRequestObject{},
	)
	var responseErr apierror.ResponseError
	if !errors.As(err, &responseErr) || responseErr.Code != openapi.ErrorCodeInternalError {
		t.Fatalf("err = %v", err)
	}
}

func TestListMCPServersRejectsInvalidCursor(t *testing.T) {
	server := mustNewUnitServer(t, WithMCPRegistry(testMCPRegistry(t)))
	cursor := openapi.PageCursor("not-a-cursor!")
	_, err := strictOpenAPIServer{server: server}.ListMCPServers(
		context.Background(),
		openapi.ListMCPServersRequestObject{Params: openapi.ListMCPServersParams{Cursor: &cursor}},
	)
	var responseErr apierror.ResponseError
	if !errors.As(err, &responseErr) || responseErr.Code != openapi.ErrorCodeInvalidRequest {
		t.Fatalf("err = %v", err)
	}
}

package mcpregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "registry.sqlite"), false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func fixtureServers() []Server {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	return []Server{
		{
			Name: "io.github.alice/weather-tools", Title: "Weather Tools", Description: "Forecasts and alerts.",
			Version: "1.0.0", Status: "active", UpdatedAt: now,
			Remotes: []Remote{{Type: "streamable-http", URL: "https://weather.example/mcp"}},
		},
		{
			Name: "com.github/github-mcp-server", Title: "GitHub MCP Server", Description: "Official GitHub server.",
			Version: "2.1.0", Status: "active", UpdatedAt: now,
			Icons: []Icon{{Src: "https://github.com/icon.png", MimeType: "image/png", Sizes: []string{"64x64"}}},
			Remotes: []Remote{{
				Type: "streamable-http", URL: "https://api.githubcopilot.com/mcp/",
				Headers: []Header{{Name: "Authorization", Description: "Bearer token", IsRequired: true, IsSecret: true}},
			}},
		},
		{
			Name: "io.github.bob/notes", Title: "Notes", Description: "Local note taking over stdio.",
			Version: "0.3.0", Status: "active", UpdatedAt: now,
		},
	}
}

func TestStoreSearchRanksTitleMatchesFirst(t *testing.T) {
	store := openTestStore(t)
	if err := store.Replace(context.Background(), fixtureServers()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	page, err := store.Search(context.Background(), SearchParams{Query: "github"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Servers) != 3 {
		t.Fatalf("servers = %d, want 3", len(page.Servers))
	}
	if page.Servers[0].Name != "com.github/github-mcp-server" {
		t.Fatalf("first result = %s, want com.github/github-mcp-server", page.Servers[0].Name)
	}
	if page.NextCursor != nil {
		t.Fatalf("next cursor = %q, want nil", *page.NextCursor)
	}
	headers := page.Servers[0].Remotes[0].Headers
	if len(headers) != 1 || !headers[0].IsSecret || headers[0].Name != "Authorization" {
		t.Fatalf("headers = %+v", headers)
	}
	icons := page.Servers[0].Icons
	if len(icons) != 1 || icons[0].Src != "https://github.com/icon.png" ||
		len(icons[0].Sizes) != 1 || icons[0].Sizes[0] != "64x64" {
		t.Fatalf("icons = %+v", icons)
	}
	if page.Servers[1].Icons == nil {
		t.Fatal("icons should decode to an empty slice")
	}
}

func TestStoreSearchPrefixAndPagination(t *testing.T) {
	store := openTestStore(t)
	if err := store.Replace(context.Background(), fixtureServers()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	first, err := store.Search(context.Background(), SearchParams{Query: "weath", Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(first.Servers) != 1 || first.Servers[0].Name != "io.github.alice/weather-tools" {
		t.Fatalf("servers = %+v", first.Servers)
	}
	all, err := store.Search(context.Background(), SearchParams{Limit: 2})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(all.Servers) != 2 || all.NextCursor == nil {
		t.Fatalf("page 1 = %+v", all)
	}
	rest, err := store.Search(context.Background(), SearchParams{Limit: 2, Cursor: *all.NextCursor})
	if err != nil {
		t.Fatalf("search page 2: %v", err)
	}
	if len(rest.Servers) != 1 || rest.NextCursor != nil {
		t.Fatalf("page 2 = %+v", rest)
	}
	if rest.Servers[0].Remotes == nil {
		t.Fatal("remotes should decode to an empty slice")
	}
	_, err = store.Search(context.Background(), SearchParams{Cursor: "not-a-cursor"})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("invalid cursor error = %v", err)
	}
}

func TestFinalizedStoreOpensReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.sqlite")
	writer, err := OpenStore(context.Background(), path, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := writer.Replace(context.Background(), fixtureServers()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := writer.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("wal file should be gone: %v", err)
	}
	reader, err := OpenStore(context.Background(), path, true)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() { _ = reader.Close() }()
	count, err := reader.Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	if err := reader.Replace(context.Background(), nil); err == nil {
		t.Fatal("read-only store should reject writes")
	}
}

func TestStoreSearchMatchesRemoteURLTokens(t *testing.T) {
	store := openTestStore(t)
	if err := store.Replace(context.Background(), fixtureServers()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	for _, query := range []string{"githubcopilot", "copilot", "api.githubcopilot.com"} {
		page, err := store.Search(context.Background(), SearchParams{Query: query})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(page.Servers) == 0 || page.Servers[0].Name != "com.github/github-mcp-server" {
			t.Fatalf("servers for %q = %+v", query, page.Servers)
		}
	}
}

func TestStoreSearchFiltersByRemoteURL(t *testing.T) {
	store := openTestStore(t)
	if err := store.Replace(context.Background(), fixtureServers()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	for _, needle := range []string{
		"https://api.githubcopilot.com/mcp",
		"HTTP://API.GITHUBCOPILOT.COM/MCP/",
		"api.githubcopilot.com/mcp",
	} {
		page, err := store.Search(context.Background(), SearchParams{RemoteURL: needle})
		if err != nil {
			t.Fatalf("search %q: %v", needle, err)
		}
		if len(page.Servers) != 1 || page.Servers[0].Name != "com.github/github-mcp-server" {
			t.Fatalf("servers for %q = %+v", needle, page.Servers)
		}
	}
	for _, partial := range []string{"%", "api.githubcopilot.com", "githubcopilot", "https://api.githubcopilot.com/mcp/extra"} {
		page, err := store.Search(context.Background(), SearchParams{RemoteURL: partial})
		if err != nil {
			t.Fatalf("search %q: %v", partial, err)
		}
		if len(page.Servers) != 0 {
			t.Fatalf("remote_url must match exactly, %q returned %+v", partial, page.Servers)
		}
	}
	combined, err := store.Search(context.Background(), SearchParams{
		Query: "weather", RemoteURL: "https://api.githubcopilot.com/mcp/",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(combined.Servers) != 0 {
		t.Fatalf("servers = %+v, want none", combined.Servers)
	}
	missing, err := store.Search(context.Background(), SearchParams{RemoteURL: "https://nowhere.example/mcp"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(missing.Servers) != 0 {
		t.Fatalf("servers = %+v, want none", missing.Servers)
	}
}

func TestStoreReplaceIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	for range 2 {
		if err := store.Replace(context.Background(), fixtureServers()); err != nil {
			t.Fatalf("replace: %v", err)
		}
	}
	count, err := store.Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func TestFTSQueryEscapesOperators(t *testing.T) {
	if got := ftsQuery(`git-hub OR "x"`); got != `"git"* "hub"* "OR"* "x"*` {
		t.Fatalf("ftsQuery = %q", got)
	}
	if got := ftsQuery("  "); got != "" {
		t.Fatalf("ftsQuery blank = %q", got)
	}
}

func upstreamFixture(t *testing.T) *httptest.Server {
	t.Helper()
	pages := map[string]string{
		"": `{"servers":[
			{"server":{"name":"io.github.alice/weather-tools","title":"Weather Tools",
			  "description":"Forecasts.","version":"0.9.0",
			  "remotes":[{"type":"sse","url":"https://old.example/sse"}]},
			 "_meta":{"io.modelcontextprotocol.registry/official":{"status":"active","updatedAt":"2026-08-01T00:00:00Z","isLatest":false}}},
			{"server":{"name":"io.github.alice/weather-tools","title":"Weather Tools",
			  "description":"Forecasts.","version":"1.0.0","websiteUrl":"https://weather.example",
			  "icons":[{"src":"https://weather.example/icon.svg","mimeType":"image/svg+xml","sizes":["any"],"theme":"light"},{"src":""}],
			  "remotes":[{"type":"streamable-http","url":"https://weather.example/mcp",
			    "headers":[{"name":"X-API-Key","description":"key","isRequired":true,"isSecret":true}]}]},
			 "_meta":{"io.modelcontextprotocol.registry/official":{"status":"active","updatedAt":"2026-08-20T00:00:00Z","isLatest":true}}}
		],"metadata":{"nextCursor":"page2","count":2}}`,
		"page2": `{"servers":[
			{"server":{"name":"io.github.bob/notes","description":"Notes.","version":"0.3.0",
			  "packages":[{"registryType":"npm"}]},
			 "_meta":{"io.modelcontextprotocol.registry/official":{"status":"active","updatedAt":"2026-08-21T00:00:00Z","isLatest":true}}}
		],"metadata":{"count":1}}`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0.1/servers" || r.URL.Query().Get("version") != "latest" {
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		body, ok := pages[r.URL.Query().Get("cursor")]
		if !ok {
			http.Error(w, "unknown cursor", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestSyncerFetchKeepsOnlyLatestVersions(t *testing.T) {
	upstream := upstreamFixture(t)
	defer upstream.Close()
	servers, err := Syncer{UpstreamURL: upstream.URL, HTTPClient: upstream.Client()}.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %d, want 2: %+v", len(servers), servers)
	}
	weather := servers[0]
	if weather.Version != "1.0.0" || weather.WebsiteURL != "https://weather.example" || weather.Status != "active" {
		t.Fatalf("weather = %+v", weather)
	}
	if len(weather.Remotes) != 1 || weather.Remotes[0].Headers[0].Name != "X-API-Key" {
		t.Fatalf("remotes = %+v", weather.Remotes)
	}
	icon := weather.Icons[0]
	if len(weather.Icons) != 1 || icon.Theme != "light" || icon.Sizes[0] != "any" {
		t.Fatalf("icons = %+v", weather.Icons)
	}
	if !weather.UpdatedAt.Equal(time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("updated_at = %s", weather.UpdatedAt)
	}
	if len(servers[1].Remotes) != 0 {
		t.Fatalf("notes remotes = %+v", servers[1].Remotes)
	}
}

func TestHandlerAndClientRoundTrip(t *testing.T) {
	store := openTestStore(t)
	if err := store.Replace(context.Background(), fixtureServers()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	service := httptest.NewServer(NewHandler(store))
	defer service.Close()

	client, err := NewClient(service.URL, service.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	byURL, err := client.Search(context.Background(), SearchParams{RemoteURL: "https://weather.example/mcp/"})
	if err != nil {
		t.Fatalf("search by remote url: %v", err)
	}
	if len(byURL.Servers) != 1 || byURL.Servers[0].Name != "io.github.alice/weather-tools" {
		t.Fatalf("servers by url = %+v", byURL.Servers)
	}
	page, err := client.Search(context.Background(), SearchParams{Query: "notes", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Servers) != 1 || page.Servers[0].Name != "io.github.bob/notes" {
		t.Fatalf("servers = %+v", page.Servers)
	}
	if page.Servers[0].Remotes == nil {
		t.Fatal("remotes should be an empty slice")
	}

	resp, err := http.Get(service.URL + ServersPath + "?limit=0")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure errorBody
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil || failure.Error == "" {
		t.Fatalf("error body = %+v, err = %v", failure, err)
	}
	if _, err := client.Search(context.Background(), SearchParams{Cursor: "bad"}); err == nil {
		t.Fatal("expected bad request error")
	}

	health, err := http.Get(service.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", health.StatusCode)
	}
}

func TestClientReportsUnavailableUpstream(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer service.Close()
	client, err := NewClient(service.URL, service.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Search(context.Background(), SearchParams{})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, err := NewClient("mcp-registry:8090", nil); err == nil {
		t.Fatal("expected relative url rejection")
	}
}

package mcpregistry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func fixtureRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := NewRegistry(BuildSnapshot(fixtureServers(), time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func names(page SearchPage) []string {
	out := make([]string, 0, len(page.Servers))
	for _, server := range page.Servers {
		out = append(out, server.Name)
	}
	return out
}

func TestSearchRanksTitleMatchesFirst(t *testing.T) {
	page, err := fixtureRegistry(t).Search(SearchParams{Query: "github"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := names(page)
	if len(got) != 3 || got[0] != "com.github/github-mcp-server" {
		t.Fatalf("names = %v", got)
	}
}

func TestSearchRequiresEveryTermAsTokenPrefix(t *testing.T) {
	registry := fixtureRegistry(t)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{query: "weath", want: []string{"io.github.alice/weather-tools"}},
		{query: "weather alerts", want: []string{"io.github.alice/weather-tools"}},
		{query: "ather", want: nil},
		{query: "weather.example", want: []string{"io.github.alice/weather-tools"}},
		{query: "weather notes", want: nil},
		{
			query: "",
			want:  []string{"com.github/github-mcp-server", "io.github.alice/weather-tools", "io.github.bob/notes"},
		},
	} {
		page, err := registry.Search(SearchParams{Query: tc.query})
		if err != nil {
			t.Fatalf("%q: %v", tc.query, err)
		}
		got := names(page)
		if len(got) != len(tc.want) {
			t.Fatalf("%q: names = %v, want %v", tc.query, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%q: names = %v, want %v", tc.query, got, tc.want)
			}
		}
	}
}

func TestSearchMatchesRemoteURLQueries(t *testing.T) {
	registry := fixtureRegistry(t)
	queries := []string{"https://api.githubcopilot.com/mcp/", "API.githubcopilot.com/mcp", "githubcopilot.com"}
	for _, query := range queries {
		page, err := registry.Search(SearchParams{Query: query})
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		got := names(page)
		if len(got) == 0 || got[0] != "com.github/github-mcp-server" {
			t.Fatalf("%q: names = %v", query, got)
		}
	}
}

func TestSearchFiltersByExactRemoteURL(t *testing.T) {
	registry := fixtureRegistry(t)
	page, err := registry.Search(SearchParams{RemoteURL: "HTTP://weather.example/mcp/"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := names(page); len(got) != 1 || got[0] != "io.github.alice/weather-tools" {
		t.Fatalf("names = %v", got)
	}
	page, err = registry.Search(SearchParams{RemoteURL: "weather.example/mcp", Query: "notes"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Servers) != 0 {
		t.Fatalf("names = %v, want none", names(page))
	}
	page, err = registry.Search(SearchParams{RemoteURL: "missing.example/mcp"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Servers) != 0 {
		t.Fatalf("names = %v, want none", names(page))
	}
}

func TestSearchPaginatesWithCursor(t *testing.T) {
	registry := fixtureRegistry(t)
	first, err := registry.Search(SearchParams{Limit: 2})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(first.Servers) != 2 || first.NextCursor == nil {
		t.Fatalf("first = %v cursor %v", names(first), first.NextCursor)
	}
	second, err := registry.Search(SearchParams{Limit: 2, Cursor: *first.NextCursor})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second.Servers) != 1 || second.NextCursor != nil || second.Servers[0].Name != "io.github.bob/notes" {
		t.Fatalf("second = %v cursor %v", names(second), second.NextCursor)
	}
	if _, err := registry.Search(SearchParams{Cursor: "%%%"}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("err = %v", err)
	}
	if _, err := registry.Search(SearchParams{Cursor: encodeCursor(-1)}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchClampsLimit(t *testing.T) {
	registry := fixtureRegistry(t)
	if got := normalizeLimit(0); got != DefaultSearchLimit {
		t.Fatalf("limit 0 = %d", got)
	}
	if got := normalizeLimit(MaxSearchLimit + 1); got != MaxSearchLimit {
		t.Fatalf("limit over max = %d", got)
	}
	page, err := registry.Search(SearchParams{Limit: MaxSearchLimit + 1})
	if err != nil || len(page.Servers) != 3 {
		t.Fatalf("page = %v err = %v", names(page), err)
	}
}

func TestSnapshotRoundTripsThroughFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp-registry.json")
	generatedAt := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	if err := WriteSnapshot(path, BuildSnapshot(fixtureServers(), generatedAt)); err != nil {
		t.Fatalf("write: %v", err)
	}
	registry, err := LoadSnapshot(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if registry.Len() != 3 || !registry.GeneratedAt().Equal(generatedAt) {
		t.Fatalf("len = %d generated_at = %v", registry.Len(), registry.GeneratedAt())
	}
	page, err := registry.Search(SearchParams{Query: "github"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := page.Servers[0]
	if got.Name != "com.github/github-mcp-server" || len(got.Remotes) != 1 ||
		len(got.Remotes[0].Headers) != 1 || len(got.Icons) != 1 {
		t.Fatalf("server = %+v", got)
	}
	if notes := page.Servers[2]; notes.Remotes == nil || notes.Icons == nil {
		t.Fatalf("expected empty slices, got %+v", notes)
	}
	if _, err := os.Stat(path + ".staging"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging file left behind: %v", err)
	}
}

func TestLoadSnapshotErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSnapshot(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(corrupt); err == nil {
		t.Fatal("corrupt snapshot loaded")
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"servers":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(empty); !errors.Is(err, errEmptySnapshot) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := NewRegistry(Snapshot{
		Servers:        []SnapshotServer{{}},
		RemoteURLIndex: map[string][]int{"x": {5}},
	}); err == nil {
		t.Fatal("out of range index accepted")
	}
}

func TestNormalizeRemoteURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://MCP.Linear.app/mcp/": "mcp.linear.app/mcp",
		"  http://a.example//  ":      "a.example",
		"a.example/mcp":               "a.example/mcp",
		"":                            "",
	} {
		if got := normalizeRemoteURL(raw); got != want {
			t.Fatalf("normalizeRemoteURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

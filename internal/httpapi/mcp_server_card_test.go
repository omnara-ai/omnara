package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
)

const serverCardTestPublicURL = "https://omnara.test"

func serveServerCardRequest(
	t *testing.T, server *Server, method, target string, headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, serverCardTestPublicURL+target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func assertServerCardCORS(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"Access-Control-Allow-Origin":   "*",
		"Access-Control-Allow-Methods":  "GET",
		"Access-Control-Allow-Headers":  "Content-Type, If-None-Match",
		"Access-Control-Expose-Headers": "ETag",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestMCPServerCardRoute(t *testing.T) {
	server := mustNewUnitServer(t, WithPublicURL(serverCardTestPublicURL))
	rec := serveServerCardRequest(t, server, http.MethodGet, mcpServerCardPath, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != mcpServerCardMediaType {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
	assertServerCardCORS(t, rec)

	var card apimcp.ServerCard
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Schema != "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json" {
		t.Fatalf("$schema = %q", card.Schema)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`).MatchString(card.Name) {
		t.Fatalf("name %q does not match the server card name pattern", card.Name)
	}
	if n := len(card.Description); n < 1 || n > 100 {
		t.Fatalf("description length = %d, want 1..100", n)
	}
	if card.Version == "" {
		t.Fatal("version is empty")
	}
	if len(card.Remotes) != 1 {
		t.Fatalf("remotes = %+v", card.Remotes)
	}
	remote := card.Remotes[0]
	if remote.Type != "streamable-http" || remote.URL != serverCardTestPublicURL+"/mcp" {
		t.Fatalf("remote = %+v", remote)
	}
	if !slices.Equal(remote.SupportedProtocolVersions, mcp.SupportedProtocolVersions()) {
		t.Fatalf("supportedProtocolVersions = %v", remote.SupportedProtocolVersions)
	}

	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	conditional := map[string]string{"If-None-Match": etag}
	cached := serveServerCardRequest(t, server, http.MethodGet, mcpServerCardPath, conditional)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("conditional GET status = %d body=%q", cached.Code, cached.Body.String())
	}
}

func TestMCPServerCardMatchesRegistryServerJSON(t *testing.T) {
	raw, err := os.ReadFile("../../server.json")
	if err != nil {
		t.Fatalf("read server.json: %v", err)
	}
	var registry struct {
		Name        string                      `json:"name"`
		Title       string                      `json:"title"`
		Description string                      `json:"description"`
		Version     string                      `json:"version"`
		WebsiteURL  string                      `json:"websiteUrl"`
		Repository  apimcp.ServerCardRepository `json:"repository"`
		Icons       []mcp.Icon                  `json:"icons"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatalf("decode server.json: %v", err)
	}
	server := mustNewUnitServer(t, WithPublicURL(serverCardTestPublicURL))
	card, ok := server.mcpServerCard(httptest.NewRequest(http.MethodGet, serverCardTestPublicURL+mcpServerCardPath, nil))
	if !ok {
		t.Fatal("card unavailable")
	}
	if card.Name != registry.Name || card.Title != registry.Title || card.Description != registry.Description ||
		card.Version != registry.Version || card.WebsiteURL != registry.WebsiteURL ||
		*card.Repository != registry.Repository || len(card.Icons) != len(registry.Icons) ||
		card.Icons[0].Source != registry.Icons[0].Source {
		t.Fatalf("card identity %+v differs from server.json %+v", card, registry)
	}
}

func TestMCPServerCardPreflight(t *testing.T) {
	server := mustNewUnitServer(t, WithPublicURL(serverCardTestPublicURL))
	rec := serveServerCardRequest(t, server, http.MethodOptions, mcpServerCardPath, map[string]string{
		"Origin":                         "https://client.example",
		"Access-Control-Request-Method":  "GET",
		"Access-Control-Request-Headers": "if-none-match",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d body=%s", rec.Code, rec.Body.String())
	}
	assertServerCardCORS(t, rec)
}

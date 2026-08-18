package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var wantSPACSP = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self' 'sha256-4Z+0IbR8cDetVQawCZYyJN7DAZJUmjFGeS+nwKwqD8c='",
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data:",
	"font-src 'self' data:",
	"connect-src 'self'",
	"object-src 'none'",
	"base-uri 'none'",
	"frame-ancestors 'none'",
}, "; ")

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func spaAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(`<!doctype html><div id="root"></div>`)},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		".gitkeep":             {Data: []byte("placeholder")},
		"favicon.svg":          {Data: []byte("<svg/>")},
	}
}

func TestWebIndexAvailable(t *testing.T) {
	if webIndexAvailable(nil) {
		t.Fatal("nil assets should be unavailable")
	}
	if webIndexAvailable(fstest.MapFS{"other.txt": {Data: []byte("x")}}) {
		t.Fatal("assets without index.html should be unavailable")
	}
	if !webIndexAvailable(spaAssets()) {
		t.Fatal("assets with index.html should be available")
	}
}

func TestServerServesSPA(t *testing.T) {
	srv, err := New(
		discardLogger(),
		nil,
		WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
		WithWebAssets(spaAssets()),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	root := get(http.MethodGet, "/")
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), `id="root"`) {
		t.Fatalf("GET / did not serve index.html: %d %s", root.Code, root.Body.String())
	}
	if cc := root.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", cc)
	}
	if csp := root.Header().Get("Content-Security-Policy"); csp != wantSPACSP {
		t.Fatalf("index Content-Security-Policy = %q, want %q", csp, wantSPACSP)
	}
	if nosniff := root.Header().Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Fatalf("index X-Content-Type-Options = %q, want nosniff", nosniff)
	}
	if referrerPolicy := root.Header().Get("Referrer-Policy"); referrerPolicy != "no-referrer" {
		t.Fatalf("index Referrer-Policy = %q, want no-referrer", referrerPolicy)
	}

	asset := get(http.MethodGet, "/assets/app-abc123.js")
	if asset.Code != http.StatusOK {
		t.Fatalf("GET asset status = %d", asset.Code)
	}
	if cc := asset.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("asset Cache-Control = %q, want immutable", cc)
	}
	if nosniff := asset.Header().Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Fatalf("asset X-Content-Type-Options = %q, want nosniff", nosniff)
	}

	deep := get(http.MethodGet, "/orgs/abc/projects")
	if deep.Code != http.StatusOK || !strings.Contains(deep.Body.String(), `id="root"`) {
		t.Fatalf("deep link did not fall back to index.html: %d %s", deep.Code, deep.Body.String())
	}

	spec := get(http.MethodGet, "/api/openapi.yaml")
	if spec.Code != http.StatusOK || !strings.Contains(spec.Header().Get("Content-Type"), "yaml") {
		t.Fatalf("GET /api/openapi.yaml = %d %q", spec.Code, spec.Header().Get("Content-Type"))
	}

	post := get(http.MethodPost, "/orgs/abc/projects")
	if post.Code != http.StatusNotFound {
		t.Fatalf("POST unmatched status = %d, want 404", post.Code)
	}
	if strings.Contains(post.Body.String(), `id="root"`) {
		t.Fatalf("POST unmatched returned the HTML shell")
	}

	for _, path := range []string{"/assets", "/assets/missing.js", "/missing.ico", "/.gitkeep", "/orgs/.hidden/page"} {
		rec := get(http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `id="root"`) {
			t.Fatalf("GET %s returned the HTML shell", path)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", path, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestSPAReservedNamespacesReturnJSON404(t *testing.T) {
	srv, err := New(
		discardLogger(),
		nil,
		WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
		WithWebAssets(spaAssets()),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	for _, path := range []string{
		"/api/v1",
		"/api/auth",
		"/api/auth/does-not-exist",
		"/api/mcp-oauth",
		"/api/mcp-oauth/does-not-exist",
		"/install",
		"/install/does-not-exist",
		"/.well-known",
		"/.well-known/does-not-exist",
		"/api/integrations",
		"/api/integrations/does-not-exist",
	} {
		rec := get(path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `id="root"`) {
			t.Fatalf("GET %s returned the HTML shell instead of a JSON 404: %s", path, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("GET %s Content-Type = %q, want application/json", path, ct)
		}
	}

	deep := get("/orgs/abc/agents")
	if deep.Code != http.StatusOK || !strings.Contains(deep.Body.String(), `id="root"`) {
		t.Fatalf("non-reserved deep link did not fall back to index.html: %d %s", deep.Code, deep.Body.String())
	}
}

func TestConfiguredPublicURLRejectsUnknownHost(t *testing.T) {
	srv, err := New(discardLogger(), nil,
		WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
		WithPublicURL(" https://omnara.test/ "),
		WithWebAssets(spaAssets()),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	handler := srv.Handler()

	for _, tc := range []struct {
		name string
		url  string
		want int
	}{
		{name: "configured host", url: "https://omnara.test/", want: http.StatusOK},
		{name: "configured host default port", url: "https://omnara.test:443/", want: http.StatusOK},
		{name: "unknown host", url: "https://unknown.test/", want: http.StatusNotFound},
		{name: "unknown spec host", url: "https://unknown.test/api/openapi.yaml", want: http.StatusNotFound},
		{
			name: "unknown well-known host",
			url:  "https://unknown.test/.well-known/oauth-client.json",
			want: http.StatusNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != tc.want {
				t.Fatalf("GET %s status=%d want=%d body=%s", tc.url, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestServerWithoutWebIndexKeepsAPIOnlyRoot(t *testing.T) {
	srv, err := New(
		discardLogger(),
		nil,
		WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
		WithWebAssets(fstest.MapFS{".gitkeep": {Data: []byte("x")}}),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected API-only blank 404 root, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("served SPA shell without an index.html")
	}
}

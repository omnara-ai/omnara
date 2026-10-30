package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestExternalLoginServerErrorRedirectsToLogin(t *testing.T) {
	handler := New(Config{Store: failingConnectorStore{}, PublicURL: "https://example.com"})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	for path, want := range map[string]string{
		"/api/auth/connectors/github/login?return_to=/after": "/login?auth_connector=github&auth_error=server_error&return_to=%2Fafter",
		"/api/auth/connectors/github/callback":               "/login?auth_connector=github&auth_error=server_error",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://example.com"+path, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Fatalf("%s: status=%d location=%q, want 302 %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}

type failingConnectorStore struct{ Store }

func (failingConnectorStore) GetEnabledAuthConnectorBySlug(
	context.Context, string,
) (identitystore.AuthConnectorRecord, error) {
	return identitystore.AuthConnectorRecord{}, errors.New("database unavailable")
}

func TestHTTPClientWithoutRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	response, err := httpClientWithoutRedirects(source.Client()).Get(source.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusTemporaryRedirect)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirected requests = %d, want 0", redirectedRequests.Load())
	}
}

func TestRedirectAuthOutcome(t *testing.T) {
	cases := []struct {
		name     string
		returnTo string
		values   url.Values
		want     string
	}{
		{name: "success", returnTo: "/device?user_code=ABCD", want: "/device?user_code=ABCD"},
		{
			name:     "error",
			returnTo: "/device?user_code=ABCD",
			values:   url.Values{"auth_error": []string{"identity_failed"}},
			want:     "/login?auth_error=identity_failed&return_to=%2Fdevice%3Fuser_code%3DABCD",
		},
		{
			name:     "error without return target",
			returnTo: "/",
			values:   url.Values{"auth_error": []string{"access_denied"}},
			want:     "/login?auth_error=access_denied",
		},
		{name: "unsafe return target", returnTo: "//evil.example", want: "/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://omnara.test/callback", nil)
			redirectAuthOutcome(rec, req, tc.returnTo, tc.values)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("location = %q, want %q", got, tc.want)
			}
		})
	}
}

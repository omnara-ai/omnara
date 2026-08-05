package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

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

package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowserSessionCookieRejectsNonHostFallbackForHTTPS(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://omnara.test/api/v1/orgs", nil)
	req.AddCookie(&http.Cookie{Name: BrowserSessionCookieName, Value: "session-token"})
	if _, err := BrowserSessionCookie(req, "https://omnara.test"); !errors.Is(err, http.ErrNoCookie) {
		t.Fatalf("non-host HTTPS cookie error=%v, want ErrNoCookie", err)
	}

	req.AddCookie(&http.Cookie{Name: BrowserSessionHostCookieName, Value: "host-session-token"})
	cookie, err := BrowserSessionCookie(req, "https://omnara.test")
	if err != nil {
		t.Fatalf("host HTTPS cookie: %v", err)
	}
	if cookie.Value != "host-session-token" {
		t.Fatalf("host HTTPS cookie value=%q", cookie.Value)
	}
}

func TestBrowserSessionCookieAllowsDevFallbackForHTTP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://omnara.test/api/v1/orgs", nil)
	req.AddCookie(&http.Cookie{Name: BrowserSessionCookieName, Value: "session-token"})
	cookie, err := BrowserSessionCookie(req, "http://omnara.test")
	if err != nil {
		t.Fatalf("HTTP fallback cookie: %v", err)
	}
	if cookie.Value != "session-token" {
		t.Fatalf("HTTP fallback cookie value=%q", cookie.Value)
	}
}

func TestSetLastLoginMethodCookie(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		publicURL string
		secure    bool
	}{
		{name: "http", publicURL: "http://omnara.test", secure: false},
		{name: "https", publicURL: "https://omnara.test", secure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "http://internal/api/auth/login", nil)
			setLastLoginMethodCookie(rec, req, test.publicURL, "connector:github", now)

			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("cookie count=%d want 1", len(cookies))
			}
			cookie := cookies[0]
			if cookie.Name != lastLoginMethodCookieName || cookie.Value != "connector:github" {
				t.Fatalf("cookie=%+v", cookie)
			}
			if cookie.Path != "/" || cookie.HttpOnly || cookie.Secure != test.secure ||
				cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie attributes=%+v", cookie)
			}
			if !cookie.Expires.Equal(now.Add(lastLoginMethodCookieTTL)) {
				t.Fatalf("cookie expiry=%v", cookie.Expires)
			}
		})
	}
}

func TestOAuthFlowCookieRejectsNonHostFallbackForHTTPS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/github/callback", nil)
	req.AddCookie(&http.Cookie{Name: oauthFlowCookieName, Value: "binding-token"})
	if _, err := oauthFlowCookie(req, "https://omnara.test"); !errors.Is(err, http.ErrNoCookie) {
		t.Fatalf("non-host HTTPS oauth cookie error=%v, want ErrNoCookie", err)
	}

	req.AddCookie(&http.Cookie{Name: oauthFlowHostCookieName, Value: "host-binding-token"})
	cookie, err := oauthFlowCookie(req, "https://omnara.test")
	if err != nil {
		t.Fatalf("host HTTPS oauth cookie: %v", err)
	}
	if cookie.Value != "host-binding-token" {
		t.Fatalf("host HTTPS oauth cookie value=%q", cookie.Value)
	}
}

func TestSameOriginUsesPublicURLHostWhenConfigured(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://internal:8080/api/auth/login", nil)
	req.Host = "internal:8080"
	req.Header.Set("Origin", "https://omnara.example")

	if !SameOrigin("https://omnara.example", req) {
		t.Fatal("expected public URL origin to match despite internal request host")
	}

	req.Header.Set("Origin", "https://evil.example")
	if SameOrigin("https://omnara.example", req) {
		t.Fatal("expected different origin host to fail")
	}
}

func TestSameOriginCanonicalizesHosts(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://internal:8080/api/auth/login", nil)
	req.Host = "internal:8080"
	for _, tc := range []struct {
		name      string
		publicURL string
		origin    string
	}{
		{name: "https default port", publicURL: "https://omnara.example", origin: "https://omnara.example:443"},
		{name: "http default port", publicURL: "http://omnara.example", origin: "http://omnara.example:80"},
		{name: "trailing dot", publicURL: "https://omnara.example.", origin: "https://omnara.example"},
		{name: "case", publicURL: "https://Omnara.Example", origin: "https://omnara.example"},
		{name: "ipv6 default port", publicURL: "https://[::1]", origin: "https://[::1]:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req.Header.Set("Origin", tc.origin)
			if !SameOrigin(tc.publicURL, req) {
				t.Fatalf("expected %q to match %q", tc.origin, tc.publicURL)
			}
		})
	}
}

func TestSameOriginFallsBackToRequestHostWithoutPublicURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://omnara.test/api/auth/login", nil)
	req.Header.Set("Referer", "http://omnara.test/settings")

	if !SameOrigin("", req) {
		t.Fatal("expected request host referer to match without public URL")
	}
}

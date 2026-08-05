package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/httporigin"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (h *Handler) logoutRoute(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return
	}
	if cookie, err := BrowserSessionCookie(r, h.publicURL); err == nil {
		if err := h.store.RevokeBrowserSession(
			r.Context(),
			cookie.Value,
		); err != nil &&
			!errors.Is(err, storeerr.ErrUnauthorized) {
			apierror.Write(w, openapi.ErrorCodeInvalidRequest, err.Error())
			return
		}
	}
	clearBrowserSessionCookies(w, r, h.publicURL)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

const (
	BrowserSessionCookieName     = "omnara_session"
	BrowserSessionHostCookieName = "__Host-omnara_session"
	CSRFCookieName               = "omnara_csrf"
	CSRFHostCookieName           = "__Host-omnara_csrf"
	CSRFHeaderName               = "X-Omnara-Csrf"
	lastLoginMethodCookieName    = "omnara_last_login_method"
	lastLoginMethodCookieTTL     = 365 * 24 * time.Hour
)

func SetBrowserSessionCookies(
	w http.ResponseWriter,
	r *http.Request,
	publicURL, sessionToken, csrfToken string,
	ttl time.Duration,
) {
	secure := requestScheme(publicURL, r) == "https"
	maxAge := int(ttl / time.Second)
	sessionName := BrowserSessionCookieName
	csrfName := CSRFCookieName
	if secure {
		sessionName = BrowserSessionHostCookieName
		csrfName = CSRFHostCookieName
	}
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     sessionName,
			Value:    sessionToken,
			Path:     "/",
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   maxAge,
		},
	)
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     csrfName,
			Value:    csrfToken,
			Path:     "/",
			HttpOnly: false,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   maxAge,
		},
	)
}

func setLastLoginMethodCookie(
	w http.ResponseWriter,
	r *http.Request,
	publicURL, method string,
	now time.Time,
) {
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     lastLoginMethodCookieName,
			Value:    method,
			Path:     "/",
			HttpOnly: false,
			Secure:   requestScheme(publicURL, r) == "https",
			SameSite: http.SameSiteLaxMode,
			Expires:  now.Add(lastLoginMethodCookieTTL),
			MaxAge:   int(lastLoginMethodCookieTTL / time.Second),
		},
	)
}

func clearBrowserSessionCookies(w http.ResponseWriter, r *http.Request, publicURL string) {
	secure := requestScheme(publicURL, r) == "https"
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     BrowserSessionCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		},
	)
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     CSRFCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: false,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		},
	)
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     BrowserSessionHostCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		},
	)
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     CSRFHostCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: false,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		},
	)
}

func BrowserSessionCookie(r *http.Request, publicURL string) (*http.Cookie, error) {
	if cookie, err := r.Cookie(BrowserSessionHostCookieName); err == nil {
		return cookie, nil
	}
	if requestScheme(publicURL, r) == "https" {
		return nil, http.ErrNoCookie
	}
	return r.Cookie(BrowserSessionCookieName)
}

func requestScheme(publicURL string, r *http.Request) string {
	if publicURL != "" {
		if parsed, err := url.Parse(publicURL); err == nil && parsed.Scheme != "" {
			return strings.ToLower(parsed.Scheme)
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func SameOrigin(publicURL string, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		return originMatches(publicURL, origin, r)
	}
	referer := r.Header.Get("Referer")
	if referer != "" {
		return originMatches(publicURL, referer, r)
	}
	return false
}

func originMatches(publicURL, raw string, r *http.Request) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	wantScheme, wantHost := requestOrigin(publicURL, r)
	scheme := strings.ToLower(parsed.Scheme)
	wantScheme = strings.ToLower(wantScheme)
	if scheme != wantScheme {
		return false
	}
	return httporigin.CanonicalHost(scheme, parsed.Host) == httporigin.CanonicalHost(wantScheme, wantHost)
}

func requestOrigin(publicURL string, r *http.Request) (string, string) {
	if publicURL != "" {
		if parsed, err := url.Parse(publicURL); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return parsed.Scheme, parsed.Host
		}
	}
	return requestScheme(publicURL, r), r.Host
}

func RandomURLToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func SafeReturnTo(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return "/"
	}
	return value
}

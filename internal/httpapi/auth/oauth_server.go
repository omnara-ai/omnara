package auth

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	OAuthAuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"
	OAuthDeviceAuthorizationPath         = "/api/auth/device/code"
	OAuthTokenPath                       = "/api/auth/device/token"
	OAuthDeviceGrantType                 = "urn:ietf:params:oauth:grant-type:device_code"
	OmnaraCLIClientID                    = "omnara-cli"

	maxOAuthFormBytes = 16 * 1024
)

type oauthPublicClient struct {
	name string
}

var oauthPublicClients = map[string]oauthPublicClient{
	OmnaraCLIClientID: {name: "Omnara CLI"},
}

type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func (h *Handler) authorizationServerMetadataRoute(w http.ResponseWriter, r *http.Request) {
	issuer := h.issuerURL(r)
	if issuer == "" {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "issuer is not configured")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"device_authorization_endpoint":         issuer + OAuthDeviceAuthorizationPath,
		"token_endpoint":                        issuer + OAuthTokenPath,
		"grant_types_supported":                 []string{OAuthDeviceGrantType},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func (h *Handler) issuerURL(r *http.Request) string {
	if issuer := strings.TrimRight(strings.TrimSpace(h.publicURL), "/"); issuer != "" {
		return issuer
	}
	if r.Host == "" {
		return ""
	}
	scheme := requestScheme(h.publicURL, r)
	return scheme + "://" + r.Host
}

func parseOAuthForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "content type must be application/x-www-form-urlencoded")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthFormBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "request form is invalid")
		return nil, false
	}
	for _, values := range r.PostForm {
		if len(values) > 1 {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "request parameters must not be repeated")
			return nil, false
		}
	}
	return r.PostForm, true
}

func oauthFormValue(values url.Values, key string) string {
	return values.Get(key)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	setOAuthNoStoreHeaders(w)
	writeJSON(w, status, oauthErrorResponse{Error: code, ErrorDescription: description})
}

func writeOAuthJSON(w http.ResponseWriter, status int, body any) {
	setOAuthNoStoreHeaders(w)
	writeJSON(w, status, body)
}

func setOAuthNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (h *Handler) requireOAuthRateLimits(
	w http.ResponseWriter,
	r *http.Request,
	action, subject string,
	pairLimit, subjectLimit, clientLimit int,
	window time.Duration,
	tokenEndpoint bool,
) bool {
	err := h.checkAuthRateLimits(
		r,
		action,
		subject,
		pairLimit,
		subjectLimit,
		clientLimit,
		window,
	)
	if err == nil {
		return true
	}
	if errors.Is(err, errRateLimited) {
		if tokenEndpoint {
			writeOAuthError(w, http.StatusBadRequest, "slow_down", "polling rate limit exceeded")
		} else {
			writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "rate limit exceeded")
		}
		return false
	}
	if h.log != nil {
		h.log.ErrorContext(r.Context(), "OAuth rate limiter failed", "error", err)
	}
	writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "authorization server unavailable")
	return false
}

func (h *Handler) writeOAuthServerError(w http.ResponseWriter, r *http.Request, err error) {
	if h.log != nil {
		h.log.ErrorContext(r.Context(), "OAuth request failed", "error", err)
	}
	writeOAuthError(w, http.StatusInternalServerError, "server_error", "authorization server request failed")
}

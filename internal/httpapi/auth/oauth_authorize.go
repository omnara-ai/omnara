package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	OAuthAuthorizePagePath        = "/oauth/authorize"
	OAuthAuthorizePendingPath     = "/api/auth/oauth/authorize/pending"
	OAuthAuthorizeApprovePath     = "/api/auth/oauth/authorize/approve"
	OAuthAuthorizeDenyPath        = "/api/auth/oauth/authorize/deny"
	OAuthAuthorizationCodeGrant   = "authorization_code"
	OAuthRefreshTokenGrant        = "refresh_token"
	oauthPKCEMethodS256           = "S256"
	oauthClientMetadataMaxBytes   = 64 * 1024
	oauthAuthorizeParamMaxBytes   = 2048
	oauthAuthorizeRequestMaxBytes = 8 * 1024
)

type oauthClientMetadata struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type oauthAuthorizeRequest struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Resource      string
}

type oauthAuthorizeError struct {
	code        string
	description string
	redirectURI string
	state       string
}

func (e *oauthAuthorizeError) Error() string {
	return e.code + ": " + e.description
}

func clientError(code, description string) *oauthAuthorizeError {
	return &oauthAuthorizeError{code: code, description: description}
}

func (r oauthAuthorizeRequest) redirectError(code, description string) *oauthAuthorizeError {
	return &oauthAuthorizeError{code: code, description: description, redirectURI: r.RedirectURI, state: r.State}
}

func (h *Handler) allowedMCPResourceURLs(r *http.Request) []string {
	if len(h.mcpResourceURLs) > 0 {
		return h.mcpResourceURLs
	}
	issuer := h.issuerURL(r)
	if issuer == "" {
		return nil
	}
	return []string{issuer + apimcp.Path}
}

func parseOAuthAuthorizeRequest(values url.Values) (oauthAuthorizeRequest, *oauthAuthorizeError) {
	for key, list := range values {
		if len(list) != 1 {
			return oauthAuthorizeRequest{}, clientError("invalid_request", key+" must not be repeated")
		}
		if len(list[0]) > oauthAuthorizeParamMaxBytes {
			return oauthAuthorizeRequest{}, clientError("invalid_request", key+" is too long")
		}
	}
	request := oauthAuthorizeRequest{
		ClientID:      values.Get("client_id"),
		RedirectURI:   values.Get("redirect_uri"),
		State:         values.Get("state"),
		CodeChallenge: values.Get("code_challenge"),
		Resource:      strings.TrimRight(values.Get("resource"), "/"),
	}
	if err := validateClientIDURL(request.ClientID); err != nil {
		return oauthAuthorizeRequest{}, clientError("invalid_client", err.Error())
	}
	if err := validateRedirectURI(request.RedirectURI); err != nil {
		return oauthAuthorizeRequest{}, clientError("invalid_request", err.Error())
	}
	return request, nil
}

func (r oauthAuthorizeRequest) validateGrantParams(values url.Values, resources []string) *oauthAuthorizeError {
	if values.Get("response_type") != "code" {
		return r.redirectError("unsupported_response_type", "response_type must be code")
	}
	if values.Get("code_challenge_method") != oauthPKCEMethodS256 {
		return r.redirectError("invalid_request", "code_challenge_method must be S256")
	}
	if len(r.CodeChallenge) < 43 || len(r.CodeChallenge) > 128 ||
		strings.IndexFunc(r.CodeChallenge, func(c rune) bool {
			return !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
				c == '-' || c == '_')
		}) >= 0 {
		return r.redirectError("invalid_request", "code_challenge is invalid")
	}
	if values.Get("scope") != "" {
		return r.redirectError("invalid_scope", "this authorization server does not define OAuth scopes")
	}
	if r.Resource == "" {
		return r.redirectError("invalid_target", "resource is required")
	}
	if !slices.Contains(resources, r.Resource) {
		return r.redirectError("invalid_target", "resource must be one of "+strings.Join(resources, ", "))
	}
	return nil
}

func validateClientIDURL(clientID string) error {
	if !utf8.ValidString(clientID) {
		return errors.New("client_id must be valid UTF-8")
	}
	parsed, err := url.Parse(clientID)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.Path == "" || parsed.Path == "/" || parsed.String() != clientID {
		return errors.New("client_id must be an https URL with a path")
	}
	return nil
}

func validateRedirectURI(redirectURI string) error {
	if !utf8.ValidString(redirectURI) {
		return errors.New("redirect_uri must be valid UTF-8")
	}
	parsed, err := url.Parse(redirectURI)
	if err != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil || parsed.String() != redirectURI {
		return errors.New("redirect_uri must be an absolute URL without a fragment")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return errors.New("http redirect_uri must use a loopback host")
	default:
		return errors.New("redirect_uri must use https or a loopback http URL")
	}
}

func redirectURIRegistered(registered []string, presented string) bool {
	if slices.Contains(registered, presented) {
		return true
	}
	parsed, err := url.Parse(presented)
	if err != nil || parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return false
	}
	for _, candidate := range registered {
		other, err := url.Parse(candidate)
		if err != nil || other.Scheme != "http" || !isLoopbackHost(other.Hostname()) {
			continue
		}
		if other.Hostname() == parsed.Hostname() && other.Path == parsed.Path && other.RawQuery == parsed.RawQuery {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *Handler) fetchClientMetadata(ctx context.Context, clientID string) (oauthClientMetadata, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return oauthClientMetadata{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := h.clientMetadataHTTPClient.Do(request)
	if err != nil {
		return oauthClientMetadata{}, fmt.Errorf("fetch client metadata: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return oauthClientMetadata{}, fmt.Errorf("client metadata returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, oauthClientMetadataMaxBytes+1))
	if err != nil {
		return oauthClientMetadata{}, fmt.Errorf("read client metadata: %w", err)
	}
	if len(body) > oauthClientMetadataMaxBytes {
		return oauthClientMetadata{}, errors.New("client metadata document is too large")
	}
	var metadata oauthClientMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return oauthClientMetadata{}, fmt.Errorf("client metadata is not valid JSON: %w", err)
	}
	if metadata.ClientID != clientID {
		return oauthClientMetadata{}, errors.New("client metadata client_id does not match its URL")
	}
	metadata.ClientName, err = resourcename.CanonicalizeRequired("client_name", strings.TrimSpace(metadata.ClientName))
	if err != nil {
		return oauthClientMetadata{}, err
	}
	if len(metadata.RedirectURIs) == 0 {
		return oauthClientMetadata{}, errors.New("client metadata must list redirect_uris")
	}
	if metadata.TokenEndpointAuthMethod != "" && metadata.TokenEndpointAuthMethod != "none" {
		return oauthClientMetadata{}, errors.New("only public clients (token_endpoint_auth_method none) are supported")
	}
	if len(metadata.GrantTypes) > 0 && !slices.Contains(metadata.GrantTypes, OAuthAuthorizationCodeGrant) {
		return oauthClientMetadata{}, errors.New("client metadata must allow the authorization_code grant")
	}
	if len(metadata.ResponseTypes) > 0 && !slices.Contains(metadata.ResponseTypes, "code") {
		return oauthClientMetadata{}, errors.New("client metadata must allow the code response type")
	}
	if metadata.ClientURI != "" {
		parsed, err := url.Parse(metadata.ClientURI)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			metadata.ClientURI = ""
		}
	}
	return metadata, nil
}

func (h *Handler) resolveOAuthAuthorizeRequest(
	r *http.Request,
	values url.Values,
) (oauthAuthorizeRequest, oauthClientMetadata, *oauthAuthorizeError) {
	resources := h.allowedMCPResourceURLs(r)
	if len(resources) == 0 {
		return oauthAuthorizeRequest{}, oauthClientMetadata{}, clientError(
			"temporarily_unavailable", "issuer is not configured",
		)
	}
	request, authErr := parseOAuthAuthorizeRequest(values)
	if authErr != nil {
		return oauthAuthorizeRequest{}, oauthClientMetadata{}, authErr
	}
	metadata, err := h.fetchClientMetadata(r.Context(), request.ClientID)
	if err != nil {
		return oauthAuthorizeRequest{}, oauthClientMetadata{}, clientError("invalid_client", err.Error())
	}
	if !redirectURIRegistered(metadata.RedirectURIs, request.RedirectURI) {
		return oauthAuthorizeRequest{}, oauthClientMetadata{}, clientError(
			"invalid_request", "redirect_uri is not registered for this client",
		)
	}
	if authErr := request.validateGrantParams(values, resources); authErr != nil {
		return oauthAuthorizeRequest{}, oauthClientMetadata{}, authErr
	}
	return request, metadata, nil
}

func (h *Handler) oauthAuthorizePrincipal(
	w http.ResponseWriter,
	r *http.Request,
) (identitystore.PrincipalRecord, bool) {
	principal, ok := h.currentPrincipal(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID ||
		principal.BrowserSessionID == storage.NilID {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return identitystore.PrincipalRecord{}, false
	}
	if !h.requireAuthRateLimits(
		w,
		r,
		"oauth_authorize",
		principal.ID.String(),
		LoginLimit,
		LoginSubjectLimit,
		TokenClientLimit,
		authShortWindow,
	) {
		return identitystore.PrincipalRecord{}, false
	}
	return principal, true
}

func (h *Handler) writeOAuthAuthorizeError(w http.ResponseWriter, r *http.Request, authErr *oauthAuthorizeError) {
	body := map[string]any{"error": authErr.code, "error_description": authErr.description}
	if authErr.redirectURI != "" {
		body["redirect_url"] = h.oauthRedirectURL(r, authErr.redirectURI, url.Values{
			"error":             {authErr.code},
			"error_description": {authErr.description},
		}, authErr.state)
	}
	writeOAuthJSON(w, http.StatusBadRequest, body)
}

func (h *Handler) oauthRedirectURL(r *http.Request, redirectURI string, params url.Values, state string) string {
	target, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	query := target.Query()
	for key, values := range params {
		query.Set(key, values[0])
	}
	if state != "" {
		query.Set("state", state)
	}
	query.Set("iss", h.issuerURL(r))
	target.RawQuery = query.Encode()
	return target.String()
}

func (h *Handler) pendingOAuthAuthorizeRoute(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.oauthAuthorizePrincipal(w, r); !ok {
		return
	}
	request, metadata, authErr := h.resolveOAuthAuthorizeRequest(r, r.URL.Query())
	if authErr != nil {
		h.writeOAuthAuthorizeError(w, r, authErr)
		return
	}
	redirect, err := url.Parse(request.RedirectURI)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInternalError)
		return
	}
	writeOAuthJSON(w, http.StatusOK, map[string]any{
		"client_id":     metadata.ClientID,
		"client_name":   metadata.ClientName,
		"client_uri":    metadata.ClientURI,
		"redirect_uri":  request.RedirectURI,
		"redirect_host": redirect.Host,
		"loopback":      isLoopbackHost(redirect.Hostname()),
		"resource":      request.Resource,
	})
}

func (h *Handler) decodeOAuthAuthorizeDecision(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	if !requireJSONContentType(w, r) {
		return nil, false
	}
	var body struct {
		Query string `json:"query"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"query": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return nil, false
	}
	if len(body.Query) > oauthAuthorizeRequestMaxBytes {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, "query is too long")
		return nil, false
	}
	values, err := url.ParseQuery(strings.TrimPrefix(body.Query, "?"))
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, "query is not a valid query string")
		return nil, false
	}
	return values, true
}

func (h *Handler) approveOAuthAuthorizeRoute(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.oauthAuthorizePrincipal(w, r)
	if !ok {
		return
	}
	values, ok := h.decodeOAuthAuthorizeDecision(w, r)
	if !ok {
		return
	}
	request, metadata, authErr := h.resolveOAuthAuthorizeRequest(r, values)
	if authErr != nil {
		h.writeOAuthAuthorizeError(w, r, authErr)
		return
	}
	code, err := h.store.CreateOAuthAuthorizationCode(r.Context(), identitystore.CreateOAuthAuthorizationCodeInput{
		UserID:           principal.ID,
		BrowserSessionID: principal.BrowserSessionID,
		ClientID:         metadata.ClientID,
		ClientName:       metadata.ClientName,
		RedirectURI:      request.RedirectURI,
		CodeChallenge:    request.CodeChallenge,
		Resource:         request.Resource,
	})
	if errors.Is(err, storeerr.ErrUnauthorized) {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if errors.Is(err, storeerr.ErrInvalidRequest) {
		h.writeOAuthAuthorizeError(w, r, clientError("invalid_client", err.Error()))
		return
	}
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	writeOAuthJSON(w, http.StatusOK, map[string]string{
		"redirect_url": h.oauthRedirectURL(r, request.RedirectURI, url.Values{"code": {code}}, request.State),
	})
}

func (h *Handler) denyOAuthAuthorizeRoute(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.oauthAuthorizePrincipal(w, r); !ok {
		return
	}
	values, ok := h.decodeOAuthAuthorizeDecision(w, r)
	if !ok {
		return
	}
	request, _, authErr := h.resolveOAuthAuthorizeRequest(r, values)
	if authErr != nil {
		h.writeOAuthAuthorizeError(w, r, authErr)
		return
	}
	writeOAuthJSON(w, http.StatusOK, map[string]string{
		"redirect_url": h.oauthRedirectURL(r, request.RedirectURI, url.Values{
			"error":             {"access_denied"},
			"error_description": {"the user denied the authorization request"},
		}, request.State),
	})
}

func (h *Handler) authorizationCodeGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	clientID := form.Get("client_id")
	if err := validateClientIDURL(clientID); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", err.Error())
		return
	}
	if form.Get("code") == "" || form.Get("redirect_uri") == "" || form.Get("code_verifier") == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code, redirect_uri, and code_verifier are required")
		return
	}
	if !h.requireOAuthGrantRateLimits(w, r, "oauth_code_exchange", form.Get("code")) {
		return
	}
	tokens, err := h.store.ExchangeOAuthAuthorizationCode(r.Context(), identitystore.ExchangeOAuthAuthorizationCodeInput{
		Code:         form.Get("code"),
		ClientID:     clientID,
		RedirectURI:  form.Get("redirect_uri"),
		CodeVerifier: form.Get("code_verifier"),
		Resource:     strings.TrimRight(form.Get("resource"), "/"),
	})
	h.writeOAuthTokenSet(w, r, tokens, err)
}

func (h *Handler) refreshTokenGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	clientID := form.Get("client_id")
	if err := validateClientIDURL(clientID); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", err.Error())
		return
	}
	if form.Get("refresh_token") == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	if !h.requireOAuthGrantRateLimits(w, r, "oauth_refresh", form.Get("refresh_token")) {
		return
	}
	tokens, err := h.store.RefreshOAuthAccessToken(r.Context(), identitystore.RefreshOAuthAccessTokenInput{
		RefreshToken: form.Get("refresh_token"),
		ClientID:     clientID,
		Resource:     strings.TrimRight(form.Get("resource"), "/"),
	})
	h.writeOAuthTokenSet(w, r, tokens, err)
}

func (h *Handler) requireOAuthGrantRateLimits(w http.ResponseWriter, r *http.Request, action, grant string) bool {
	return h.requireOAuthRateLimits(
		w, r, action, grant, TokenConsumeLimit, TokenConsumeLimit, TokenClientLimit, authShortWindow, false,
	)
}

func (h *Handler) writeOAuthTokenSet(
	w http.ResponseWriter,
	r *http.Request,
	tokens identitystore.OAuthTokenSetRecord,
	err error,
) {
	if errors.Is(err, storeerr.ErrUnauthorized) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "the authorization grant is invalid or expired")
		return
	}
	if err != nil {
		h.writeOAuthServerError(w, r, err)
		return
	}
	writeOAuthJSON(w, http.StatusOK, map[string]any{
		"access_token":  tokens.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    int(tokens.ExpiresIn.Seconds()),
		"refresh_token": tokens.RefreshToken,
	})
}

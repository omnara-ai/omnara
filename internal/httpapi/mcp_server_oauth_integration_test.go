//go:build integration

package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

const mcpOAuthTestPublicURL = "https://omnara.test"

type mcpOAuthTestClient struct {
	server      *httptest.Server
	clientID    string
	redirectURI string
}

func newMCPOAuthTestClient(t *testing.T, redirectURI string) mcpOAuthTestClient {
	t.Helper()
	client := mcpOAuthTestClient{redirectURI: redirectURI}
	client.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/client.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":                  client.clientID,
			"client_name":                "Example MCP Client",
			"client_uri":                 "https://client.example",
			"redirect_uris":              []string{redirectURI},
			"grant_types":                []string{"authorization_code", "refresh_token"},
			"response_types":             []string{"code"},
			"token_endpoint_auth_method": "none",
		})
	}))
	t.Cleanup(client.server.Close)
	client.clientID = client.server.URL + "/oauth/client.json"
	return client
}

func pkcePair(t *testing.T) (string, string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate verifier: %v", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	return verifier, identitystore.PKCES256Challenge(verifier)
}

func mcpOAuthBrowserRequest(method, target, body, session, csrf string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", mcpOAuthTestPublicURL)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: session})
	if csrf != "" {
		req.Header.Set(httpauth.CSRFHeaderName, csrf)
	}
	return req
}

func mcpToolCallRequest(token, tool string) *http.Request {
	req := httptest.NewRequest(
		http.MethodPost,
		mcpOAuthTestPublicURL+apimcp.Path,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":{}}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func decodeJSONBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

func jsonString(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	value, ok := body[key].(string)
	if !ok {
		t.Fatalf("%s = %v, want string", key, body[key])
	}
	return value
}

func jsonFirstElement(t *testing.T, body map[string]any, key string) any {
	t.Helper()
	list, ok := body[key].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("%s = %v, want non-empty list", key, body[key])
	}
	return list[0]
}

func redirectURLFromBody(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	parsed, err := url.Parse(jsonString(t, decodeJSONBody(t, rec), "redirect_url"))
	if err != nil {
		t.Fatalf("parse redirect_url from %s: %v", rec.Body.String(), err)
	}
	return parsed
}

func TestMCPServerOAuthAuthorizationCodeFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	client := newMCPOAuthTestClient(t, "http://127.0.0.1:3000/callback")
	handler := newIntegrationServer(
		pool,
		WithPublicURL(mcpOAuthTestPublicURL),
		WithOAuthClientMetadataHTTPClient(client.server.Client()),
		WithAuthRateLimiter(allowAllAuthLimiter{}),
	)
	store := integrationStoreForHandler(t, handler)
	user, err := storagetest.CreateVerifiedUser(
		ctx, pool, storagetest.CreateVerifiedUserInput{Email: "mcp-oauth@example.com", DisplayName: "MCP OAuth"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	session, csrf, err := sCreateBrowserSessionForTest(ctx, store, user.ID)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	resource := mcpOAuthTestPublicURL + apimcp.Path

	rec := performRequest(handler, mcpToolCallRequest("", "whoami"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mcp status=%d body=%s", rec.Code, rec.Body.String())
	}
	wantChallenge := `Bearer resource_metadata="` + mcpOAuthTestPublicURL + `/.well-known/oauth-protected-resource/api/mcp"`
	if got := rec.Header().Get("WWW-Authenticate"); got != wantChallenge {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, wantChallenge)
	}

	rec = performRequest(handler, httptest.NewRequest(
		http.MethodGet, mcpOAuthTestPublicURL+"/.well-known/oauth-protected-resource/api/mcp", nil,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("protected resource metadata status=%d body=%s", rec.Code, rec.Body.String())
	}
	prm := decodeJSONBody(t, rec)
	if prm["resource"] != resource || jsonFirstElement(t, prm, "authorization_servers") != mcpOAuthTestPublicURL {
		t.Fatalf("protected resource metadata = %v", prm)
	}

	rec = performRequest(handler, httptest.NewRequest(
		http.MethodGet, mcpOAuthTestPublicURL+"/.well-known/oauth-authorization-server", nil,
	))
	asm := decodeJSONBody(t, rec)
	if asm["authorization_endpoint"] != mcpOAuthTestPublicURL+"/oauth/authorize" ||
		asm["client_id_metadata_document_supported"] != true ||
		jsonFirstElement(t, asm, "code_challenge_methods_supported") != "S256" {
		t.Fatalf("authorization server metadata = %v", asm)
	}

	verifier, challenge := pkcePair(t)
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.clientID},
		"redirect_uri":          {client.redirectURI},
		"state":                 {"state-123"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}.Encode()

	rec = performRequest(handler, httptest.NewRequest(
		http.MethodGet, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizePendingPath+"?"+query, nil,
	))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pending without session status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodGet, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizePendingPath+"?"+query, "", session, "",
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("pending status=%d body=%s", rec.Code, rec.Body.String())
	}
	pending := decodeJSONBody(t, rec)
	if pending["client_name"] != "Example MCP Client" || pending["redirect_host"] != "127.0.0.1:3000" ||
		pending["loopback"] != true || pending["resource"] != resource {
		t.Fatalf("pending = %v", pending)
	}

	badRedirect, _ := url.ParseQuery(query)
	badRedirect.Set("redirect_uri", "http://127.0.0.1:3000/other")
	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodGet, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizePendingPath+"?"+badRedirect.Encode(), "", session, "",
	))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_request"`) ||
		strings.Contains(rec.Body.String(), "redirect_url") {
		t.Fatalf("unregistered redirect status=%d body=%s", rec.Code, rec.Body.String())
	}

	badResource, _ := url.ParseQuery(query)
	badResource.Set("resource", "https://other.example/mcp")
	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodGet, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizePendingPath+"?"+badResource.Encode(), "", session, "",
	))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_target"`) {
		t.Fatalf("foreign resource status=%d body=%s", rec.Code, rec.Body.String())
	}
	errorRedirect := redirectURLFromBody(t, rec)
	if errorRedirect.Query().Get("error") != "invalid_target" ||
		errorRedirect.Query().Get("state") != "state-123" || errorRedirect.Query().Get("iss") != mcpOAuthTestPublicURL {
		t.Fatalf("error redirect = %v", errorRedirect)
	}

	unregisteredRedirect, _ := url.ParseQuery(query)
	unregisteredRedirect.Set("redirect_uri", "https://attacker.example/callback")
	unregisteredRedirect.Set("response_type", "token")
	unregisteredPending := mcpOAuthTestPublicURL + httpauth.OAuthAuthorizePendingPath + "?" + unregisteredRedirect.Encode()
	rec = performRequest(handler, mcpOAuthBrowserRequest(http.MethodGet, unregisteredPending, "", session, ""))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_request"`) ||
		strings.Contains(rec.Body.String(), "redirect_url") {
		t.Fatalf("unregistered redirect status=%d body=%s", rec.Code, rec.Body.String())
	}

	decision := `{"query":` + quotedJSONString("?"+query) + `}`
	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodPost, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizeDenyPath, decision, session, csrf,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("deny status=%d body=%s", rec.Code, rec.Body.String())
	}
	denied := redirectURLFromBody(t, rec)
	if denied.Query().Get("error") != "access_denied" || denied.Query().Get("state") != "state-123" {
		t.Fatalf("deny redirect = %v", denied)
	}

	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodPost, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizeApprovePath, decision, session, "",
	))
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("approve without csrf status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodPost, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizeApprovePath, decision, session, csrf,
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	approved := redirectURLFromBody(t, rec)
	code := approved.Query().Get("code")
	if approved.Scheme != "http" || approved.Host != "127.0.0.1:3000" || approved.Path != "/callback" || code == "" ||
		approved.Query().Get("state") != "state-123" || approved.Query().Get("iss") != mcpOAuthTestPublicURL {
		t.Fatalf("approve redirect = %v", approved)
	}

	tokenForm := func(mutate func(url.Values)) url.Values {
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"client_id":     {client.clientID},
			"redirect_uri":  {client.redirectURI},
			"code_verifier": {verifier},
			"resource":      {resource},
		}
		if mutate != nil {
			mutate(form)
		}
		return form
	}
	tokenEndpoint := mcpOAuthTestPublicURL + httpauth.OAuthTokenPath
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, tokenForm(func(v url.Values) {
		v.Set("client_id", "https://other.example/client.json")
	})))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("wrong client exchange status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, tokenForm(nil)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("consumed code reused after rejected exchange status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodPost, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizeApprovePath, decision, session, csrf,
	))
	approved = redirectURLFromBody(t, rec)
	code = approved.Query().Get("code")
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, tokenForm(func(v url.Values) {
		v.Set("code_verifier", strings.Repeat("b", 43))
	})))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("wrong verifier status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodPost, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizeApprovePath, decision, session, csrf,
	))
	approved = redirectURLFromBody(t, rec)
	code = approved.Query().Get("code")
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, tokenForm(nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token response Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	tokens := decodeJSONBody(t, rec)
	accessToken := jsonString(t, tokens, "access_token")
	refreshToken := jsonString(t, tokens, "refresh_token")
	if !strings.HasPrefix(accessToken, "omnara_oauth_v1_") || refreshToken == "" || tokens["token_type"] != "Bearer" ||
		tokens["expires_in"] != float64(identitystore.OAuthAccessTokenTTL/time.Second) {
		t.Fatalf("token response = %v", tokens)
	}
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, tokenForm(nil)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("code replay status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = performRequest(handler, mcpToolCallRequest(accessToken, "whoami"))
	if rec.Code != http.StatusOK {
		t.Fatalf("mcp call with oauth token status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), user.ID.String()) && !strings.Contains(rec.Body.String(), `"user"`) {
		t.Fatalf("whoami body = %s", rec.Body.String())
	}

	apiReq := httptest.NewRequest(http.MethodGet, mcpOAuthTestPublicURL+"/api/v1/me", nil)
	apiReq.Header.Set("Authorization", "Bearer "+accessToken)
	rec = performRequest(handler, apiReq)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("oauth token outside mcp resource status=%d body=%s", rec.Code, rec.Body.String())
	}

	refreshForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {client.clientID},
	}
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	refreshed := decodeJSONBody(t, rec)
	newAccess := jsonString(t, refreshed, "access_token")
	newRefresh := jsonString(t, refreshed, "refresh_token")
	if newAccess == accessToken || newRefresh == refreshToken {
		t.Fatalf("refresh response = %v", refreshed)
	}
	rec = performRequest(handler, mcpToolCallRequest(accessToken, "whoami"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("rotated access token status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performRequest(handler, mcpToolCallRequest(newAccess, "whoami"))
	if rec.Code != http.StatusOK {
		t.Fatalf("refreshed access token status=%d body=%s", rec.Code, rec.Body.String())
	}

	if err := store.Identity().RevokeBrowserSession(ctx, session); err != nil {
		t.Fatalf("revoke browser session: %v", err)
	}
	rec = performRequest(handler, mcpToolCallRequest(newAccess, "whoami"))
	if rec.Code != http.StatusOK {
		t.Fatalf("oauth token after session logout status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotated refresh token replay within grace status=%d body=%s", rec.Code, rec.Body.String())
	}
	graced := decodeJSONBody(t, rec)
	graceAccess := jsonString(t, graced, "access_token")
	graceRefresh := jsonString(t, graced, "refresh_token")
	if graceAccess == newAccess || graceRefresh == newRefresh || graceRefresh == refreshToken {
		t.Fatalf("grace refresh response = %v", graced)
	}
	rec = performRequest(handler, mcpToolCallRequest(newAccess, "whoami"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("access token superseded within grace status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performRequest(handler, mcpToolCallRequest(graceAccess, "whoami"))
	if rec.Code != http.StatusOK {
		t.Fatalf("grace access token status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE oauth_retired_refresh_tokens retired
		 SET retired_at = retired.retired_at - ($2::bigint * interval '1 second')
		 FROM oauth_access_tokens token
		 WHERE retired.oauth_access_token_id = token.id
		   AND token.user_id = $1`,
		user.ID,
		int64(identitystore.OAuthRefreshTokenReuseGrace/time.Second)+1,
	); err != nil {
		t.Fatalf("age refresh token rotation: %v", err)
	}
	refreshForm.Set("refresh_token", newRefresh)
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("rotated refresh token replay after grace status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performRequest(handler, mcpToolCallRequest(graceAccess, "whoami"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("access token after refresh reuse status=%d body=%s", rec.Code, rec.Body.String())
	}
	refreshForm.Set("refresh_token", graceRefresh)
	rec = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("refresh token after refresh reuse status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPServerOAuthTokenRateLimitsScopeToPresentedGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	redisClient := integrationredis.OpenClient(t)
	handler := newIntegrationServer(pool, WithPublicURL(mcpOAuthTestPublicURL), WithRedisBackedAuth(redisClient))
	store := integrationStoreForHandler(t, handler)
	runKey := identitystore.HashBearerToken(t.Name() + time.Now().UTC().Format(time.RFC3339Nano))[:12]
	clientID := "https://client.example/" + runKey + "/client.json"
	resource := mcpOAuthTestPublicURL + apimcp.Path
	user, err := storagetest.CreateVerifiedUser(
		ctx, pool, storagetest.CreateVerifiedUserInput{Email: "mcp-oauth-rate@example.com", DisplayName: "MCP OAuth Rate"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	session, err := store.Identity().CreateBrowserSession(ctx, identitystore.CreateBrowserSessionInput{
		UserID:    user.ID,
		Token:     "oauth-rate-session-" + runKey,
		CSRFToken: "oauth-rate-csrf-" + runKey,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	verifier, challenge := pkcePair(t)
	code, err := store.Identity().CreateOAuthAuthorizationCode(ctx, identitystore.CreateOAuthAuthorizationCodeInput{
		UserID:           user.ID,
		BrowserSessionID: session.ID,
		ClientID:         clientID,
		ClientName:       "Rate Limited Client",
		RedirectURI:      "http://127.0.0.1:3000/callback",
		CodeChallenge:    challenge,
		Resource:         resource,
	})
	if err != nil {
		t.Fatalf("create authorization code: %v", err)
	}
	tokens, err := store.Identity().ExchangeOAuthAuthorizationCode(ctx, identitystore.ExchangeOAuthAuthorizationCodeInput{
		Code:         code,
		ClientID:     clientID,
		RedirectURI:  "http://127.0.0.1:3000/callback",
		CodeVerifier: verifier,
		Resource:     resource,
	})
	if err != nil {
		t.Fatalf("exchange authorization code: %v", err)
	}
	tokenEndpoint := mcpOAuthTestPublicURL + httpauth.OAuthTokenPath
	clientBucket := "oauth-rate-client-" + runKey
	refreshRequest := func(refreshToken string) *http.Request {
		req := newOAuthFormRequest(http.MethodPost, tokenEndpoint, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {refreshToken},
			"client_id":     {clientID},
		})
		req.RemoteAddr = clientBucket
		return req
	}

	for i := range httpauth.TokenConsumeLimit {
		rec := performRequest(handler, refreshRequest("bogus-refresh-"+runKey))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_grant"`) {
			t.Fatalf("bogus refresh %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	rec := performRequest(handler, refreshRequest("bogus-refresh-"+runKey))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("exhausted bogus refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = performRequest(handler, refreshRequest(tokens.RefreshToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid refresh sharing client_id and address status=%d body=%s", rec.Code, rec.Body.String())
	}
}

//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestOIDCAuthorizationAndUserInfo(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	client := newMCPOAuthTestClient(t, "https://client.example/callback")
	options := []Option{
		WithPublicURL(mcpOAuthTestPublicURL),
		WithOAuthClientMetadataHTTPClient(client.server.Client()),
		WithAuthRateLimiter(allowAllAuthLimiter{}),
	}
	handler := newIntegrationServer(pool, options...)
	store := integrationStoreForHandler(t, handler)
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "oidc@example.com", DisplayName: "OIDC User",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, csrf, err := sCreateBrowserSessionForTest(ctx, store, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := pkcePair(t)
	query := url.Values{
		"response_type": {"code"}, "client_id": {client.clientID}, "redirect_uri": {client.redirectURI},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"resource": {mcpOAuthTestPublicURL + "/mcp"}, "scope": {"openid email"}, "nonce": {"oidc-nonce"},
	}
	pending := performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodGet, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizePendingPath+"?"+query.Encode(), "", session, csrf,
	))
	if pending.Code != 200 || decodeJSONBody(t, pending)["scope"] != "openid email" {
		t.Fatalf("pending scope: %d %s", pending.Code, pending.Body.String())
	}
	decision, err := json.Marshal(map[string]string{"query": query.Encode()})
	if err != nil {
		t.Fatal(err)
	}
	approved := performRequest(handler, mcpOAuthBrowserRequest(
		http.MethodPost, mcpOAuthTestPublicURL+httpauth.OAuthAuthorizeApprovePath, string(decision), session, csrf,
	))
	if approved.Code != 200 {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	callback := redirectURLFromBody(t, approved)
	tokenEndpoint := mcpOAuthTestPublicURL + httpauth.OAuthTokenPath
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {client.clientID}, "redirect_uri": {client.redirectURI},
		"code_verifier": {verifier}, "code": {callback.Query().Get("code")},
	}
	response := performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, form))
	if response.Code != 200 {
		t.Fatalf("exchange: %d %s", response.Code, response.Body.String())
	}
	tokens := decodeJSONBody(t, response)
	if tokens["scope"] != "openid email" {
		t.Fatalf("scope: %v", tokens["scope"])
	}
	access := jsonString(t, tokens, "access_token")
	refresh := jsonString(t, tokens, "refresh_token")
	jwks := performRequest(handler, httptest.NewRequest(http.MethodGet, mcpOAuthTestPublicURL+httpauth.OIDCJWKSPath, nil))
	if jwks.Code != 200 {
		t.Fatalf("JWKS: %s", jwks.Body.String())
	}
	var keys jose.JSONWebKeySet
	if err := json.Unmarshal(jwks.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.ParseSigned(jsonString(t, tokens, "id_token"), []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := parsed.Claims(keys.Keys[0].Key, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["sub"] != user.ID.String() || claims["nonce"] != "oidc-nonce" ||
		claims["aud"] != client.clientID || claims["email_verified"] != true {
		t.Fatalf("ID claims: %v", claims)
	}
	restarted := newIntegrationServer(pool, options...)
	otherJWKS := performRequest(restarted,
		httptest.NewRequest(http.MethodGet, mcpOAuthTestPublicURL+httpauth.OIDCJWKSPath, nil),
	)
	if otherJWKS.Body.String() != jwks.Body.String() {
		t.Fatal("signing key changed across server instances")
	}
	userInfo := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, mcpOAuthTestPublicURL+httpauth.OIDCUserInfoPath, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		return performRequest(handler, request)
	}
	info := userInfo(access)
	if info.Code != 200 {
		t.Fatalf("userinfo: %d %s", info.Code, info.Body.String())
	}
	body := decodeJSONBody(t, info)
	if body["sub"] != user.ID.String() || body["email"] != "oidc@example.com" || body["email_verified"] != true {
		t.Fatalf("userinfo: %v", body)
	}
	postUserInfo := httptest.NewRequest(http.MethodPost, mcpOAuthTestPublicURL+httpauth.OIDCUserInfoPath, nil)
	postUserInfo.Header.Set("Authorization", "Bearer "+access)
	postResponse := performRequest(handler, postUserInfo)
	if postResponse.Code != http.StatusOK || decodeJSONBody(t, postResponse)["sub"] != user.ID.String() {
		t.Fatalf("POST userinfo: %d %s", postResponse.Code, postResponse.Body.String())
	}
	if userInfo(refresh).Code != http.StatusUnauthorized {
		t.Fatal("refresh token accepted as UserInfo bearer token")
	}
	refreshForm := url.Values{"grant_type": {"refresh_token"}, "client_id": {client.clientID}, "refresh_token": {refresh}}
	response = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if response.Code != 200 {
		t.Fatalf("refresh: %d %s", response.Code, response.Body.String())
	}
	tokens = decodeJSONBody(t, response)
	if tokens["scope"] != "openid email" || userInfo(jsonString(t, tokens, "access_token")).Code != 200 {
		t.Fatal("refresh lost identity scopes")
	}
	if userInfo(access).Code != 401 {
		t.Fatal("rotated access token still accepted")
	}
	refreshForm.Set("refresh_token", jsonString(t, tokens, "refresh_token"))
	refreshForm.Set("scope", "openid")
	response = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if response.Code != 200 {
		t.Fatalf("narrow refresh: %d %s", response.Code, response.Body.String())
	}
	tokens = decodeJSONBody(t, response)
	info = userInfo(jsonString(t, tokens, "access_token"))
	if info.Code != http.StatusOK || decodeJSONBody(t, info)["sub"] != user.ID.String() {
		t.Fatalf("narrowed userinfo: %d %s", info.Code, info.Body.String())
	}
	if _, ok := decodeJSONBody(t, info)["email"]; ok {
		t.Fatal("narrowed token disclosed email")
	}
	refreshForm.Set("refresh_token", jsonString(t, tokens, "refresh_token"))
	refreshForm.Set("scope", "openid email")
	response = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if response.Code != 200 {
		t.Fatalf("rewiden refresh: %d %s", response.Code, response.Body.String())
	}
	tokens = decodeJSONBody(t, response)
	info = userInfo(jsonString(t, tokens, "access_token"))
	if tokens["scope"] != "openid email" || decodeJSONBody(t, info)["email"] != "oidc@example.com" {
		t.Fatalf("rewidened userinfo: %v %s", tokens["scope"], info.Body.String())
	}
	refreshForm.Set("refresh_token", jsonString(t, tokens, "refresh_token"))
	refreshForm.Set("scope", "openid")
	response = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if response.Code != 200 {
		t.Fatalf("second narrow refresh: %d %s", response.Code, response.Body.String())
	}
	refreshForm.Set("refresh_token", jsonString(t, decodeJSONBody(t, response), "refresh_token"))
	refreshForm.Del("scope")
	response = performRequest(handler, newOAuthFormRequest(http.MethodPost, tokenEndpoint, refreshForm))
	if response.Code != 200 || decodeJSONBody(t, response)["scope"] != "openid email" {
		t.Fatalf("refresh without scope did not restore the granted scope: %d %s", response.Code, response.Body.String())
	}
}

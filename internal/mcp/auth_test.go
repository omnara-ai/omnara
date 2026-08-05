package mcp_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/secrets"
	"golang.org/x/oauth2"
)

func oauthTestKeyWrapper(t *testing.T) secrets.KeyWrapper {
	t.Helper()
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new local key wrapper: %v", err)
	}
	return keyWrapper
}

func TestDetectAuthDiscoversChallengeMetadataAndAuthServer(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{ChallengeScope: "files:read files:write", PKCE: true})

	req, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if !req.Required {
		t.Fatal("Required = false, want true")
	}
	if req.Resource != ts.URL+"/mcp" {
		t.Fatalf("Resource = %q, want %q", req.Resource, ts.URL+"/mcp")
	}
	if got, want := strings.Join(req.Scopes, " "), "files:read files:write"; got != want {
		t.Fatalf("Scopes = %q, want %q", got, want)
	}
	if req.ProtectedResourceMetadata == nil || len(req.ProtectedResourceMetadata.AuthorizationServers) != 1 {
		t.Fatalf("missing protected resource metadata: %+v", req.ProtectedResourceMetadata)
	}
	if req.AuthorizationServer == nil || req.AuthorizationServer.TokenEndpoint != ts.URL+"/token" {
		t.Fatalf("unexpected authorization server metadata: %+v", req.AuthorizationServer)
	}
}

func TestDetectAuthDoesNotFetchInvalidChallengeMetadataURL(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{
		PKCE:                         true,
		ResourceMetadataChallengeURL: "https://user:secret@metadata.example.com/resource",
	})
	var fetchedInvalidURL atomic.Bool
	client := *ts.Client()
	baseTransport := client.Transport
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "metadata.example.com" {
			fetchedInvalidURL.Store(true)
			return nil, errors.New("invalid metadata URL was fetched")
		}
		return baseTransport.RoundTrip(request)
	})

	if _, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: &client}); err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if fetchedInvalidURL.Load() {
		t.Fatal("DetectAuth fetched a challenge metadata URL containing credentials")
	}
}

func TestDetectAuthAllowsInformationalMetadataURLsOutsideEndpointPolicy(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true, InsecureInformationalMetadata: true})

	requirement, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if got := requirement.AuthorizationServer.ServiceDocumentation; got != "http://docs.example.com/oauth#overview" {
		t.Fatalf("ServiceDocumentation = %q", got)
	}
}

func TestDetectAuthDoesNotFollowDiscoveryRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Store(true)
	}))
	t.Cleanup(target.Close)

	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+source.URL+`/resource-metadata"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(source.Close)

	_, err := mcp.DetectAuth(context.Background(), source.URL+"/mcp", mcp.AuthOptions{HTTPClient: source.Client()})
	if !errors.Is(err, outboundhttp.ErrRedirect) {
		t.Fatalf("DetectAuth error = %v, want redirect rejection", err)
	}
	if followed.Load() {
		t.Fatal("OAuth discovery followed a redirect")
	}
}

func TestDetectAuthNoChallengeMeansAuthNotRequired(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":-1,"result":{}}`))
	}))
	t.Cleanup(ts.Close)

	req, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if req.Required {
		t.Fatal("Required = true, want false")
	}
}

func TestStartOAuthBuildsAuthorizationURLWithSealedStateAndPKCE(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{ChallengeScope: "files:read", PKCE: true})
	req, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}

	keyWrapper := oauthTestKeyWrapper(t)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	authURL, err := mcp.StartOAuth(context.Background(), mcp.OAuthStartInput{
		ClientID:     "client-123",
		ClientSecret: "secret-456",
		RedirectURI:  "http://127.0.0.1/callback",
		Scopes:       req.Scopes,
		AuthMetadata: &req,
		FlowData:     json.RawMessage(`{"secret_name":"linear"}`),
		ExpiresAt:    expiresAt,
		KeyWrapper:   keyWrapper,
	})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	values := parsed.Query()
	assertQuery(t, values, "response_type", "code")
	assertQuery(t, values, "client_id", "client-123")
	assertQuery(t, values, "redirect_uri", "http://127.0.0.1/callback")
	assertQuery(t, values, "scope", "files:read")
	assertQuery(t, values, "resource", ts.URL+"/mcp")
	assertQuery(t, values, "code_challenge_method", "S256")

	flow, err := mcp.OpenOAuthState(context.Background(), keyWrapper, values.Get("state"))
	if err != nil {
		t.Fatalf("OpenOAuthState: %v", err)
	}
	if string(flow.FlowData) != `{"secret_name":"linear"}` {
		t.Fatalf("FlowData = %s, want caller data round-tripped", flow.FlowData)
	}
	if flow.EndpointURL != ts.URL+"/mcp" || flow.Resource != ts.URL+"/mcp" {
		t.Fatalf("flow endpoint/resource = %q/%q, want MCP endpoint", flow.EndpointURL, flow.Resource)
	}
	if flow.TokenEndpoint != ts.URL+"/token" || flow.ClientID != "client-123" || flow.ClientSecret != "secret-456" {
		t.Fatalf("flow client fields = %+v, want token endpoint and client credentials", flow)
	}
	if got, want := strings.Join(flow.Scopes, " "), "files:read"; got != want {
		t.Fatalf("flow.Scopes = %q, want %q", got, want)
	}
	if !flow.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("flow.ExpiresAt = %v, want %v", flow.ExpiresAt, expiresAt)
	}
	hashed := sha256.Sum256([]byte(flow.CodeVerifier))
	if base64.RawURLEncoding.EncodeToString(hashed[:]) != values.Get("code_challenge") {
		t.Fatal("code_challenge does not match the sealed code verifier")
	}
}

func TestOpenOAuthStateRejectsExpiredAndTamperedState(t *testing.T) {
	keyWrapper := oauthTestKeyWrapper(t)
	authURL, err := mcp.StartOAuth(context.Background(), mcp.OAuthStartInput{
		ClientID:    "client-123",
		RedirectURI: "http://127.0.0.1/callback",
		AuthMetadata: &mcp.AuthRequirement{
			EndpointURL: "https://mcp.example.com/mcp",
			AuthorizationServer: &oauthex.AuthServerMeta{
				AuthorizationEndpoint: "https://as.example.com/authorize",
				TokenEndpoint:         "https://as.example.com/token",
			},
		},
		ExpiresAt:  time.Now().UTC().Add(-time.Minute),
		KeyWrapper: keyWrapper,
	})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	state := parsed.Query().Get("state")
	if _, err := mcp.OpenOAuthState(context.Background(), keyWrapper, state); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("OpenOAuthState error = %v, want expired error", err)
	}
	tampered := state[:len(state)-4] + "AAAA"
	if _, err := mcp.OpenOAuthState(context.Background(), keyWrapper, tampered); err == nil ||
		strings.Contains(err.Error(), "expired") {
		t.Fatalf("OpenOAuthState error = %v, want authentication error for tampered state", err)
	}
}

func TestExchangeOAuthCodeSendsPKCEAndResource(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true})

	token, err := mcp.ExchangeOAuthCode(context.Background(), mcp.OAuthCodeExchangeInput{
		TokenEndpoint: ts.URL + "/token",
		ClientID:      "client-123",
		RedirectURI:   "http://127.0.0.1/callback",
		Code:          "code-123",
		CodeVerifier:  "verifier-123",
		Resource:      ts.URL + "/mcp",
		HTTPClient:    ts.Client(),
		AuthStyle:     oauth2.AuthStyleInParams,
	})
	if err != nil {
		t.Fatalf("ExchangeOAuthCode: %v", err)
	}
	if token.AccessToken != "access-code-123" || token.RefreshToken != "refresh-code-123" ||
		token.IDToken != "id-code-123" {
		t.Fatalf("unexpected token set: %+v", token)
	}
	if got, want := strings.Join(token.Scopes, " "), "files:read"; got != want {
		t.Fatalf("Scopes = %q, want %q", got, want)
	}

	form := ts.lastTokenForm(t)
	assertForm(t, form, "grant_type", "authorization_code")
	assertForm(t, form, "client_id", "client-123")
	assertForm(t, form, "code", "code-123")
	assertForm(t, form, "code_verifier", "verifier-123")
	assertForm(t, form, "redirect_uri", "http://127.0.0.1/callback")
	assertForm(t, form, "resource", ts.URL+"/mcp")
}

func TestExchangeOAuthCodeAcceptsStringExpiresIn(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true, StringExpiresIn: true})

	token, err := mcp.ExchangeOAuthCode(context.Background(), mcp.OAuthCodeExchangeInput{
		TokenEndpoint: ts.URL + "/token",
		ClientID:      "client-123",
		RedirectURI:   "http://127.0.0.1/callback",
		Code:          "code-123",
		CodeVerifier:  "verifier-123",
		Resource:      ts.URL + "/mcp",
		HTTPClient:    ts.Client(),
		AuthStyle:     oauth2.AuthStyleInParams,
	})
	if err != nil {
		t.Fatalf("ExchangeOAuthCode: %v", err)
	}
	if token.ExpiresIn <= 0 {
		t.Fatal("ExpiresIn is not positive, want duration derived from string expires_in")
	}
}

func TestRefreshOAuthTokenSendsResourceAndPreservesRefreshToken(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true, OmitRefreshOnRefresh: true})

	token, err := mcp.RefreshOAuthToken(context.Background(), mcp.OAuthRefreshInput{
		TokenEndpoint: ts.URL + "/token",
		ClientID:      "client-123",
		RefreshToken:  "refresh-old",
		Resource:      ts.URL + "/mcp",
		HTTPClient:    ts.Client(),
		AuthStyle:     oauth2.AuthStyleInParams,
	})
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if token.AccessToken != "access-refresh" {
		t.Fatalf("AccessToken = %q, want access-refresh", token.AccessToken)
	}
	if token.RefreshToken != "refresh-old" {
		t.Fatalf("RefreshToken = %q, want refresh-old", token.RefreshToken)
	}

	form := ts.lastTokenForm(t)
	assertForm(t, form, "grant_type", "refresh_token")
	assertForm(t, form, "client_id", "client-123")
	assertForm(t, form, "refresh_token", "refresh-old")
	assertForm(t, form, "resource", ts.URL+"/mcp")
	if form.Has("scope") {
		t.Fatalf("scope = %q, want scope omitted so the server reuses the original grant", form.Get("scope"))
	}
}

func TestOAuthTokenExchangeRejectsPublicHTTP(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: unexpectedRoundTripper{t: t}}
	_, err := mcp.ExchangeOAuthCode(context.Background(), mcp.OAuthCodeExchangeInput{
		TokenEndpoint: "http://auth.example.com/token",
		ClientID:      "client-123",
		RedirectURI:   "https://app.example.com/callback",
		Code:          "code-123",
		CodeVerifier:  "verifier-123",
		Resource:      "https://mcp.example.com",
		HTTPClient:    client,
	})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("ExchangeOAuthCode error = %v, want secure token endpoint error", err)
	}
	_, err = mcp.RefreshOAuthToken(context.Background(), mcp.OAuthRefreshInput{
		TokenEndpoint: "http://auth.example.com/token",
		ClientID:      "client-123",
		RefreshToken:  "refresh-old",
		Resource:      "https://mcp.example.com",
		HTTPClient:    client,
	})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("RefreshOAuthToken error = %v, want secure token endpoint error", err)
	}
}

func TestOAuthTokenExchangeDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	var redirected atomic.Bool
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	defer redirectTarget.Close()
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer tokenServer.Close()

	_, err := mcp.ExchangeOAuthCode(context.Background(), mcp.OAuthCodeExchangeInput{
		TokenEndpoint: tokenServer.URL,
		ClientID:      "client-123",
		RedirectURI:   "https://app.example.com/callback",
		Code:          "code-123",
		CodeVerifier:  "verifier-123",
		Resource:      "https://mcp.example.com",
		HTTPClient:    tokenServer.Client(),
	})
	if !errors.Is(err, outboundhttp.ErrRedirect) {
		t.Fatalf("ExchangeOAuthCode error = %v, want redirect rejection", err)
	}
	if redirected.Load() {
		t.Fatal("OAuth token exchange followed a redirect")
	}
}

func TestOAuthTokenExchangeRejectsOversizedResponse(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024+1)))
			}))
			t.Cleanup(tokenServer.Close)

			_, err := mcp.ExchangeOAuthCode(context.Background(), mcp.OAuthCodeExchangeInput{
				TokenEndpoint: tokenServer.URL,
				ClientID:      "client-123",
				RedirectURI:   "http://127.0.0.1/callback",
				Code:          "code-123",
				CodeVerifier:  "verifier-123",
				Resource:      "https://mcp.example.com",
				HTTPClient:    tokenServer.Client(),
				AuthStyle:     oauth2.AuthStyleInParams,
			})
			if !errors.Is(err, mcp.ErrResponseTooLarge) {
				t.Fatalf("ExchangeOAuthCode error = %v, want ErrResponseTooLarge", err)
			}
			if status == http.StatusUnauthorized && !strings.Contains(err.Error(), "HTTP 401") {
				t.Fatalf("ExchangeOAuthCode error = %v, want HTTP status", err)
			}
		})
	}
}

func TestOAuthTokenEndpointErrorsDoNotExposeResponseBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{
			name:     "OAuth error",
			body:     `{"error":"invalid_grant","error_description":"do-not-log","detail":"do-not-log"}`,
			wantCode: "invalid_grant",
		},
		{name: "malformed response", body: "upstream failure: do-not-log"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(tokenServer.Close)

			_, err := mcp.RefreshOAuthToken(context.Background(), mcp.OAuthRefreshInput{
				TokenEndpoint: tokenServer.URL,
				ClientID:      "client-123",
				RefreshToken:  "refresh-old",
				Resource:      "https://mcp.example.com",
				HTTPClient:    tokenServer.Client(),
				AuthStyle:     oauth2.AuthStyleInParams,
			})
			if err == nil {
				t.Fatal("RefreshOAuthToken returned nil error")
			}
			if test.wantCode != "" && !strings.Contains(err.Error(), test.wantCode) {
				t.Fatalf("RefreshOAuthToken error = %v, want OAuth error code %q", err, test.wantCode)
			}
			if strings.Contains(err.Error(), "do-not-log") {
				t.Fatalf("RefreshOAuthToken error exposed response body: %v", err)
			}
		})
	}
}

func TestDetectAuthRejectsAuthorizationServerWithoutPKCES256(t *testing.T) {
	tests := []struct {
		name   string
		config oauthMCPTestConfig
	}{
		{name: "unsupported", config: oauthMCPTestConfig{}},
		{
			name: "metadata omitted",
			config: oauthMCPTestConfig{
				PKCE:                     true,
				OmitCodeChallengeMethods: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts := newOAuthMCPTestServer(t, test.config)
			_, err := mcp.DetectAuth(
				context.Background(),
				ts.URL+"/mcp",
				mcp.AuthOptions{HTTPClient: ts.Client()},
			)
			if err == nil || !strings.Contains(err.Error(), "PKCE") {
				t.Fatalf("DetectAuth error = %v, want PKCE error", err)
			}
		})
	}
}

func TestDetectAuthFallsBackToWellKnownProtectedResourceMetadata(t *testing.T) {
	ts := newOAuthMCPTestServer(
		t,
		oauthMCPTestConfig{ChallengeScope: "", PKCE: true, OmitResourceMetadataChallenge: true},
	)

	req, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if !req.Required {
		t.Fatal("Required = false, want true")
	}
	if got, want := strings.Join(req.Scopes, " "), "metadata:read"; got != want {
		t.Fatalf("Scopes = %q, want %q", got, want)
	}
}

func TestDetectAuthFallsBackToRootProtectedResourceMetadata(t *testing.T) {
	ts := newOAuthMCPTestServer(
		t,
		oauthMCPTestConfig{PKCE: true, OmitResourceMetadataChallenge: true, ServeRootMetadataOnly: true},
	)

	req, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if !req.Required {
		t.Fatal("Required = false, want true")
	}
	if req.Resource != ts.URL {
		t.Fatalf("Resource = %q, want %q", req.Resource, ts.URL)
	}
}

func TestDetectAuthFallsBackToNextAuthorizationServer(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true, BrokenFirstIssuer: true})

	req, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if req.AuthorizationServer == nil || req.AuthorizationServer.TokenEndpoint != ts.URL+"/token" {
		t.Fatalf("unexpected authorization server metadata: %+v", req.AuthorizationServer)
	}
}

func TestDetectAuthAcceptsMultiTenantIssuerAlias(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{
		PKCE:                   true,
		MultiTenantIssuerAlias: true,
		ServeOIDCPathAppending: true,
	})

	req, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("DetectAuth: %v", err)
	}
	if req.AuthorizationServer == nil || req.AuthorizationServer.TokenEndpoint != ts.URL+"/token" {
		t.Fatalf("unexpected authorization server metadata: %+v", req.AuthorizationServer)
	}
}

func TestDetectAuthRejectsSameOriginIssuerPathMismatch(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{
		PKCE:           true,
		MetadataIssuer: "same-origin-alias",
	})

	_, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err == nil || !strings.Contains(err.Error(), "does not match issuer URL") {
		t.Fatalf("DetectAuth error = %v, want issuer mismatch error", err)
	}
}

func TestDetectAuthErrorsOnUnexpectedProbeStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)

	_, err := mcp.DetectAuth(context.Background(), ts.URL+"/mcp", mcp.AuthOptions{HTTPClient: ts.Client()})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("DetectAuth error = %v, want unexpected HTTP 503 error", err)
	}
}

func TestStartOAuthDoesNotMutateProvidedAuthMetadata(t *testing.T) {
	metadata := mcp.AuthRequirement{
		Required:    true,
		EndpointURL: "https://EXAMPLE.com/mcp",
		AuthorizationServer: &oauthex.AuthServerMeta{
			AuthorizationEndpoint: "https://as.example.com/authorize",
			TokenEndpoint:         "https://as.example.com/token",
		},
	}

	keyWrapper := oauthTestKeyWrapper(t)
	authURL, err := mcp.StartOAuth(context.Background(), mcp.OAuthStartInput{
		ClientID:     "client-123",
		RedirectURI:  "http://127.0.0.1/callback",
		AuthMetadata: &metadata,
		ExpiresAt:    time.Now().UTC().Add(time.Minute),
		KeyWrapper:   keyWrapper,
	})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	flow, err := mcp.OpenOAuthState(context.Background(), keyWrapper, parsed.Query().Get("state"))
	if err != nil {
		t.Fatalf("OpenOAuthState: %v", err)
	}
	if flow.Resource != "https://example.com/mcp" {
		t.Fatalf("Resource = %q, want canonical endpoint URI", flow.Resource)
	}
	if metadata.Resource != "" {
		t.Fatalf("StartOAuth mutated input AuthMetadata.Resource to %q", metadata.Resource)
	}
}

func TestRefreshOAuthTokenSendsClientSecretInBasicAuthHeader(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true})

	_, err := mcp.RefreshOAuthToken(context.Background(), mcp.OAuthRefreshInput{
		TokenEndpoint: ts.URL + "/token",
		ClientID:      "client-123",
		ClientSecret:  "secret-456",
		RefreshToken:  "refresh-old",
		Resource:      ts.URL + "/mcp",
		HTTPClient:    ts.Client(),
		AuthStyle:     oauth2.AuthStyleInHeader,
	})
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	calls := ts.drainTokenCalls()
	if len(calls) != 1 {
		t.Fatalf("token endpoint called %d times, want 1", len(calls))
	}
	if !calls[0].basicSet || calls[0].basicUser != "client-123" {
		t.Fatalf("basic auth user = %q (set=%v), want client-123", calls[0].basicUser, calls[0].basicSet)
	}
	if calls[0].form.Get("client_secret") != "" {
		t.Fatal("client_secret must not be sent in the form when using basic auth")
	}
}

func TestRefreshOAuthTokenAutoDetectFallsBackToFormParams(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{
				PKCE:                     true,
				BasicAuthRejectionStatus: status,
			})

			token, err := mcp.RefreshOAuthToken(context.Background(), mcp.OAuthRefreshInput{
				TokenEndpoint: ts.URL + "/token",
				ClientID:      "client-123",
				ClientSecret:  "secret-456",
				RefreshToken:  "refresh-old",
				Resource:      ts.URL + "/mcp",
				HTTPClient:    ts.Client(),
			})
			if err != nil {
				t.Fatalf("RefreshOAuthToken: %v", err)
			}
			if token.AccessToken != "access-refresh" {
				t.Fatalf("AccessToken = %q, want access-refresh", token.AccessToken)
			}
			calls := ts.drainTokenCalls()
			if len(calls) != 2 {
				t.Fatalf("token endpoint called %d times, want 2", len(calls))
			}
			if !calls[0].basicSet {
				t.Fatal("first attempt should use basic auth")
			}
			if calls[1].basicSet {
				t.Fatal("second attempt should not use basic auth")
			}
			assertForm(t, calls[1].form, "client_id", "client-123")
			assertForm(t, calls[1].form, "client_secret", "secret-456")
		})
	}
}

func TestRefreshOAuthTokenAutoDetectDoesNotRetryGrantFailures(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true, FailGrantsWith: "invalid_grant"})

	_, err := mcp.RefreshOAuthToken(context.Background(), mcp.OAuthRefreshInput{
		TokenEndpoint: ts.URL + "/token",
		ClientID:      "client-123",
		ClientSecret:  "secret-456",
		RefreshToken:  "refresh-old",
		Resource:      ts.URL + "/mcp",
		HTTPClient:    ts.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("RefreshOAuthToken error = %v, want invalid_grant error", err)
	}
	if calls := ts.drainTokenCalls(); len(calls) != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (grant failures must not be retried)", len(calls))
	}
}

func TestRefreshOAuthTokenRejectsResponseMissingAccessToken(t *testing.T) {
	ts := newOAuthMCPTestServer(t, oauthMCPTestConfig{PKCE: true, OmitAccessToken: true})

	_, err := mcp.RefreshOAuthToken(context.Background(), mcp.OAuthRefreshInput{
		TokenEndpoint: ts.URL + "/token",
		ClientID:      "client-123",
		RefreshToken:  "refresh-old",
		Resource:      ts.URL + "/mcp",
		HTTPClient:    ts.Client(),
		AuthStyle:     oauth2.AuthStyleInParams,
	})
	if err == nil || !strings.Contains(err.Error(), "missing access_token") {
		t.Fatalf("RefreshOAuthToken error = %v, want missing access_token error", err)
	}
}

type oauthMCPTestConfig struct {
	ChallengeScope                string
	PKCE                          bool
	OmitRefreshOnRefresh          bool
	OmitResourceMetadataChallenge bool
	OmitAccessToken               bool
	StringExpiresIn               bool
	BasicAuthRejectionStatus      int
	FailGrantsWith                string
	ServeRootMetadataOnly         bool
	BrokenFirstIssuer             bool
	MetadataIssuer                string
	MultiTenantIssuerAlias        bool
	OmitCodeChallengeMethods      bool
	ServeOIDCPathAppending        bool
	ResourceMetadataChallengeURL  string
	InsecureInformationalMetadata bool
}

type unexpectedRoundTripper struct {
	t *testing.T
}

func (transport unexpectedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	transport.t.Fatal("HTTP request was sent before token endpoint validation")
	return nil, errors.New("unexpected HTTP request")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type tokenCall struct {
	form      url.Values
	basicUser string
	basicSet  bool
}

type oauthMCPTestServer struct {
	*httptest.Server
	tokenCalls chan tokenCall
}

func newOAuthMCPTestServer(t *testing.T, cfg oauthMCPTestConfig) *oauthMCPTestServer {
	t.Helper()
	tokenCalls := make(chan tokenCall, 10)
	mux := http.NewServeMux()
	ts := &oauthMCPTestServer{tokenCalls: tokenCalls}
	server := httptest.NewServer(mux)
	ts.Server = server
	t.Cleanup(server.Close)

	resource := server.URL + "/mcp"
	if cfg.ServeRootMetadataOnly {
		resource = server.URL
	}
	issuerPath := "issuer"
	if cfg.MultiTenantIssuerAlias {
		issuerPath = "common/v2.0"
	}
	issuer := server.URL + "/" + issuerPath
	issuers := []string{issuer}
	if cfg.BrokenFirstIssuer {
		issuers = []string{server.URL + "/missing-issuer", issuer}
	}
	authMetadataPath := "/.well-known/oauth-authorization-server/" + issuerPath
	if cfg.ServeOIDCPathAppending {
		authMetadataPath = "/" + issuerPath + "/.well-known/openid-configuration"
	}

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		challenge := `Bearer`
		var params []string
		if !cfg.OmitResourceMetadataChallenge {
			metadataURL := cfg.ResourceMetadataChallengeURL
			if metadataURL == "" {
				metadataURL = server.URL + "/resource-metadata"
			}
			params = append(params, `resource_metadata="`+metadataURL+`"`)
		}
		if cfg.ChallengeScope != "" {
			params = append(params, `scope="`+cfg.ChallengeScope+`"`)
		}
		if len(params) > 0 {
			challenge += " " + strings.Join(params, ", ")
		}
		w.Header().Set("WWW-Authenticate", challenge)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	writeProtectedResourceMetadata := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"resource":              resource,
			"authorization_servers": issuers,
			"scopes_supported":      []string{"metadata:read"},
		})
	}
	if cfg.ServeRootMetadataOnly {
		mux.HandleFunc("/.well-known/oauth-protected-resource", writeProtectedResourceMetadata)
	} else {
		mux.HandleFunc("/resource-metadata", writeProtectedResourceMetadata)
		mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", writeProtectedResourceMetadata)
	}
	mux.HandleFunc(authMetadataPath, func(w http.ResponseWriter, _ *http.Request) {
		methods := []string{}
		if cfg.PKCE {
			methods = []string{"S256"}
		}
		metadataIssuer := issuer
		if cfg.MultiTenantIssuerAlias {
			metadataIssuer = server.URL + "/00000000-0000-0000-0000-000000000000/v2.0"
		} else if cfg.MetadataIssuer == "same-origin-alias" {
			metadataIssuer = server.URL + "/tenant/v2.0"
		} else if cfg.MetadataIssuer != "" {
			metadataIssuer = cfg.MetadataIssuer
		}
		body := map[string]any{
			"issuer":                                metadataIssuer,
			"authorization_endpoint":                server.URL + "/authorize",
			"token_endpoint":                        server.URL + "/token",
			"jwks_uri":                              server.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post", "none"},
		}
		if cfg.InsecureInformationalMetadata {
			body["service_documentation"] = "http://docs.example.com/oauth#overview"
			body["op_policy_uri"] = "http://docs.example.com/policy"
			body["op_tos_uri"] = "http://docs.example.com/terms"
		}
		if !cfg.OmitCodeChallengeMethods {
			body["code_challenge_methods_supported"] = methods
		}
		writeJSON(t, w, body)
	})
	writeOAuthError := func(w http.ResponseWriter, status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
	}
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		call := tokenCall{form: cloneForm(r.PostForm)}
		call.basicUser, _, call.basicSet = r.BasicAuth()
		tokenCalls <- call
		if cfg.BasicAuthRejectionStatus != 0 && call.basicSet {
			writeOAuthError(w, cfg.BasicAuthRejectionStatus, "invalid_client")
			return
		}
		if cfg.FailGrantsWith != "" {
			writeOAuthError(w, http.StatusBadRequest, cfg.FailGrantsWith)
			return
		}
		if call.form.Get("resource") != resource {
			http.Error(w, "missing resource", http.StatusBadRequest)
			return
		}
		switch call.form.Get("grant_type") {
		case "authorization_code":
			if call.form.Get("code_verifier") == "" {
				http.Error(w, "missing verifier", http.StatusBadRequest)
				return
			}
			resp := map[string]any{
				"access_token":  "access-code-123",
				"refresh_token": "refresh-code-123",
				"id_token":      "id-code-123",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"scope":         "files:read",
			}
			if cfg.StringExpiresIn {
				resp["expires_in"] = "3600"
			}
			writeJSON(t, w, resp)
		case "refresh_token":
			resp := map[string]any{
				"access_token": "access-refresh",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"scope":        call.form.Get("scope"),
			}
			if cfg.OmitAccessToken {
				delete(resp, "access_token")
			}
			if !cfg.OmitRefreshOnRefresh {
				resp["refresh_token"] = "refresh-new"
			}
			writeJSON(t, w, resp)
		default:
			http.Error(w, "unsupported grant", http.StatusBadRequest)
		}
	})

	return ts
}

func (s *oauthMCPTestServer) lastTokenForm(t *testing.T) url.Values {
	t.Helper()
	select {
	case call := <-s.tokenCalls:
		return call.form
	default:
		t.Fatal("token endpoint was not called")
		return nil
	}
}

func (s *oauthMCPTestServer) drainTokenCalls() []tokenCall {
	var calls []tokenCall
	for {
		select {
		case call := <-s.tokenCalls:
			calls = append(calls, call)
		default:
			return calls
		}
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func assertQuery(t *testing.T, values url.Values, key, want string) {
	t.Helper()
	if got := values.Get(key); got != want {
		t.Fatalf("query %s = %q, want %q", key, got, want)
	}
}

func assertForm(t *testing.T, values url.Values, key, want string) {
	t.Helper()
	if got := values.Get(key); got != want {
		t.Fatalf("form %s = %q, want %q", key, got, want)
	}
}

func cloneForm(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, value := range values {
		out[key] = append([]string(nil), value...)
	}
	return out
}

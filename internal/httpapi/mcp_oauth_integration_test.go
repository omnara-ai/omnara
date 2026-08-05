//go:build integration

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/mcp"
)

func TestMCPOAuthFlowEndToEndCreatesAndRotatesSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-oauth-e2e")
	fake := newMCPOAuthFakeServer(
		t,
		mcpOAuthFakeConfig{RequireAuth: true, PKCE: true, DCR: true},
	)

	startBody := `{"owner":{"kind":"org"},"mcp_url":"` + fake.URL + `/mcp","name":"linear-mcp","return_to":"/settings/secrets","metadata":{"env":"prod"}}`
	startPath := "/api/v1/orgs/" + project.OrgID + "/secrets/mcp-oauth"
	start := startMCPOAuthFlow(t, handler, startPath, startBody, project.AdminToken)

	authURL, err := url.Parse(start["authorization_url"].(string))
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	authQuery := authURL.Query()
	if got := authQuery.Get("client_id"); got != "dcr-client-123" {
		t.Fatalf("client_id = %q, want DCR-registered client", got)
	}
	if got, want := authQuery.Get("redirect_uri"), "http://omnara.test/api/mcp-oauth/callback"; got != want {
		t.Fatalf("redirect_uri = %q, want %q", got, want)
	}
	if got, want := authQuery.Get("resource"), fake.URL+"/mcp"; got != want {
		t.Fatalf("resource = %q, want %q", got, want)
	}
	if got := authQuery.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	if got := authQuery.Get("scope"); got != "files:read" {
		t.Fatalf("scope = %q, want challenge scope", got)
	}
	state := authQuery.Get("state")
	if state == "" || authQuery.Get("code_challenge") == "" {
		t.Fatalf(
			"authorization URL missing state or code_challenge: %s",
			authURL,
		)
	}
	fake.setCodeChallenge(authQuery.Get("code_challenge"))

	location := completeMCPOAuthCallback(t, handler, state, http.StatusFound)
	redirect, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if redirect.Path != "/settings/secrets" ||
		redirect.Query().Get("mcp_oauth") != "success" {
		t.Fatalf(
			"callback redirect = %q, want success redirect to return_to",
			location,
		)
	}
	secretID := redirect.Query().Get("secret_id")
	if !strings.HasPrefix(secretID, "sec_") {
		t.Fatalf("secret_id = %q, want public secret id", secretID)
	}

	secret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if secret["kind"] != "oauth_token_set" || secret["owner"].(map[string]any)["kind"] != "org" {
		t.Fatalf("unexpected secret: %+v", secret)
	}
	payloadKeys := jsonStringSlice(t, secret["payload_keys"])
	wantPayloadKeys := "access_token client_id client_secret id_token " +
		"mcp_url refresh_token resource scopes token_endpoint token_type"
	if strings.Join(payloadKeys, " ") != wantPayloadKeys {
		t.Fatalf(
			"payload_keys = %v, want OAuth credential fields",
			payloadKeys,
		)
	}
	metadata := secret["metadata"].(map[string]any)
	if len(metadata) != 2 || metadata["env"] != "prod" ||
		metadata["mcp_url"] != fake.URL+"/mcp" {
		t.Fatalf("metadata = %+v, want caller metadata plus mcp_url", metadata)
	}

	tokenForm := fake.lastTokenForm(t)
	if tokenForm.Get("code") != "code-123" ||
		tokenForm.Get("code_verifier") == "" {
		t.Fatalf("token request missing code/verifier: %v", tokenForm)
	}

	replay := doMCPOAuthRequest(
		t,
		handler,
		http.MethodGet,
		"/api/mcp-oauth/callback?code=code-123&state="+url.QueryEscape(state),
		"",
		nil,
	)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf(
			"replayed callback status = %d, want 401 body=%s",
			replay.Code,
			replay.Body.String(),
		)
	}

	restartBody := `{"owner":{"kind":"org"},"mcp_url":"` + fake.URL + `/mcp","name":"linear-mcp","return_to":"/settings/secrets"}`
	restart := startMCPOAuthFlow(
		t,
		handler,
		startPath,
		restartBody,
		project.AdminToken,
	)
	restartURL, err := url.Parse(restart["authorization_url"].(string))
	if err != nil {
		t.Fatalf("parse second authorization_url: %v", err)
	}
	fake.setCodeChallenge(restartURL.Query().Get("code_challenge"))
	location = completeMCPOAuthCallback(
		t,
		handler,
		restartURL.Query().Get("state"),
		http.StatusFound,
	)
	redirect, err = url.Parse(location)
	if err != nil {
		t.Fatalf("parse second callback redirect: %v", err)
	}
	if got := redirect.Query().Get("secret_id"); got != secretID {
		t.Fatalf("re-auth secret_id = %q, want existing %q", got, secretID)
	}
	rotated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if rotated["current_version_number"] != float64(2) {
		t.Fatalf(
			"current_version_number = %v, want 2 after re-auth",
			rotated["current_version_number"],
		)
	}
	rotatedMetadata := rotated["metadata"].(map[string]any)
	if len(rotatedMetadata) != 2 || rotatedMetadata["env"] != "prod" ||
		rotatedMetadata["mcp_url"] != fake.URL+"/mcp" {
		t.Fatalf(
			"metadata after re-auth without metadata = %+v, want original preserved plus mcp_url",
			rotatedMetadata,
		)
	}
}

func TestMCPOAuthProjectAndUserScopedFlows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-oauth-scopes")
	fake := newMCPOAuthFakeServer(
		t,
		mcpOAuthFakeConfig{RequireAuth: true, PKCE: true, DCR: true},
	)

	cases := []struct {
		name          string
		owner         string
		wantOwnerKind string
	}{
		{
			name:          "project",
			owner:         `{"kind":"project","project_id":"` + project.ProjectID + `"}`,
			wantOwnerKind: "project",
		},
		{
			name:          "user",
			owner:         `{"kind":"user"}`,
			wantOwnerKind: "user",
		},
	}
	for _, tc := range cases {
		body := `{"owner":` + tc.owner + `,"mcp_url":"` + fake.URL + `/mcp","name":"` + tc.name + `-mcp"}`
		start := startMCPOAuthFlow(
			t,
			handler,
			"/api/v1/orgs/"+project.OrgID+"/secrets/mcp-oauth",
			body,
			project.AdminToken,
		)
		authURL, err := url.Parse(start["authorization_url"].(string))
		if err != nil {
			t.Fatalf("%s: parse authorization_url: %v", tc.name, err)
		}
		fake.setCodeChallenge(authURL.Query().Get("code_challenge"))
		location := completeMCPOAuthCallback(
			t,
			handler,
			authURL.Query().Get("state"),
			http.StatusFound,
		)
		redirect, err := url.Parse(location)
		if err != nil {
			t.Fatalf("%s: parse redirect: %v", tc.name, err)
		}
		secretID := redirect.Query().Get("secret_id")
		secret := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID,
			"",
			"",
			http.StatusOK,
			authHeaders(project.AdminToken),
		)
		if secret["owner"].(map[string]any)["kind"] != tc.wantOwnerKind ||
			secret["kind"] != "oauth_token_set" {
			t.Fatalf("%s: unexpected secret owner: %+v", tc.name, secret)
		}
	}
}

func TestMCPOAuthStartValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-oauth-validation")
	startPath := "/api/v1/orgs/" + project.OrgID + "/secrets/mcp-oauth"
	store := integrationStoreForHandler(t, handler)
	_, memberToken := createHTTPOrgMemberToken(t, ctx, pool, store, project.OrgUUID, "mcp-owner-denied")
	denied := doMCPOAuthRequest(t, handler, http.MethodPost, startPath,
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"mcp_url":"https://unreachable.invalid/mcp","name":"denied"}`,
		authHeaders(memberToken))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unauthorized owner status = %d, want 403 body=%s", denied.Code, denied.Body.String())
	}

	openServer := newMCPOAuthFakeServer(
		t,
		mcpOAuthFakeConfig{RequireAuth: false},
	)
	rec := doMCPOAuthRequest(
		t,
		handler,
		http.MethodPost,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"`+openServer.URL+`/mcp","name":"open-mcp"}`,
		authHeaders(project.AdminToken),
	)
	if rec.Code != http.StatusConflict {
		t.Fatalf(
			"open server start status = %d, want 409 body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodPost,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"`+openServer.URL+`/mcp"}`,
		authHeaders(project.AdminToken),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name status = %d, want 400", rec.Code)
	}

	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodPost,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"ftp://mcp.example.com/mcp","name":"bad-url"}`,
		authHeaders(project.AdminToken),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid url status = %d, want 400", rec.Code)
	}

	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodPost,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"https://user:pass@mcp.example.com/mcp","name":"userinfo-url"}`,
		authHeaders(project.AdminToken),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"userinfo url status = %d, want 400 body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodPost,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"https://mcp.example.com/mcp","name":"secret-only","client_secret":"s"}`,
		authHeaders(project.AdminToken),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"client_secret without client_id status = %d, want 400",
			rec.Code,
		)
	}

	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodPost,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"https://mcp.example.com/mcp","name":"bad-metadata","metadata":{"count":1}}`,
		authHeaders(project.AdminToken),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"non-string metadata status = %d, want 400 body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	noRegistration := newMCPOAuthFakeServer(
		t,
		mcpOAuthFakeConfig{RequireAuth: true, PKCE: true},
	)
	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodPost,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"`+noRegistration.URL+`/mcp","name":"no-reg"}`,
		authHeaders(project.AdminToken),
	)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"no registration strategy status = %d, want 422 body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	start := startMCPOAuthFlow(
		t,
		handler,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"`+noRegistration.URL+`/mcp","name":"pre-reg","client_id":"pre-registered-1"}`,
		project.AdminToken,
	)
	authURL, err := url.Parse(start["authorization_url"].(string))
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	if got := authURL.Query().Get("client_id"); got != "pre-registered-1" {
		t.Fatalf("client_id = %q, want pre-registered", got)
	}
}

func TestMCPOAuthCallbackStateAndRedirectSafety(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-oauth-callback")
	fake := newMCPOAuthFakeServer(t, mcpOAuthFakeConfig{RequireAuth: true, PKCE: true, DCR: true})
	startPath := "/api/v1/orgs/" + project.OrgID + "/secrets/mcp-oauth"

	start := startMCPOAuthFlow(
		t,
		handler,
		startPath,
		`{"owner":{"kind":"org"},"mcp_url":"`+fake.URL+`/mcp","name":"cb-mcp","return_to":"https://evil.example/phish"}`,
		project.AdminToken,
	)
	authURL, err := url.Parse(start["authorization_url"].(string))
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	state := authURL.Query().Get("state")

	rec := doMCPOAuthRequest(t, handler, http.MethodGet, "/api/mcp-oauth/callback?code=code-123", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback without state status = %d, want 400", rec.Code)
	}

	tampered := state[:len(state)-4] + "AAAA"
	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodGet,
		"/api/mcp-oauth/callback?code=code-123&state="+url.QueryEscape(tampered),
		"",
		nil,
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered state status = %d, want 401", rec.Code)
	}

	expired := expiredTestMCPOAuthState(t)
	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodGet,
		"/api/mcp-oauth/callback?code=code-123&state="+url.QueryEscape(expired),
		"",
		nil,
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired state status = %d, want 401", rec.Code)
	}

	rec = doMCPOAuthRequest(
		t,
		handler,
		http.MethodGet,
		"/api/mcp-oauth/callback?error=access_denied&state="+url.QueryEscape(state),
		"",
		nil,
	)
	if rec.Code != http.StatusFound {
		t.Fatalf(
			"denied callback status = %d, want 302 body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}
	redirect, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse denied redirect: %v", err)
	}
	if !redirect.IsAbs() || redirect.Host != "omnara.test" || redirect.Path != "/" ||
		redirect.Query().Get("mcp_oauth_error") != "access_denied" {
		t.Fatalf("denied redirect = %q, want absolute app-origin error redirect", rec.Header().Get("Location"))
	}
}

func TestMCPOAuthClientMetadataDocument(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	httpsHandler := newIntegrationServer(pool,
		WithPublicURL("https://app.omnara.example"),
		WithSecretKeyWrapper(integrationKeyWrapper()),
		WithAgentConfigOptions(agentconfig.CompileOptions{AllowInsecureLocalMCPHTTP: true}),
	)
	docRec := doMCPOAuthRequest(
		t,
		httpsHandler,
		http.MethodGet,
		"https://app.omnara.example/.well-known/oauth-client.json",
		"",
		nil,
	)
	if docRec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want 200 body=%s", docRec.Code, docRec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(docRec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode metadata document: %v body=%s", err, docRec.Body.String())
	}
	if doc["client_id"] != "https://app.omnara.example/.well-known/oauth-client.json" {
		t.Fatalf("client_id = %v, want metadata document URL", doc["client_id"])
	}
	if doc["client_uri"] != "https://app.omnara.example" {
		t.Fatalf("client_uri = %v, want public URL", doc["client_uri"])
	}
	if doc["token_endpoint_auth_method"] != "none" {
		t.Fatalf(
			"token_endpoint_auth_method = %v, want none",
			doc["token_endpoint_auth_method"],
		)
	}
	redirectURIs := jsonStringSlice(t, doc["redirect_uris"])
	if len(redirectURIs) != 1 || redirectURIs[0] != "https://app.omnara.example/api/mcp-oauth/callback" {
		t.Fatalf("redirect_uris = %v, want callback URL", redirectURIs)
	}

	project := bootstrapPublicHTTPProject(t, httpsHandler, "mcp-oauth-cimd")
	fake := newMCPOAuthFakeServer(t, mcpOAuthFakeConfig{RequireAuth: true, PKCE: true, CIMDSupported: true})
	start := startMCPOAuthFlow(
		t,
		httpsHandler,
		"/api/v1/orgs/"+project.OrgID+"/secrets/mcp-oauth",
		`{"owner":{"kind":"org"},"mcp_url":"`+fake.URL+`/mcp","name":"cimd-mcp","return_to":"/settings/secrets"}`,
		project.AdminToken,
	)
	authURL, err := url.Parse(start["authorization_url"].(string))
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	if got := authURL.Query().Get("client_id"); got != "https://app.omnara.example/.well-known/oauth-client.json" {
		t.Fatalf("client_id = %q, want CIMD URL", got)
	}
	if got := authURL.Query().Get("redirect_uri"); got != "https://app.omnara.example/api/mcp-oauth/callback" {
		t.Fatalf("redirect_uri = %q, want public callback URL", got)
	}
	fake.setCodeChallenge(authURL.Query().Get("code_challenge"))
	state := authURL.Query().Get("state")
	callback := doMCPOAuthRequest(
		t,
		httpsHandler,
		http.MethodGet,
		"https://app.omnara.example/api/mcp-oauth/callback?code=code-123&state="+url.QueryEscape(state),
		"",
		nil,
	)
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302 body=%s", callback.Code, callback.Body.String())
	}
	location, err := url.Parse(callback.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if location.Scheme != "https" || location.Host != "app.omnara.example" || location.Path != "/settings/secrets" ||
		location.Query().Get("mcp_oauth") != "success" {
		t.Fatalf("callback redirect = %q, want app-origin success redirect", callback.Header().Get("Location"))
	}

	httpHandler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	rec := doMCPOAuthRequest(
		t,
		httpHandler,
		http.MethodGet,
		"/.well-known/oauth-client.json",
		"",
		nil,
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("http client metadata status = %d, want 404", rec.Code)
	}
}

type mcpOAuthFakeConfig struct {
	RequireAuth   bool
	PKCE          bool
	DCR           bool
	CIMDSupported bool
}

type mcpOAuthFakeServer struct {
	*httptest.Server

	mu            sync.Mutex
	codeChallenge string
	tokenForms    []url.Values
}

func newMCPOAuthFakeServer(
	t *testing.T,
	cfg mcpOAuthFakeConfig,
) *mcpOAuthFakeServer {
	t.Helper()
	fake := &mcpOAuthFakeServer{}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	fake.Server = server
	t.Cleanup(server.Close)

	resource := server.URL + "/mcp"
	issuer := server.URL + "/issuer"

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !cfg.RequireAuth {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":-1,"result":{}}`))
			return
		}
		w.Header().
			Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/prm", scope="files:read"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	mux.HandleFunc("/prm", func(w http.ResponseWriter, _ *http.Request) {
		writeFakeJSON(t, w, map[string]any{
			"resource":              resource,
			"authorization_servers": []string{issuer},
			"scopes_supported":      []string{"files:read"},
		})
	})
	mux.HandleFunc(
		"/.well-known/oauth-authorization-server/issuer",
		func(w http.ResponseWriter, _ *http.Request) {
			methods := []string{}
			if cfg.PKCE {
				methods = []string{"S256"}
			}
			metadata := map[string]any{
				"issuer":                   issuer,
				"authorization_endpoint":   server.URL + "/authorize",
				"token_endpoint":           server.URL + "/token",
				"response_types_supported": []string{"code"},
				"grant_types_supported": []string{
					"authorization_code",
					"refresh_token",
				},
				"code_challenge_methods_supported": methods,
				"token_endpoint_auth_methods_supported": []string{
					"client_secret_post",
					"none",
				},
			}
			if cfg.DCR {
				metadata["registration_endpoint"] = server.URL + "/register"
			}
			if cfg.CIMDSupported {
				metadata["client_id_metadata_document_supported"] = true
			}
			writeFakeJSON(t, w, metadata)
		},
	)
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var registration map[string]any
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]any{
			"client_id":     "dcr-client-123",
			"client_secret": "dcr-secret-456",
		}); err != nil {
			t.Errorf("encode registration response: %v", err)
		}
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fake.mu.Lock()
		fake.tokenForms = append(fake.tokenForms, r.PostForm)
		expectedChallenge := fake.codeChallenge
		fake.mu.Unlock()
		if r.PostForm.Get("grant_type") != "authorization_code" ||
			r.PostForm.Get("code") != "code-123" {
			http.Error(w, "invalid grant", http.StatusBadRequest)
			return
		}
		if cfg.DCR {
			secret := r.PostForm.Get("client_secret")
			if _, basicSecret, ok := r.BasicAuth(); ok && secret == "" {
				secret = basicSecret
			}
			if secret != "dcr-secret-456" {
				http.Error(
					w,
					"invalid client credentials",
					http.StatusUnauthorized,
				)
				return
			}
		}
		if r.PostForm.Get("resource") != resource {
			http.Error(w, "missing resource", http.StatusBadRequest)
			return
		}
		verifier := r.PostForm.Get("code_verifier")
		hashed := sha256.Sum256([]byte(verifier))
		if expectedChallenge == "" ||
			base64.RawURLEncoding.EncodeToString(
				hashed[:],
			) != expectedChallenge {
			http.Error(w, "pkce verification failed", http.StatusBadRequest)
			return
		}
		writeFakeJSON(t, w, map[string]any{
			"access_token":  "access-123",
			"refresh_token": "refresh-123",
			"id_token":      "id-123",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "files:read",
		})
	})
	return fake
}

func (s *mcpOAuthFakeServer) setCodeChallenge(challenge string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codeChallenge = challenge
}

func (s *mcpOAuthFakeServer) lastTokenForm(t *testing.T) url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tokenForms) == 0 {
		t.Fatal("token endpoint was not called")
	}
	return s.tokenForms[len(s.tokenForms)-1]
}

func newMCPOAuthIntegrationServer(
	pool *pgxpool.Pool,
	publicURL string,
) http.Handler {
	return newIntegrationServer(
		pool,
		WithPublicURL(publicURL),
		WithSecretKeyWrapper(integrationKeyWrapper()),
		WithAgentConfigOptions(
			agentconfig.CompileOptions{AllowInsecureLocalMCPHTTP: true},
		),
	)
}

func expiredTestMCPOAuthState(t *testing.T) string {
	t.Helper()
	authURL, err := mcp.StartOAuth(context.Background(), mcp.OAuthStartInput{
		ClientID:    "client-123",
		RedirectURI: "http://omnara.test/api/mcp-oauth/callback",
		AuthMetadata: &mcp.AuthRequirement{
			EndpointURL: "https://mcp.example.com/mcp",
			AuthorizationServer: &oauthex.AuthServerMeta{
				AuthorizationEndpoint: "https://as.example.com/authorize",
				TokenEndpoint:         "https://as.example.com/token",
			},
		},
		ExpiresAt:  time.Now().UTC().Add(-time.Minute),
		KeyWrapper: integrationKeyWrapper(),
	})
	if err != nil {
		t.Fatalf("start expired test flow: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse expired test authorization URL: %v", err)
	}
	return parsed.Query().Get("state")
}

func startMCPOAuthFlow(
	t *testing.T,
	handler http.Handler,
	path, body, token string,
) map[string]any {
	t.Helper()
	return requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusCreated,
		authHeaders(token),
	)
}

func completeMCPOAuthCallback(
	t *testing.T,
	handler http.Handler,
	state string,
	wantStatus int,
) string {
	t.Helper()
	rec := doMCPOAuthRequest(
		t,
		handler,
		http.MethodGet,
		"/api/mcp-oauth/callback?code=code-123&state="+url.QueryEscape(state),
		"",
		nil,
	)
	if rec.Code != wantStatus {
		t.Fatalf(
			"callback status = %d, want %d body=%s",
			rec.Code,
			wantStatus,
			rec.Body.String(),
		)
	}
	return rec.Header().Get("Location")
}

func doMCPOAuthRequest(
	t *testing.T,
	handler http.Handler,
	method, path, body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func writeFakeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode fake response: %v", err)
	}
}

func jsonStringSlice(t *testing.T, value any) []string {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("value %v is not a JSON array", value)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(string))
	}
	return out
}

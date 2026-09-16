package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

const (
	testAppID        = "9007199254740989"
	testInstallID    = "9007199254740990"
	testRepositoryID = "9007199254740991"
	testOwner        = `{"id":8,"node_id":"O_org","login":"acme","type":"Organization"}`
	testPermissions  = `{"pull_requests":"write","issues":"read","metadata":"read"}`
)

func newTestConfig(t *testing.T) Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return Config{
		AppID: testAppID, ClientID: "Iv1.test-client", ClientSecret: "fake-client-secret",
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
		})),
	}
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := newTestConfig(t)
	config.HTTPClient = server.Client()
	config.APIURL = server.URL + "/api"
	config.TokenURL = server.URL + "/token"
	client, err := NewClient(config)
	require.NoError(t, err)
	return client
}

func testUser() UserOAuth {
	return UserOAuth{AccessToken: "ghu_fake-setup-user", ExpiresAt: time.Now().Add(time.Hour)}
}

func appResponse() string {
	return `{"id":` + testAppID + `,"client_id":"Iv1.test-client","slug":"acme-reviewer",` +
		`"owner":` + testOwner + `,"permissions":` + testPermissions + `}`
}

func installationResponse() string {
	return `{"id":` + testInstallID + `,"app_id":` + testAppID + `,"account":` + testOwner +
		`,"permissions":` + testPermissions + `,"suspended_at":null}`
}

func repositoryResponse(id string, admin bool) string {
	return `{"id":` + id + `,"node_id":"R_private","name":"private-repo",` +
		`"full_name":"acme/private-repo","owner":` + testOwner + `,"private":true,` +
		`"permissions":{"admin":` + fmt.Sprint(admin) + `,"push":true,"pull":true}}`
}

func respond(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func TestAuthorizeURLUsesRegisteredClientAndPKCE(t *testing.T) {
	t.Parallel()
	client, err := NewClient(newTestConfig(t))
	require.NoError(t, err)
	verifier := strings.Repeat("v", 43)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	input := AuthorizeInput{
		RedirectURI: "https://omnara.test/callback", State: "bound-user-project-app-state", CodeChallenge: challenge,
	}
	value, err := client.AuthorizeURL(input)
	require.NoError(t, err)
	parsed, err := url.Parse(value)
	require.NoError(t, err)
	require.Equal(t, "github.com", parsed.Host)
	require.Equal(t, "/login/oauth/authorize", parsed.Path)
	require.Equal(t, client.clientID, parsed.Query().Get("client_id"))
	require.Equal(t, input.State, parsed.Query().Get("state"))
	require.Equal(t, input.RedirectURI, parsed.Query().Get("redirect_uri"))
	require.Equal(t, challenge, parsed.Query().Get("code_challenge"))
	require.Equal(t, "S256", parsed.Query().Get("code_challenge_method"))
	require.False(t, parsed.Query().Has("scope"))
	require.NotContains(t, value, client.clientSecret)
	input.CodeChallenge = ""
	value, err = client.AuthorizeURL(input)
	require.NoError(t, err)
	require.NotContains(t, value, "code_challenge")
	input.State = ""
	_, err = client.AuthorizeURL(input)
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestExchangeCodeReturnsOnlyExpiringSetupToken(t *testing.T) {
	t.Parallel()
	for _, verifier := range []string{"", strings.Repeat("v", 43)} {
		t.Run(fmt.Sprintf("pkce=%v", verifier != ""), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/token" || r.URL.RawQuery != "" {
					t.Errorf("unexpected OAuth route: %s %s", r.Method, r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.PostForm.Get("client_secret") != "fake-client-secret" ||
					r.PostForm.Get("code_verifier") != verifier || r.PostForm.Get("code") != "test-code" {
					t.Error("OAuth request did not carry the correlated credentials/PKCE")
				}
				respond(w, `{"access_token":"ghu_short-lived","token_type":"bearer","expires_in":28800,`+
					`"scope":"","refresh_token":"ghr_must-not-escape"}`)
			})
			started := time.Now()
			user, err := client.ExchangeCode(t.Context(), ExchangeInput{
				Code: "test-code", RedirectURI: "https://omnara.test/callback", CodeVerifier: verifier,
			})
			require.NoError(t, err)
			require.Equal(t, "ghu_short-lived", user.AccessToken)
			require.WithinDuration(t, started.Add(8*time.Hour), user.ExpiresAt, time.Second)
			require.EqualValues(t, 1, calls.Load())
			encoded, err := json.Marshal(user)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "ghr_")
		})
	}
}

func TestExchangeRejectsUnboundedOrInvalidCredentials(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want error
	}{
		{"nonexpiring", `{"access_token":"ghu_test","token_type":"bearer"}`, ErrExpiringTokenRequired},
		{"long-lived", `{"access_token":"ghu_test","token_type":"bearer","expires_in":999999}`, ErrInvalidResponse},
		{"PAT", `{"access_token":"ghp_test","token_type":"bearer","expires_in":28800}`, ErrInvalidResponse},
		{"scope", `{"access_token":"ghu_test","token_type":"bearer","expires_in":28800,"scope":"repo"}`, ErrInvalidResponse},
		{"rejected", `{"error":"raw-secret-provider-message"}`, ErrOAuthExchange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { respond(w, tc.body) })
			_, err := client.ExchangeCode(t.Context(), ExchangeInput{
				Code: "test-code", RedirectURI: "https://omnara.test/callback",
			})
			require.ErrorIs(t, err, tc.want)
			require.NotContains(t, err.Error(), "raw-secret")
		})
	}
}

func TestVerifyAppJWTAndInstallationBootstrap(t *testing.T) {
	t.Parallel()
	tokens := make(chan string, 1)
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		tokens <- strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Method != http.MethodGet || r.URL.Path != "/api/app" {
			t.Errorf("unexpected app route: %s %s", r.Method, r.URL.Path)
		}
		respond(w, appResponse())
	})
	app, err := client.VerifyApp(t.Context())
	require.NoError(t, err)
	require.Equal(t, testAppID, app.ID)
	require.Equal(t, "acme-reviewer", app.Slug)
	require.Equal(t, "https://github.com/apps/acme-reviewer/installations/new", app.InstallationURL)
	require.Equal(t, "acme", app.Owner.Login)
	require.Equal(t, "write", app.Permissions.PullRequests)
	jws, err := jose.ParseSigned(<-tokens, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	claimsJSON, err := jws.Verify(&client.privateKey.PublicKey)
	require.NoError(t, err)
	var claims struct {
		Issuer string `json:"iss"`
		Iat    int64  `json:"iat"`
		Exp    int64  `json:"exp"`
	}
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	require.Equal(t, client.clientID, claims.Issuer)
	require.WithinDuration(t, time.Now().Add(-time.Minute), time.Unix(claims.Iat, 0), 2*time.Second)
	require.WithinDuration(t, time.Now().Add(9*time.Minute), time.Unix(claims.Exp, 0), 2*time.Second)
}

func TestSelectionRevalidatesAdminAndAppInstallationWithoutSourceAccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("setup mutated GitHub: %s", r.Method)
		}
		switch r.URL.Path {
		case "/api/app":
			respond(w, appResponse())
		case "/api/repos/acme/private-repo/installation":
			respond(w, installationResponse())
		case "/api/repos/acme/private-repo":
			if r.Header.Get("Authorization") != "Bearer "+testUser().AccessToken || r.URL.RawQuery != "" {
				t.Error("repository proof must use the user token and the selected path")
			}
			respond(w, repositoryResponse(testRepositoryID, true))
		default:
			t.Errorf("unexpected native call, source/token minting is forbidden: %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	selection, err := client.VerifySelection(t.Context(), testUser(), testInstallID, testRepositoryID, "acme/private-repo")
	require.NoError(t, err)
	require.Equal(t, testInstallID, selection.Installation.ID)
	require.Equal(t, testRepositoryID, selection.Repository.ID)
	require.Equal(t, "R_private", selection.Repository.NodeID)
	require.True(t, selection.Repository.Admin)
	require.True(t, selection.Repository.Private)
	require.Equal(t, "acme/private-repo", selection.Repository.FullName)
	require.EqualValues(t, 3, calls.Load())
}

func TestSelectionRejectsAuthorityMismatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		app          string
		installation string
		repository   string
		want         error
	}{
		{name: "wrong App", app: strings.Replace(appResponse(), testAppID, "22", 1), want: ErrAppMismatch},
		{name: "wrong OAuth client", app: strings.Replace(appResponse(), "Iv1.test-client", "Iv1.other", 1),
			want: ErrAppMismatch},
		{name: "foreign installation", installation: strings.Replace(installationResponse(), testAppID, "22", 1),
			want: ErrAppMismatch},
		{name: "substituted installation", installation: strings.Replace(installationResponse(), testInstallID, "22", 1),
			want: ErrInvalidResponse},
		{name: "suspended", installation: strings.Replace(installationResponse(), "null", `"2026-09-01T00:00:00Z"`, 1),
			want: ErrInstallationUnavailable},
		{name: "read-only PR", installation: strings.Replace(installationResponse(), `"write"`, `"read"`, 1),
			want: ErrMissingPermissions},
		{name: "missing Issues", installation: strings.Replace(installationResponse(), `"issues":"read",`, "", 1),
			want: ErrMissingPermissions},
		{name: "missing Metadata", installation: strings.Replace(installationResponse(), `,"metadata":"read"`, "", 1),
			want: ErrMissingPermissions},
		{name: "writer is not admin", repository: repositoryResponse(testRepositoryID, false),
			want: ErrRepositoryAdminRequired},
		{name: "wrong repository", repository: repositoryResponse("22", true), want: ErrSelectionNotAccessible},
		{name: "wrong repository owner",
			repository: strings.Replace(repositoryResponse(testRepositoryID, true), `"id":8`, `"id":9`, 1),
			want:       ErrInvalidResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/app":
					body := tc.app
					if body == "" {
						body = appResponse()
					}
					respond(w, body)
				case "/api/repos/acme/private-repo/installation":
					body := tc.installation
					if body == "" {
						body = installationResponse()
					}
					respond(w, body)
				default:
					body := tc.repository
					if body == "" {
						body = repositoryResponse(testRepositoryID, true)
					}
					respond(w, body)
				}
			})
			_, err := client.VerifySelection(t.Context(), testUser(), testInstallID, testRepositoryID, "acme/private-repo")
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestExpiredSetupAuthorityMakesNoRequests(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("expired token made a request") })
	user := testUser()
	user.ExpiresAt = time.Now().Add(-time.Second)
	_, err := client.ListInstallations(t.Context(), user, 1)
	require.ErrorIs(t, err, ErrOAuthExpired)
	_, err = client.ListRepositories(t.Context(), user, testInstallID, 1)
	require.ErrorIs(t, err, ErrOAuthExpired)
	_, err = client.VerifySelection(t.Context(), user, testInstallID, testRepositoryID, "acme/private-repo")
	require.ErrorIs(t, err, ErrOAuthExpired)
}

func TestCanceledSetupRequestPreservesContextError(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { respond(w, appResponse()) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.VerifyApp(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

//go:build integration

package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

// Keep the configured origin github.com while routing requests entirely to a
// local provider. Guided setup must not guess an Enterprise web origin.
type githubSetupLocalTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t githubSetupLocalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
		return nil, fmt.Errorf("unexpected GitHub API origin")
	}
	clone := r.Clone(r.Context())
	copied := *r.URL
	copied.Scheme, copied.Host = t.target.Scheme, t.target.Host
	clone.URL = &copied
	return t.base.RoundTrip(clone)
}

type githubManifestFixture struct {
	handler     http.Handler
	project     publicHTTPProject
	app         integrationstore.ProjectAppRecord
	privateKey  string
	conversions atomic.Int32
	inspections atomic.Int32
	onConvert   func()
}

func newGitHubManifestFixture(t *testing.T, options ...Option) *githubManifestFixture {
	t.Helper()
	return newGitHubManifestFixtureWithAppID(t, 123, options...)
}

func newGitHubManifestFixtureWithAppID(t *testing.T, appID int64, options ...Option) *githubManifestFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &githubManifestFixture{
		privateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/app-manifests/"):
			f.conversions.Add(1)
			if f.onConvert != nil {
				f.onConvert()
			}
			if strings.Contains(r.URL.Path, "failed") || f.conversions.Load() > 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"message":"private conversion response"}`)
				return
			}
			writeJSON(
				w,
				http.StatusCreated,
				map[string]any{
					"id":             appID,
					"name":           "Verified Helper",
					"slug":           "verified-helper",
					"owner":          map[string]any{"id": 888, "login": "octo-org", "type": "Organization"},
					"pem":            f.privateKey,
					"webhook_secret": githubJourneyWebhookSecret,
					"client_secret":  "unused-client-secret",
				},
			)
		case r.URL.Path == "/app":
			f.inspections.Add(1)
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{
					"id":    appID,
					"name":  "Verified Helper",
					"slug":  "verified-helper",
					"owner": map[string]any{"id": 888, "login": "octo-org", "type": "Organization"},
				},
			)
		case r.URL.Path == "/app/installations":
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("Link", `<https://api.github.com/app/installations?per_page=100&page=2>; rel="next"`)
				writeJSON(
					w,
					http.StatusOK,
					[]any{
						map[string]any{
							"id":          456,
							"app_id":      appID,
							"account":     map[string]any{"id": 888, "login": "octo-org", "type": "Organization"},
							"target_type": "Organization",
						},
					},
				)
			} else {
				writeJSON(w, http.StatusOK, []any{})
			}
		case r.URL.Path == "/app/installations/456":
			writeJSON(w, http.StatusOK, map[string]any{"id": 456, "app_id": appID})
		case r.URL.Path == "/app/installations/999":
			writeJSON(w, http.StatusOK, map[string]any{"id": 999, "app_id": 999})
		case r.URL.Path == "/app/installations/456/access_tokens":
			writeJSON(
				w,
				http.StatusCreated,
				map[string]any{"token": "metadata-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
			)
		case r.URL.Path == "/users/verified-helper[bot]":
			writeJSON(w, http.StatusOK, map[string]any{"id": 999, "login": "verified-helper[bot]", "type": "Bot"})
		case r.URL.Path == "/installation/token":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	target, err := url.Parse(provider.URL)
	require.NoError(t, err)
	options = append([]Option{
		WithPublicURL("https://omnara.test"),
		WithGitHubClientConfig(github.Config{
			HTTPClient: &http.Client{Transport: githubSetupLocalTransport{target: target, base: provider.Client().Transport}},
		}),
	}, options...)
	f.handler = newIntegrationServer(openIntegrationDB(t, t.Context()), options...)
	f.project = bootstrapPublicHTTPProject(t, f.handler, "github-manifest")
	f.app = createSetupHTTPApp(t, f.handler, f.project, "github-guided", appdefinition.GitHubPR)
	return f
}

func (f *githubManifestFixture) headers() map[string]string {
	return map[string]string{
		"Cookie": httpauth.BrowserSessionHostCookieName + "=" + f.project.AdminSession + "; " +
			httpauth.CSRFHostCookieName + "=" + f.project.AdminCSRF,
		"Origin":                "https://omnara.test",
		httpauth.CSRFHeaderName: f.project.AdminCSRF,
	}
}
func (f *githubManifestFixture) path(t *testing.T) string {
	return f.project.ProjectPath + "/apps/" + testPublicID(t, publicid.KindProjectApp, f.app.ID) + "/github-setup"
}
func (f *githubManifestFixture) start(t *testing.T) (map[string]any, string, githubManifestState) {
	t.Helper()
	response := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t),
		projectAppHTTPJSON(t, map[string]any{"expected_setup_revision": f.app.SetupRevision}),
		"",
		http.StatusCreated,
		f.headers(),
	)
	registration, err := url.Parse(testutil.RequireType[string](t, response["registration_url"]))
	require.NoError(t, err)
	token := registration.Query().Get("state")
	state, err := (&Server{secretKeyWrapper: integrationKeyWrapper()}).decodeGitHubManifestState(t.Context(), token)
	require.NoError(t, err)
	return response, token, state
}
func githubManifestCallback(handler http.Handler, token, code, session string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(
		http.MethodGet,
		"https://omnara.test"+githubManifestCallbackPath+"?"+url.Values{"state": {token}, "code": {code}}.Encode(),
		nil,
	)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: session})
	}
	return performRequest(handler, r)
}
func (f *githubManifestFixture) saveCredentials(t *testing.T) string {
	t.Helper()
	_, token, _ := f.start(t)
	response := githubManifestCallback(f.handler, token, strings.Repeat("a", 40), f.project.AdminSession)
	require.Equal(t, http.StatusFound, response.Code, response.Body.String())
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "credentials_saved", location.Query().Get("github_setup"))
	return location.Query().Get("credentials_secret_ref")
}

func TestGitHubManifestRegistrationSavesRecoverableSecretWithoutConnecting(t *testing.T) {
	t.Parallel()
	f := newGitHubManifestFixture(t)
	response, token, state := f.start(t)
	require.WithinDuration(t, time.Now().Add(time.Hour), state.ExpiresAt, time.Second)
	manifest := testutil.RequireType[map[string]any](t, response["manifest"])
	canonical := "https://omnara.test/projects/" + f.project.ProjectID + "/apps/" + testPublicID(
		t,
		publicid.KindProjectApp,
		f.app.ID,
	)
	require.Equal(t, canonical, manifest["setup_url"])
	require.Equal(t, "https://omnara.test"+githubManifestCallbackPath, manifest["redirect_url"])
	require.Equal(
		t,
		map[string]any{"url": "https://omnara.test" + GitHubSharedEventsPath, "active": true},
		manifest["hook_attributes"],
	)
	require.Equal(t, map[string]any{"pull_requests": "write", "issues": "read"}, manifest["default_permissions"])
	require.ElementsMatch(
		t,
		[]any{"pull_request", "issue_comment", "pull_request_review", "pull_request_review_comment"},
		manifest["default_events"],
	)
	for _, flag := range []string{"public", "request_oauth_on_install", "setup_on_update"} {
		require.Equal(t, false, manifest[flag])
	}
	require.NotContains(t, manifest, "callback_urls")
	require.Equal(t, f.project.AdminUserUUID, state.UserID)
	callback := githubManifestCallback(f.handler, token, strings.Repeat("a", 40), f.project.AdminSession)
	require.Equal(t, http.StatusFound, callback.Code, callback.Body.String())
	location, err := url.Parse(callback.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, canonical, location.Scheme+"://"+location.Host+location.Path)
	require.Equal(t, "credentials_saved", location.Query().Get("github_setup"))
	require.Empty(t, location.Query().Get("github_setup_error"))
	ref := location.Query().Get("credentials_secret_ref")
	secretID, err := publicid.Decode(publicid.KindSecret, ref)
	require.NoError(t, err)
	secret, err := f.project.Store.Secrets().
		ReadProjectAvailableSecretPayload(t.Context(), secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			SecretID: secretID, Kind: secrets.KindGitHubAppCredentials,
		})
	require.NoError(t, err)
	require.Equal(
		t,
		secrets.Payload{
			secrets.KeyAppID:         "123",
			secrets.KeyPrivateKey:    f.privateKey,
			secrets.KeyWebhookSecret: githubJourneyWebhookSecret,
		},
		secret.Payload,
	)
	current, err := f.project.Store.Integrations().GetProjectApp(t.Context(), f.project.ProjectUUID, f.app.ID)
	require.NoError(t, err)
	require.Equal(t, f.app, current, "registration must not activate the app or change its setup revision")
	require.NotContains(t, callback.Header().Get("Location"), "secret=")
	require.NotContains(t, callback.Body.String(), f.privateKey)
	replay := githubManifestCallback(f.handler, token, strings.Repeat("a", 40), f.project.AdminSession)
	require.Contains(t, replay.Header().Get("Location"), "github_setup_error=conversion_failed")
	require.NotContains(t, replay.Body.String(), "private conversion response")
	// The failed one-time exchange and canceled installation leave the saved key available.
	_, err = f.project.Store.Secrets().
		ReadProjectAvailableSecretPayload(t.Context(), secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			SecretID: secretID, Kind: secrets.KindGitHubAppCredentials,
		})
	require.NoError(t, err)
}

func TestGitHubManifestUsesAPIOriginOnlyForWebhook(t *testing.T) {
	t.Parallel()
	for _, apiURL := range []string{"https://api.omnara.test", "https://api.omnara.test:8443/api/v1/"} {
		t.Run(apiURL, func(t *testing.T) {
			t.Parallel()
			f := newGitHubManifestFixture(t, WithPublicAPIURL(apiURL))
			response, token, _ := f.start(t)
			manifest := testutil.RequireType[map[string]any](t, response["manifest"])
			api, err := url.Parse(apiURL)
			require.NoError(t, err)
			webhookURL := api.Scheme + "://" + api.Host + GitHubSharedEventsPath
			require.Equal(t, map[string]any{"url": webhookURL, "active": true}, manifest["hook_attributes"])
			require.Equal(t, "https://omnara.test"+githubManifestCallbackPath, manifest["redirect_url"])
			canonical := "https://omnara.test/projects/" + f.project.ProjectID + "/apps/" +
				testPublicID(t, publicid.KindProjectApp, f.app.ID)
			require.Equal(t, canonical, manifest["setup_url"])
			require.Equal(t, canonical, manifest["url"])
			// The API host reaches shared intake (which rejects the missing hint),
			// without accidentally nesting the integration route below /api/v1.
			webhook := httptest.NewRequest(http.MethodPost, webhookURL, nil)
			require.Equal(t, http.StatusBadRequest, performRequest(f.handler, webhook).Code)
			callback := githubManifestCallback(f.handler, token, strings.Repeat("a", 40), f.project.AdminSession)
			require.Equal(t, http.StatusFound, callback.Code, callback.Body.String())
			location, err := url.Parse(callback.Header().Get("Location"))
			require.NoError(t, err)
			require.Equal(t, canonical, location.Scheme+"://"+location.Host+location.Path)
			require.Equal(t, "credentials_saved", location.Query().Get("github_setup"))
		})
	}
}

func TestGitHubManifestCallbackSavesCredentialsForLargestAppID(t *testing.T) {
	t.Parallel()
	f := newGitHubManifestFixtureWithAppID(t, math.MaxInt64)
	_, token, state := f.start(t)
	response := githubManifestCallback(f.handler, token, strings.Repeat("a", 40), f.project.AdminSession)
	require.Equal(t, http.StatusFound, response.Code, response.Body.String())
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "credentials_saved", location.Query().Get("github_setup"))
	secretID, err := publicid.Decode(publicid.KindSecret, location.Query().Get("credentials_secret_ref"))
	require.NoError(t, err)
	secret, err := f.project.Store.Secrets().GetSecret(t.Context(), f.project.OrgUUID, secretID)
	require.NoError(t, err)
	require.Equal(t, "github-9223372036854775807-"+state.FlowID.String(), secret.Name)
	require.Len(t, secret.Name, 63)
	credential, err := f.project.Store.Secrets().ReadProjectAvailableSecretPayload(
		t.Context(), secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			SecretID: secretID, Kind: secrets.KindGitHubAppCredentials,
		},
	)
	require.NoError(t, err)
	require.Equal(t, "9223372036854775807", credential.Payload[secrets.KeyAppID])
	require.Equal(t, f.privateKey, credential.Payload[secrets.KeyPrivateKey])
}

func TestGitHubManifestCallbackRejectsInvalidAuthorityBeforeConversion(t *testing.T) {
	t.Parallel()
	f := newGitHubManifestFixture(t)
	_, token, state := f.start(t)
	other := bootstrapPublicHTTPProject(t, f.handler, "github-other-user")
	wrongType := createSetupHTTPApp(t, f.handler, f.project, "slack-other-type", appdefinition.SlackThread)
	for _, tc := range []struct {
		name, token, session string
		want                 int
	}{
		{"tampered", token + "bad", f.project.AdminSession, http.StatusUnauthorized},
		{"no browser", token, "", http.StatusUnauthorized},
		{"wrong user", token, other.AdminSession, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := githubManifestCallback(f.handler, tc.token, strings.Repeat("a", 40), tc.session)
			require.Equal(t, tc.want, r.Code, r.Body.String())
			require.Zero(t, f.conversions.Load())
		})
	}
	for _, mutate := range []func(*githubManifestState){
		func(s *githubManifestState) { s.ExpiresAt = time.Now().Add(-time.Second) },
		func(s *githubManifestState) { s.ProjectID = other.ProjectUUID },
		func(s *githubManifestState) { s.AppID = uuid.New() },
		func(s *githubManifestState) { s.AppID = wrongType.ID },
	} {
		invalid := state
		mutate(&invalid)
		token, err := (&Server{secretKeyWrapper: integrationKeyWrapper()}).encodeGitHubManifestState(t.Context(), invalid)
		require.NoError(t, err)
		r := githubManifestCallback(f.handler, token, strings.Repeat("a", 40), f.project.AdminSession)
		require.True(
			t,
			r.Code >= 400 || strings.Contains(r.Header().Get("Location"), "github_setup_error="),
			"%d %s",
			r.Code,
			r.Body.String(),
		)
	}
	require.Zero(t, f.conversions.Load())
}

func TestGitHubManifestStartRequiresBrowserRevisionAndNeverConnectedApp(t *testing.T) {
	t.Parallel()
	f := newGitHubManifestFixture(t)
	body := projectAppHTTPJSON(t, map[string]any{"expected_setup_revision": f.app.SetupRevision})
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t),
		body,
		"",
		http.StatusForbidden,
		authHeaders(f.project.AdminToken),
	)
	headers := f.headers()
	delete(headers, httpauth.CSRFHeaderName)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.path(t), body, "", http.StatusForbidden, headers)
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t),
		`{"expected_setup_revision":999}`,
		"",
		http.StatusConflict,
		f.headers(),
	)
	response := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t),
		`{"expected_setup_revision":1,"organization":"octo-org","app_name":"GitHub name"}`,
		"",
		http.StatusCreated,
		f.headers(),
	)
	require.True(
		t,
		strings.HasPrefix(
			testutil.RequireType[string](t, response["registration_url"]),
			"https://github.com/organizations/octo-org/settings/apps/new?",
		),
	)
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t),
		`{"expected_setup_revision":1,"organization":"evil/path"}`,
		"",
		http.StatusBadRequest,
		f.headers(),
	)
}

func TestGitHubInstallationInspectionUsesSavedSecretAndExplicitPagination(t *testing.T) {
	t.Parallel()
	f := newGitHubManifestFixture(t)
	ref := f.saveCredentials(t)
	response := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t)+"/installations",
		projectAppHTTPJSON(t, map[string]any{"credentials_secret_ref": ref}),
		"",
		http.StatusOK,
		f.headers(),
	)
	require.Equal(t, "123", response["provider_app_id"])
	require.Equal(t, "Verified Helper", response["name"])
	require.Equal(t, "verified-helper", response["slug"])
	require.Equal(t, float64(2), response["next_page"])
	require.Equal(t, "https://github.com/apps/verified-helper/installations/new?state="+ref, response["install_url"])
	installations := testutil.RequireType[[]any](t, response["installations"])
	require.Equal(
		t,
		[]any{
			map[string]any{
				"id":           "456",
				"account":      "octo-org",
				"account_type": "Organization",
				"settings_url": "https://github.com/organizations/octo-org/settings/installations/456",
			},
		},
		installations,
	)
	response = requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t)+"/installations",
		projectAppHTTPJSON(t, map[string]any{"credentials_secret_ref": ref, "page": 2}),
		"",
		http.StatusOK,
		f.headers(),
	)
	require.Empty(t, response["installations"])
	require.NotContains(t, response, "next_page")
	current, err := f.project.Store.Integrations().GetProjectApp(t.Context(), f.project.ProjectUUID, f.app.ID)
	require.NoError(t, err)
	require.Equal(t, f.app, current)
	other := bootstrapPublicHTTPProject(t, f.handler, "github-foreign-secret")
	foreign := createSetupHTTPApp(t, f.handler, other, "github-other", appdefinition.GitHubPR)
	before := f.inspections.Load()
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		other.ProjectPath+"/apps/"+testPublicID(t, publicid.KindProjectApp, foreign.ID)+"/github-setup/installations",
		projectAppHTTPJSON(t, map[string]any{"credentials_secret_ref": ref}),
		"",
		http.StatusNotFound,
		map[string]string{
			"Cookie": httpauth.BrowserSessionHostCookieName + "=" + other.AdminSession + "; " +
				httpauth.CSRFHostCookieName + "=" + other.AdminCSRF,
			"Origin":                "https://omnara.test",
			httpauth.CSRFHeaderName: other.AdminCSRF,
		},
	)
	require.Equal(t, before, f.inspections.Load())
}

func TestGitHubManifestCallbackRechecksRevocationAndPreservesConvertedSecretOnSetupRace(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"revoked", "deleted", "disconnect", "connected before callback", "changes during conversion",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newGitHubManifestFixture(t)
			_, token, _ := f.start(t)
			switch scenario {
			case "revoked":
				pool := integrationPoolForHandler(t, f.handler)
				_, err := pool.Exec(
					t.Context(),
					`UPDATE org_memberships SET role='member' WHERE org_id=$1 AND user_id=$2`,
					f.project.OrgUUID,
					f.project.AdminUserUUID,
				)
				require.NoError(t, err)
				_, err = pool.Exec(t.Context(), `DELETE FROM project_memberships WHERE project_id=$1`, f.project.ProjectUUID)
				require.NoError(t, err)
			case "deleted":
				require.NoError(
					t,
					f.project.Store.Integrations().DeleteProjectApp(t.Context(), f.project.OrgUUID, f.project.ProjectUUID, f.app.ID),
				)
			case "disconnect":
				_, err := f.project.Store.Integrations().
					DisconnectProjectApp(t.Context(), integrationstore.DisconnectProjectAppInput{
						ProjectID: f.project.ProjectUUID, AppID: f.app.ID,
					})
				require.NoError(t, err)
			case "connected before callback":
				ref := createAppSetupHTTPSecret(t, f.handler, f.project, "manual-github-key", map[string]any{
					"kind": "github_app_credentials", "app_id": "123",
					"private_key": f.privateKey, "webhook_secret": githubJourneyWebhookSecret,
				})
				body := map[string]any{
					"expected_setup_revision": f.app.SetupRevision,
					"provider_tenant_id":      "123", "provider_account_ref": "456", "credential_secret_id": ref,
				}
				requestJSONWithHeaders(t, f.handler, http.MethodPost, appSetupPath(t, f.project, f.app),
					projectAppHTTPJSON(t, body), "", http.StatusOK, f.headers())
			case "changes during conversion":
				f.onConvert = func() {
					_, err := f.project.Store.Integrations().
						DisconnectProjectApp(t.Context(), integrationstore.DisconnectProjectAppInput{
							ProjectID: f.project.ProjectUUID, AppID: f.app.ID,
						})
					require.NoError(t, err)
				}
			}
			response := githubManifestCallback(f.handler, token, strings.Repeat("a", 40), f.project.AdminSession)
			if scenario == "revoked" || scenario == "deleted" {
				require.True(t, response.Code >= 400 || strings.Contains(response.Header().Get("Location"), "github_setup_error="))
				require.Zero(t, f.conversions.Load())
				return
			}
			require.Equal(t, http.StatusFound, response.Code, response.Body.String())
			location, err := url.Parse(response.Header().Get("Location"))
			require.NoError(t, err)
			require.Equal(t, "credentials_saved", location.Query().Get("github_setup"))
			require.Equal(t, "app_setup_changed", location.Query().Get("github_setup_error"))
			secretID, err := publicid.Decode(publicid.KindSecret, location.Query().Get("credentials_secret_ref"))
			require.NoError(t, err)
			_, err = f.project.Store.Secrets().
				ReadProjectAvailableSecretPayload(t.Context(), secretstore.ReadProjectAvailableSecretPayloadInput{
					OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
					SecretID: secretID, Kind: secrets.KindGitHubAppCredentials,
				})
			require.NoError(t, err, "one-time conversion credentials must remain recoverable")
			current, err := f.project.Store.Integrations().GetProjectApp(t.Context(), f.project.ProjectUUID, f.app.ID)
			require.NoError(t, err)
			if scenario == "connected before callback" {
				require.Equal(t, integrationstore.ProjectAppStateActive, current.State)
				require.NotEqual(t, secretID, current.CredentialSecretID, "conversion must not replace an active connection")
			} else {
				require.Equal(t, integrationstore.ProjectAppStateDisconnected, current.State)
			}
			require.Equal(t, f.app.SetupRevision+1, current.SetupRevision)
			body := map[string]any{
				"expected_setup_revision": f.app.SetupRevision,
				"provider_tenant_id":      "123", "provider_account_ref": "456",
				"credential_secret_id": location.Query().Get("credentials_secret_ref"),
			}
			requestJSONWithHeaders(t, f.handler, http.MethodPost, appSetupPath(t, f.project, f.app),
				projectAppHTTPJSON(t, body), "", http.StatusConflict, f.headers())
			body["expected_setup_revision"] = current.SetupRevision
			body["provider_account_ref"] = "999"
			requestJSONWithHeaders(t, f.handler, http.MethodPost, appSetupPath(t, f.project, f.app),
				projectAppHTTPJSON(t, body), "", http.StatusBadRequest, f.headers())
		})
	}
}

func TestGitHubGuidedConnectionUsesExistingVerifiedManualSetup(t *testing.T) {
	t.Parallel()
	f := newGitHubManifestFixture(t)
	ref := f.saveCredentials(t)
	body := map[string]any{
		"expected_setup_revision": f.app.SetupRevision,
		"provider_tenant_id":      "123",
		"provider_account_ref":    "999",
		"credential_secret_id":    ref,
	}
	// Browser-return installation IDs are hints; the ordinary setup route verifies
	// App/installation membership with the saved key before activation.
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		appSetupPath(t, f.project, f.app),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		f.headers(),
	)
	body["provider_account_ref"] = "456"
	connected := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		appSetupPath(t, f.project, f.app),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		f.headers(),
	)
	require.Equal(t, "active", connected["state"])
	require.Equal(t, "Verified Helper", connected["provider_agent_display_name"])
	// A lost response follows ordinary revision conflict/refetch semantics.
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		appSetupPath(t, f.project, f.app),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusConflict,
		f.headers(),
	)
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path(t),
		`{"expected_setup_revision":2}`,
		"",
		http.StatusBadRequest,
		f.headers(),
	)
	// Existing PAT/manual reconnect remains available and explicit display names win.
	body["expected_setup_revision"] = float64(2)
	body["provider_agent_display_name"] = "Customer label"
	reconnected := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		appSetupPath(t, f.project, f.app),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(f.project.AdminToken),
	)
	require.Equal(t, "Customer label", reconnected["provider_agent_display_name"])
	// Omitting the field on a later reconnect preserves the customer's label.
	delete(body, "provider_agent_display_name")
	body["expected_setup_revision"] = reconnected["setup_revision"]
	preserved := requestJSONWithHeaders(t, f.handler, http.MethodPost, appSetupPath(t, f.project, f.app),
		projectAppHTTPJSON(t, body), "", http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, "Customer label", preserved["provider_agent_display_name"])
	current, err := f.project.Store.Integrations().GetProjectApp(t.Context(), f.project.ProjectUUID, f.app.ID)
	require.NoError(t, err)
	require.Equal(t, "Customer label", current.ProviderAgentDisplayName)
}

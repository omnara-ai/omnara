//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

type projectSlackOAuthFixture struct {
	pool      *pgxpool.Pool
	handler   http.Handler
	project   publicHTTPProject
	exchanges atomic.Int32
	manifests atomic.Int32
	icons     atomic.Int32
}

func newProjectSlackOAuthFixture(t *testing.T, exchangeHook func(*http.Request)) *projectSlackOAuthFixture {
	t.Helper()
	f := &projectSlackOAuthFixture{pool: openIntegrationDB(t, t.Context())}
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth.v2.access":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse OAuth form: %v", err)
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			f.exchanges.Add(1)
			if exchangeHook != nil {
				exchangeHook(r)
			}
			writeJSON(w, http.StatusOK, slackOAuthTestResponse("xoxb-"+r.PostForm.Get("code")))
		case "/users.info":
			writeSlackLookupTestResponse(t, w, r)
		case "/apps.manifest.create":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse manifest form: %v", err)
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			if r.PostForm.Get("token") != "configuration-token" ||
				!strings.Contains(r.PostForm.Get("manifest"), "Standalone bot") {
				t.Errorf("manifest must use the supplied name and configuration token")
			}
			f.manifests.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "app_id": "A123",
				"credentials": map[string]string{
					"client_id": "client-123", "client_secret": "client-secret", "signing_secret": "signing-secret",
				},
			})
		case "/apps.icon.set":
			icon, err := readSlackIconSetRequest(r)
			if err != nil {
				t.Errorf("read icon: %v", err)
				http.Error(w, "invalid icon", http.StatusBadRequest)
				return
			}
			if icon.filename != "custom.png" || icon.appID != "A123" ||
				icon.token != "configuration-token" || string(icon.content) != string(slack.DefaultAppIcon().Content) {
				t.Errorf("icon must use the supplied image and created Slack app")
			}
			f.icons.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			t.Errorf("unexpected Slack path %s", r.URL.Path)
			http.Error(w, "unexpected provider request", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(slackServer.Close)
	f.handler = newIntegrationServer(f.pool, WithPublicURL("https://omnara.test"), WithSlackOAuth(SlackOAuthConfig{
		AuthorizeURL: "http://slack.test/oauth/v2/authorize", AccessURL: slackServer.URL + "/oauth.v2.access",
		APIURL: slackServer.URL, HTTPClient: slackServer.Client(),
	}))
	f.project = bootstrapPublicHTTPProject(t, f.handler, "project-slack")
	return f
}

func (f *projectSlackOAuthFixture) start(t *testing.T, manifest bool) (string, integrationOAuthState) {
	t.Helper()
	path, body := projectSlackSetupRequest(t, f.project, manifest)
	setup := requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusCreated,
		authHeaders(f.project.AdminToken))
	oauthURL, err := url.Parse(testutil.RequireType[string](t, setup["oauth_url"]))
	require.NoError(t, err)
	require.Equal(t, "client-123", oauthURL.Query().Get("client_id"))
	token := oauthURL.Query().Get("state")
	decoder := &Server{secretKeyWrapper: integrationKeyWrapper()}
	state, err := decoder.decodeIntegrationOAuthState(t.Context(), token)
	require.NoError(t, err)
	require.True(t, state.ConnectionOnly)
	require.Equal(t, uuid.Nil, state.AgentProfileID)
	require.Equal(t, f.project.ProjectUUID, state.ProjectID)
	require.Equal(t, f.project.AdminUserUUID, state.InstalledByUserID)
	require.Equal(t, "client-secret", state.ClientSecret)
	require.Equal(t, "signing-secret", state.SigningSecret)
	return token, state
}

func projectSlackSetupRequest(t *testing.T, project publicHTTPProject, manifest bool) (string, string) {
	t.Helper()
	path := project.ProjectPath + "/integration-oauth/setup"
	body := map[string]any{
		"client_id": "client-123", "client_secret": "client-secret", "signing_secret": "signing-secret",
	}
	if manifest {
		path = project.ProjectPath + "/slack-setup"
		body = map[string]any{
			"app_name": "Standalone bot", "app_configuration_token": "configuration-token",
			"icon": map[string]string{
				"filename": "custom.png", "data_base64": base64.StdEncoding.EncodeToString(slack.DefaultAppIcon().Content),
			},
		}
	}
	body["return_to"] = "/projects/" + project.ProjectID + "/apps/new/slack?draft=keep#settings"
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return path, string(encoded)
}

func projectSlackCallback(handler http.Handler, token, code, session string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+integrationOAuthCallbackPath+
		"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(token), nil)
	if session != "" {
		req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: session})
	}
	return performRequest(handler, req)
}

func (f *projectSlackOAuthFixture) complete(
	t *testing.T,
	token, code string,
) integrationstore.IntegrationConnectionRecord {
	t.Helper()
	rec := projectSlackCallback(f.handler, token, code, f.project.AdminSession)
	require.Equal(t, http.StatusFound, rec.Code, "%s", rec.Body.String())
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https", location.Scheme)
	require.Equal(t, "omnara.test", location.Host)
	require.Equal(t, "/projects/"+f.project.ProjectID+"/apps/new/slack", location.Path)
	require.Equal(t, "settings", location.Fragment)
	id, err := publicid.Decode(publicid.KindIntegrationConnection, location.Query().Get("integration_connection"))
	require.NoError(t, err)
	require.Equal(t, url.Values{
		"draft": {"keep"}, "integration_oauth": {"success"},
		"integration_connection": {location.Query().Get("integration_connection")},
	}, location.Query())
	connection, err := f.project.Store.Integrations().GetIntegrationConnection(t.Context(), f.project.ProjectUUID, id)
	require.NoError(t, err)
	return connection
}

func (f *projectSlackOAuthFixture) assertNoApps(t *testing.T) {
	t.Helper()
	page, err := f.project.Store.Integrations().ListProjectApps(t.Context(), integrationstore.ListProjectAppsInput{
		ProjectID: f.project.ProjectUUID, Limit: 100,
	})
	require.NoError(t, err)
	require.Empty(t, page.Apps)
}

func TestProjectSlackOAuthSetupWithoutProfiles(t *testing.T) {
	t.Parallel()
	for _, manifest := range []bool{false, true} {
		name := "existing Slack app"
		if manifest {
			name = "new Slack app with custom icon"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newProjectSlackOAuthFixture(t, nil)
			var profiles int
			err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_profiles WHERE project_id=$1`,
				f.project.ProjectUUID).Scan(&profiles)
			require.NoError(t, err)
			require.Zero(t, profiles)
			// App capacity and profile/model validity are irrelevant to connection-only OAuth.
			_, err = f.pool.Exec(t.Context(),
				`INSERT INTO org_resource_limit_overrides(org_id,max_active_project_apps_per_project) VALUES($1,0)`,
				f.project.OrgUUID)
			require.NoError(t, err)
			token, state := f.start(t, manifest)
			first := f.complete(t, token, "first")
			require.Equal(t, state.FlowID, first.LastOAuthFlowID)
			require.Equal(t, integrationstore.IntegrationConnectionStateActive, first.State)
			if manifest {
				require.Equal(t, "Standalone bot", first.ProviderAgentDisplayName)
				require.EqualValues(t, 1, f.manifests.Load())
				require.EqualValues(t, 1, f.icons.Load())
			}
			f.assertNoApps(t)
			require.Equal(t, http.StatusUnauthorized,
				projectSlackCallback(f.handler, token, "replay", f.project.AdminSession).Code)
			require.EqualValues(t, 1, f.exchanges.Load(), "current replay must fail before provider I/O")
			newToken, _ := f.start(t, false)
			second := f.complete(t, newToken, "second")
			require.Equal(t, first.ID, second.ID)
			require.NotEqual(t, first.CredentialSecretID, second.CredentialSecretID)
			payload, err := f.project.Store.Secrets().GetProjectOwnedSecretPayload(t.Context(),
				f.project.OrgUUID, f.project.ProjectUUID, second.CredentialSecretID)
			require.NoError(t, err)
			credentials, err := slack.AppCredentialsFromPayload(payload)
			require.NoError(t, err)
			require.Equal(t, "xoxb-second", credentials.BotToken)
			before := countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project)
			require.Equal(t, http.StatusUnauthorized,
				projectSlackCallback(f.handler, token, "superseded", f.project.AdminSession).Code)
			require.Equal(t, before, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project),
				"a superseded flow must clean up its losing credential")
			f.assertNoApps(t)
		})
	}
}

func TestProjectSlackOAuthFailureRollbackAndOwnership(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	token, state := f.start(t, false)
	_, err := f.pool.Exec(t.Context(),
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_integration_connections_per_project) VALUES($1,0)`,
		f.project.OrgUUID)
	require.NoError(t, err)
	rec := projectSlackCallback(f.handler, token, "no-capacity", f.project.AdminSession)
	require.Equal(t, http.StatusFound, rec.Code)
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "setup_save_failed", location.Query().Get("integration_oauth_error"))
	require.Empty(t, location.Query().Get("integration_connection"))
	require.Zero(t, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
	consumed, err := f.project.Store.Integrations().IntegrationOAuthFlowConsumed(t.Context(), state.FlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	_, err = f.project.Store.Integrations().GetIntegrationConnectionByProviderAccount(t.Context(), "slack", "T123", "A123")
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	f.assertNoApps(t)
	_, err = f.pool.Exec(t.Context(),
		`UPDATE org_resource_limit_overrides SET max_active_integration_connections_per_project=1 WHERE org_id=$1`,
		f.project.OrgUUID)
	require.NoError(t, err)
	first := f.complete(t, token, "retry")

	// A second project cannot claim the same globally unique Slack account.
	other := bootstrapPublicHTTPProject(t, f.handler, "other-slack")
	path, body := projectSlackSetupRequest(t, other, false)
	setup := requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusCreated,
		authHeaders(other.AdminToken))
	oauthURL, err := url.Parse(testutil.RequireType[string](t, setup["oauth_url"]))
	require.NoError(t, err)
	rec = projectSlackCallback(f.handler, oauthURL.Query().Get("state"), "other-project", other.AdminSession)
	require.Equal(t, http.StatusFound, rec.Code)
	location, err = url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "setup_save_failed", location.Query().Get("integration_oauth_error"))
	require.Empty(t, location.Query().Get("integration_connection"))
	require.Zero(t, countSlackOAuthCredentialSecrets(t, t.Context(), other.Store, other))
	current, err := f.project.Store.Integrations().GetIntegrationConnection(t.Context(), f.project.ProjectUUID, first.ID)
	require.NoError(t, err)
	require.Equal(t, first, current)

	ref, err := publicid.Encode(publicid.KindIntegrationConnection, first.ID)
	require.NoError(t, err)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, f.project.ProjectPath+"/integration-connections/"+ref,
		"", "", http.StatusNoContent, authHeaders(f.project.AdminToken))
	before := countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project)
	require.Equal(t, http.StatusUnauthorized,
		projectSlackCallback(f.handler, token, "deleted-replay", f.project.AdminSession).Code)
	require.Equal(t, before, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
}

func TestProjectSlackOAuthReconnectLeavesExistingAppsUnchanged(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	profile := createSlackReadyHTTPProfile(t, f.handler, f.project, "legacy-profile", f.project.AdminToken)
	profileID := testutil.RequireType[string](t, profile["id"])
	first := completeSlackOAuthInstall(t, f.handler, f.project, profileID, f.project.AdminSession, "legacy")
	app := assertSlackOAuthDefaultProjectApp(t, f.handler, f.project, first, profileID, 1)
	settings := testutil.RequireType[map[string]any](t, app["settings"])
	delete(settings, "launcher")
	body, err := json.Marshal(map[string]any{"name": "Configured app", "settings": settings, "enabled": false})
	require.NoError(t, err)
	updated := requestJSONWithHeaders(t, f.handler, http.MethodPut,
		f.project.ProjectPath+"/apps/"+testutil.RequireType[string](t, app["id"]), string(body), "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	token, _ := f.start(t, false)
	second := f.complete(t, token, "project-reconnect")
	require.Equal(t, first.ID, second.ID)
	require.NotEqual(t, first.CredentialSecretID, second.CredentialSecretID)
	page := requestJSONWithHeaders(t, f.handler, http.MethodGet, f.project.ProjectPath+"/apps", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, []any{updated}, page["data"], "connection-only reconnect must not change app revisions or settings")
}

func TestProjectSlackOAuthAuthorization(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	other := bootstrapPublicHTTPProject(t, f.handler, "other-user")
	browserHeaders := func() map[string]string {
		return map[string]string{
			"Cookie": httpauth.BrowserSessionHostCookieName + "=" + f.project.AdminSession + "; " +
				httpauth.CSRFHostCookieName + "=" + f.project.AdminCSRF,
			"Origin": "https://omnara.test", httpauth.CSRFHeaderName: f.project.AdminCSRF,
		}
	}
	key := requestJSONWithHeaders(t, f.handler, http.MethodPost, "/api/v1/orgs/"+f.project.OrgID+"/api-keys",
		`{"name":"setup-key","org_role":"admin"}`, "", http.StatusCreated, browserHeaders())
	keyToken := testutil.RequireType[string](t, key["token"])
	for _, manifest := range []bool{false, true} {
		path, body := projectSlackSetupRequest(t, f.project, manifest)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusUnauthorized, nil)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusForbidden, authHeaders(keyToken))
		requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusNotFound,
			authHeaders(other.AdminToken))
		// Organization and project IDs cannot be mixed even by an authenticated user.
		requestJSONWithHeaders(t, f.handler, http.MethodPost,
			strings.Replace(path, f.project.OrgID, other.OrgID, 1), body, "", http.StatusNotFound,
			authHeaders(other.AdminToken))
		headers := browserHeaders()
		delete(headers, httpauth.CSRFHeaderName)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusForbidden, headers)
	}
	require.Zero(t, f.manifests.Load(), "unauthorized setup must not call Slack")
	path, body := projectSlackSetupRequest(t, f.project, false)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusCreated, browserHeaders())
	token, _ := f.start(t, false)
	require.Equal(t, http.StatusUnauthorized, projectSlackCallback(f.handler, token, "anonymous", "").Code)
	require.Equal(t, http.StatusForbidden,
		projectSlackCallback(f.handler, token, "wrong-user", other.AdminSession).Code)
	req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+integrationOAuthCallbackPath+
		"?code=bearer&state="+url.QueryEscape(token), nil)
	req.Header.Set("Authorization", "Bearer "+f.project.AdminToken)
	require.Equal(t, http.StatusUnauthorized, performRequest(f.handler, req).Code)

	// Authorization is rechecked at callback time, after the flow has been sealed.
	_, err := f.pool.Exec(t.Context(), `UPDATE org_memberships SET role='member' WHERE org_id=$1 AND user_id=$2`,
		f.project.OrgUUID, f.project.AdminUserUUID)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `DELETE FROM project_memberships WHERE project_id=$1`, f.project.ProjectUUID)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden,
		projectSlackCallback(f.handler, token, "revoked", f.project.AdminSession).Code)
	require.Zero(t, f.exchanges.Load(), "rejected callbacks must fail before exchanging provider credentials")
	require.Zero(t, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
	f.assertNoApps(t)
}

func TestProjectSlackOAuthConcurrentRedemptionCleansLosingSecret(t *testing.T) {
	t.Parallel()
	exchangeCtx, release := context.WithCancel(t.Context())
	defer release()
	arrived := make(chan struct{}, 2)
	f := newProjectSlackOAuthFixture(t, func(_ *http.Request) {
		arrived <- struct{}{}
		<-exchangeCtx.Done()
	})
	token, _ := f.start(t, false)
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { results <- projectSlackCallback(f.handler, token, "racing-code", f.project.AdminSession) }()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("callbacks did not both reach the provider exchange")
		}
	}
	release()
	statuses := []int{(<-results).Code, (<-results).Code}
	require.ElementsMatch(t, []int{http.StatusFound, http.StatusUnauthorized}, statuses)
	require.Equal(t, 1, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
	f.assertNoApps(t)
}

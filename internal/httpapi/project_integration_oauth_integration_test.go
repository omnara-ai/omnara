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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type projectSlackOAuthFixture struct {
	pool      *pgxpool.Pool
	handler   http.Handler
	project   publicHTTPProject
	app       integrationstore.ProjectAppRecord
	exchanges atomic.Int32
	manifests atomic.Int32
	icons     atomic.Int32
}

func newProjectSlackOAuthFixture(
	t *testing.T,
	exchangeHook func(*http.Request),
) *projectSlackOAuthFixture {
	t.Helper()
	f := &projectSlackOAuthFixture{pool: openIntegrationDB(t, t.Context())}
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse OAuth form: %v", err)
					http.Error(w, "invalid form", http.StatusBadRequest)
					return
				}
				assert.Equal(t, "client-123", r.PostForm.Get("client_id"))
				assert.Equal(t, "client-secret", r.PostForm.Get("client_secret"))
				assert.Equal(
					t,
					"https://omnara.test"+integrationOAuthCallbackPath,
					r.PostForm.Get("redirect_uri"),
				)
				f.exchanges.Add(1)
				if exchangeHook != nil {
					exchangeHook(r)
				}
				response := slackOAuthTestResponse("xoxb-" + r.PostForm.Get("code"))
				if r.PostForm.Get("code") == "foreign-app" {
					response["app_id"] = "A_FOREIGN"
				}
				if r.PostForm.Get("code") == "foreign-workspace" {
					response["team"] = map[string]string{"id": "T_FOREIGN", "name": "Other"}
				}
				if r.PostForm.Get("code") == "foreign-bot" {
					response["bot_user_id"] = "U_FOREIGN"
				}
				if r.PostForm.Get("code") == "missing-scope" {
					response["scope"] = "chat:write"
				}
				if r.PostForm.Get("code") == "exchange-failure" {
					response = map[string]any{"ok": false, "error": "invalid_code"}
				}
				writeJSON(w, http.StatusOK, response)
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
				var manifest slack.AppManifest
				if assert.NoError(
					t,
					json.Unmarshal([]byte(r.PostForm.Get("manifest")), &manifest),
				) {
					assert.Equal(
						t,
						[]string{"https://omnara.test" + integrationOAuthCallbackPath},
						manifest.OAuthConfig.RedirectURLs,
					)
					assert.Equal(
						t,
						"https://omnara.test"+integrationEventsPath,
						manifest.Settings.EventSubscriptions.RequestURL,
					)
					assert.Equal(
						t,
						"https://omnara.test"+integrationActionsPath,
						manifest.Settings.Interactivity.RequestURL,
					)
					assert.ElementsMatch(
						t,
						slack.RequiredBotScopes,
						manifest.OAuthConfig.Scopes.Bot,
					)
				}
				f.manifests.Add(1)
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": true, "app_id": "A123",
					"credentials": map[string]string{
						"client_id":      "client-123",
						"client_secret":  "client-secret",
						"signing_secret": "signing-secret",
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
		}),
	)
	t.Cleanup(slackServer.Close)
	f.handler = newIntegrationServer(
		f.pool,
		WithPublicURL("https://omnara.test"),
		WithSlackOAuth(SlackOAuthConfig{
			AuthorizeURL: "http://slack.test/oauth/v2/authorize",
			AccessURL:    slackServer.URL + "/oauth.v2.access",
			APIURL:       slackServer.URL,
			HTTPClient:   slackServer.Client(),
		}),
	)
	f.project = bootstrapPublicHTTPProject(t, f.handler, "project-slack")
	f.app = createSetupHTTPApp(t, f.handler, f.project, "slack", appdefinition.SlackThread)
	return f
}

func (f *projectSlackOAuthFixture) start(
	t *testing.T,
	manifest bool,
) (string, integrationOAuthState) {
	t.Helper()
	path, body := projectSlackSetupRequest(
		t,
		f.project,
		testPublicID(t, publicid.KindProjectApp, f.app.ID),
		manifest,
	)
	setup := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusCreated,
		authHeaders(f.project.AdminToken),
	)
	oauthURL, err := url.Parse(testutil.RequireType[string](t, setup["oauth_url"]))
	require.NoError(t, err)
	require.Equal(t, "client-123", oauthURL.Query().Get("client_id"))
	require.Equal(t, "https://omnara.test"+integrationOAuthCallbackPath, setup["redirect_uri"])
	require.Equal(t, setup["redirect_uri"], oauthURL.Query().Get("redirect_uri"))
	require.Equal(t, "https://omnara.test"+integrationEventsPath, setup["events_url"])
	require.Equal(t, "https://omnara.test"+integrationActionsPath, setup["actions_url"])
	require.ElementsMatch(
		t,
		slack.RequiredBotScopes,
		strings.Split(oauthURL.Query().Get("scope"), ","),
	)
	require.NotContains(t, projectAppHTTPJSON(t, setup), "client-secret")
	require.NotContains(t, projectAppHTTPJSON(t, setup), "signing-secret")
	token := oauthURL.Query().Get("state")
	decoder := &Server{secretKeyWrapper: integrationKeyWrapper()}
	state, err := decoder.decodeIntegrationOAuthState(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, f.app.ID, state.AppID)
	require.Equal(t, f.app.SetupRevision, state.SetupRevision)
	require.EqualValues(t, state.SetupRevision, setup["setup_revision"])
	require.Equal(t, testPublicID(t, publicid.KindProjectApp, f.app.ID), setup["app_id"])
	require.Equal(t, f.project.ProjectUUID, state.ProjectID)
	require.Equal(t, f.project.AdminUserUUID, state.InstalledByUserID)
	require.Equal(t, "client-secret", state.ClientSecret)
	require.Equal(t, "signing-secret", state.SigningSecret)
	return token, state
}

func projectSlackSetupRequest(
	t *testing.T,
	project publicHTTPProject,
	appRef string,
	manifest bool,
) (string, string) {
	t.Helper()
	path := project.ProjectPath + "/apps/" + appRef + "/oauth/setup"
	body := map[string]any{
		"client_id":      "client-123",
		"client_secret":  "client-secret",
		"signing_secret": "signing-secret",
	}
	if manifest {
		path = project.ProjectPath + "/apps/" + appRef + "/slack-setup"
		body = map[string]any{
			"app_name": "Standalone bot", "app_configuration_token": "configuration-token",
			"icon": map[string]string{
				"filename":    "custom.png",
				"data_base64": base64.StdEncoding.EncodeToString(slack.DefaultAppIcon().Content),
			},
		}
	}
	body["return_to"] = "/projects/" + project.ProjectID + "/apps/new/slack?draft=keep#settings"
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return path, string(encoded)
}

func projectSlackCallback(
	handler http.Handler,
	token, code, session string,
) *httptest.ResponseRecorder {
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
) integrationstore.ProjectAppRecord {
	t.Helper()
	rec := projectSlackCallback(f.handler, token, code, f.project.AdminSession)
	require.Equal(t, http.StatusFound, rec.Code, "%s", rec.Body.String())
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https", location.Scheme)
	require.Equal(t, "omnara.test", location.Host)
	require.Equal(t, "/projects/"+f.project.ProjectID+"/apps/new/slack", location.Path)
	require.Equal(t, "settings", location.Fragment)
	id, err := publicid.Decode(publicid.KindProjectApp, location.Query().Get("app_id"))
	require.NoError(t, err)
	require.Equal(t, url.Values{
		"draft": {"keep"}, "integration_oauth": {"success"},
		"app_id": {location.Query().Get("app_id")},
	}, location.Query())
	app, err := f.project.Store.Integrations().
		GetProjectApp(t.Context(), f.project.ProjectUUID, id)
	require.NoError(t, err)
	f.app = app
	detail := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodGet,
		f.project.ProjectPath+"/apps/"+location.Query().
			Get("app_id"),
		"",
		"",
		http.StatusOK,
		authHeaders(f.project.AdminToken),
	)
	require.Equal(
		t,
		testPublicID(t, publicid.KindIntegrationOAuthFlow, app.LastOAuthFlowID),
		detail["last_oauth_flow_id"],
	)
	return app
}

func (f *projectSlackOAuthFixture) assertFailure(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	require.Equal(t, http.StatusFound, rec.Code, "%s", rec.Body.String())
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https", location.Scheme)
	require.Equal(t, "omnara.test", location.Host)
	require.Equal(t, "/projects/"+f.project.ProjectID+"/apps/new/slack", location.Path)
	require.Equal(t, "settings", location.Fragment)
	require.Equal(t, url.Values{"draft": {"keep"}, "integration_oauth_error": {code}}, location.Query())
}

func (f *projectSlackOAuthFixture) assertOneApp(t *testing.T) {
	t.Helper()
	page, err := f.project.Store.Integrations().
		ListProjectApps(t.Context(), integrationstore.ListProjectAppsInput{
			ProjectID: f.project.ProjectUUID, Limit: 100,
		})
	require.NoError(t, err)
	require.Len(t, page.Apps, 1)
	require.Equal(t, f.app.ID, page.Apps[0].ID)
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
				f.project.ProjectUUID).
				Scan(&profiles)
			require.NoError(t, err)
			require.Zero(t, profiles)
			_, err = f.pool.Exec(
				t.Context(),
				`INSERT INTO org_resource_limit_overrides(org_id,max_active_project_apps_per_project) VALUES($1,0)`,
				f.project.OrgUUID,
			)
			require.NoError(t, err)
			token, state := f.start(t, manifest)
			first := f.complete(t, token, "first")
			require.Equal(t, state.FlowID, first.LastOAuthFlowID)
			require.Equal(t, integrationstore.ProjectAppStateActive, first.State)
			if manifest {
				require.Equal(t, "Standalone bot", first.ProviderAgentDisplayName)
				require.EqualValues(t, 1, f.manifests.Load())
				require.EqualValues(t, 1, f.icons.Load())
			}
			f.assertOneApp(t)
			f.assertFailure(t, projectSlackCallback(f.handler, token, "replay", f.project.AdminSession),
				"app_setup_changed")
			require.EqualValues(
				t,
				1,
				f.exchanges.Load(),
				"current replay must fail before provider I/O",
			)
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
			f.assertFailure(t, projectSlackCallback(f.handler, token, "superseded", f.project.AdminSession),
				"app_setup_changed")
			require.Equal(
				t,
				before,
				countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project),
				"a superseded flow must clean up its losing credential",
			)
			f.assertOneApp(t)
		})
	}
}

func TestProjectSlackOAuthStaleDeletionAndNameReuse(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	older, _ := f.start(t, false)
	newer, _ := f.start(t, false)
	first := f.complete(t, newer, "newer")
	f.assertFailure(t, projectSlackCallback(f.handler, older, "reversed", f.project.AdminSession),
		"app_setup_changed")
	require.EqualValues(t, 1, f.exchanges.Load())
	token, _ := f.start(t, false)
	require.NoError(
		t,
		f.project.Store.Integrations().
			DeleteProjectApp(t.Context(), f.project.OrgUUID, f.project.ProjectUUID, first.ID),
	)
	replacement := createSetupHTTPApp(t, f.handler, f.project, "slack", appdefinition.SlackThread)
	require.NotEqual(t, first.ID, replacement.ID)
	f.assertFailure(t, projectSlackCallback(f.handler, token, "deleted", f.project.AdminSession),
		"app_deleted")
	require.EqualValues(
		t,
		1,
		f.exchanges.Load(),
		"a reused name cannot redirect a pinned OAuth flow",
	)
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, replacement.State)
}

func TestProjectSlackOAuthDisconnectInvalidatesPendingSetup(t *testing.T) {
	t.Parallel()
	for _, active := range []bool{false, true} {
		name := "disconnected app"
		if active {
			name = "active app"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newProjectSlackOAuthFixture(t, nil)
			if active {
				token, _ := f.start(t, false)
				f.complete(t, token, "initial")
			}
			token, _ := f.start(t, false)
			before := f.exchanges.Load()
			path := strings.TrimSuffix(appSetupPath(t, f.project, f.app), "/setup") + "/disconnect"
			requestJSONWithHeaders(
				t,
				f.handler,
				http.MethodPost,
				path,
				"",
				"",
				http.StatusOK,
				authHeaders(f.project.AdminToken),
			)
			f.assertFailure(t,
				projectSlackCallback(f.handler, token, "after-disconnect", f.project.AdminSession),
				"app_setup_changed")
			require.Equal(
				t,
				before,
				f.exchanges.Load(),
				"explicit disconnect invalidates setup before provider I/O",
			)
			current, err := f.project.Store.Integrations().
				GetProjectApp(t.Context(), f.project.ProjectUUID, f.app.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.ProjectAppStateDisconnected, current.State)
		})
	}
}

func TestProjectSlackOAuthCommitRaceCleansCredential(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"disconnect", "delete"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			var mutate func()
			f := newProjectSlackOAuthFixture(t, func(*http.Request) {
				if mutate != nil {
					mutate()
				}
			})
			firstToken, _ := f.start(t, false)
			original := f.complete(t, firstToken, "original")
			token, state := f.start(t, false)
			mutate = func() {
				if change == "delete" {
					assert.NoError(
						t,
						f.project.Store.Integrations().
							DeleteProjectApp(t.Context(), f.project.OrgUUID, f.project.ProjectUUID, f.app.ID),
					)
				} else {
					changed, err := f.project.Store.Integrations().
						DisconnectProjectApp(t.Context(), integrationstore.DisconnectProjectAppInput{
							ProjectID: f.project.ProjectUUID,
							AppID:     f.app.ID,
						})
					assert.NoError(t, err)
					assert.True(t, changed)
				}
			}
			rec := projectSlackCallback(
				f.handler,
				token,
				"stale-during-exchange",
				f.project.AdminSession,
			)
			if change == "delete" {
				f.assertFailure(t, rec, "app_deleted")
			} else {
				f.assertFailure(t, rec, "app_setup_changed")
				current, err := f.project.Store.Integrations().
					GetProjectApp(t.Context(), f.project.ProjectUUID, f.app.ID)
				require.NoError(t, err)
				require.Equal(t, integrationstore.ProjectAppStateDisconnected, current.State)
				require.Equal(t, original.CredentialSecretID, current.CredentialSecretID)
			}
			require.Equal(
				t,
				1,
				countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project),
				"only the rejected callback's newly created credential is removed",
			)
			consumed, err := f.project.Store.Integrations().
				IntegrationOAuthFlowConsumed(t.Context(), state.FlowID)
			require.NoError(t, err)
			require.False(t, consumed)
		})
	}
}

func TestProjectSlackOAuthIndependentAppsAndReconnect(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	token, _ := f.start(t, false)
	first := f.complete(t, token, "first")
	other := bootstrapPublicHTTPProject(t, f.handler, "other-slack")
	otherApp := createSetupHTTPApp(t, f.handler, other, "slack", appdefinition.SlackThread)
	otherRef := testPublicID(t, publicid.KindProjectApp, otherApp.ID)
	path, body := projectSlackSetupRequest(t, other, otherRef, false)
	setup := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusCreated,
		authHeaders(other.AdminToken),
	)
	oauthURL, err := url.Parse(testutil.RequireType[string](t, setup["oauth_url"]))
	require.NoError(t, err)
	response := projectSlackCallback(
		f.handler,
		oauthURL.Query().Get("state"),
		"second-project",
		other.AdminSession,
	)
	require.Equal(t, http.StatusFound, response.Code)
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, otherRef, location.Query().Get("app_id"))
	otherApp, err = other.Store.Integrations().
		GetProjectApp(t.Context(), other.ProjectUUID, otherApp.ID)
	require.NoError(t, err)
	require.Equal(t, first.ProviderTenantID, otherApp.ProviderTenantID)
	require.Equal(t, first.ProviderAccountRef, otherApp.ProviderAccountRef)
	require.NotEqual(t, first.CredentialSecretID, otherApp.CredentialSecretID)
	f.assertFailure(t, projectSlackCallback(f.handler, token, "replay", f.project.AdminSession),
		"app_setup_changed")
	newToken, _ := f.start(t, false)
	second := f.complete(t, newToken, "reconnect")
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, first.Name, second.Name)
	require.Equal(t, first.Settings, second.Settings)
	require.Equal(t, first.SetupRevision+1, second.SetupRevision)
	require.NotEqual(t, first.CredentialSecretID, second.CredentialSecretID)
	unchanged, err := other.Store.Integrations().
		GetProjectApp(t.Context(), other.ProjectUUID, otherApp.ID)
	require.NoError(t, err)
	require.Equal(t, otherApp, unchanged)
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
	key := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		"/api/v1/orgs/"+f.project.OrgID+"/api-keys",
		`{"name":"setup-key","org_role":"admin"}`,
		"",
		http.StatusCreated,
		browserHeaders(),
	)
	keyToken := testutil.RequireType[string](t, key["token"])
	for _, manifest := range []bool{false, true} {
		path, body := projectSlackSetupRequest(
			t,
			f.project,
			testPublicID(t, publicid.KindProjectApp, f.app.ID),
			manifest,
		)
		requestJSONWithHeaders(
			t,
			f.handler,
			http.MethodPost,
			path,
			body,
			"",
			http.StatusUnauthorized,
			nil,
		)
		requestJSONWithHeaders(
			t,
			f.handler,
			http.MethodPost,
			path,
			body,
			"",
			http.StatusForbidden,
			authHeaders(keyToken),
		)
		requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusNotFound,
			authHeaders(other.AdminToken))
		requestJSONWithHeaders(t, f.handler, http.MethodPost,
			strings.Replace(path, f.project.OrgID, other.OrgID, 1), body, "", http.StatusNotFound,
			authHeaders(other.AdminToken))
		headers := browserHeaders()
		delete(headers, httpauth.CSRFHeaderName)
		requestJSONWithHeaders(
			t,
			f.handler,
			http.MethodPost,
			path,
			body,
			"",
			http.StatusForbidden,
			headers,
		)
	}
	require.Zero(t, f.manifests.Load(), "unauthorized setup must not call Slack")
	path, body := projectSlackSetupRequest(
		t,
		f.project,
		testPublicID(t, publicid.KindProjectApp, f.app.ID),
		false,
	)
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusCreated,
		browserHeaders(),
	)
	token, _ := f.start(t, false)
	require.Equal(
		t,
		http.StatusUnauthorized,
		projectSlackCallback(f.handler, token, "anonymous", "").Code,
	)
	require.Equal(t, http.StatusForbidden,
		projectSlackCallback(f.handler, token, "wrong-user", other.AdminSession).Code)
	req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+integrationOAuthCallbackPath+
		"?code=bearer&state="+url.QueryEscape(token), nil)
	req.Header.Set("Authorization", "Bearer "+f.project.AdminToken)
	require.Equal(t, http.StatusUnauthorized, performRequest(f.handler, req).Code)

	_, err := f.pool.Exec(
		t.Context(),
		`UPDATE org_memberships SET role='member' WHERE org_id=$1 AND user_id=$2`,
		f.project.OrgUUID,
		f.project.AdminUserUUID,
	)
	require.NoError(t, err)
	_, err = f.pool.Exec(
		t.Context(),
		`DELETE FROM project_memberships WHERE project_id=$1`,
		f.project.ProjectUUID,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden,
		projectSlackCallback(f.handler, token, "revoked", f.project.AdminSession).Code)
	require.Zero(
		t,
		f.exchanges.Load(),
		"rejected callbacks must fail before exchanging provider credentials",
	)
	require.Zero(t, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
	f.assertOneApp(t)
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
	var outcomes []string
	for range 2 {
		rec := <-results
		require.Equal(t, http.StatusFound, rec.Code)
		location, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		if outcome := location.Query().Get("integration_oauth_error"); outcome != "" {
			f.assertFailure(t, rec, "app_setup_changed")
			outcomes = append(outcomes, outcome)
		} else {
			require.Equal(t, testPublicID(t, publicid.KindProjectApp, f.app.ID), location.Query().Get("app_id"))
			outcomes = append(outcomes, location.Query().Get("integration_oauth"))
		}
	}
	require.ElementsMatch(t, []string{"success", "app_setup_changed"}, outcomes)
	require.Equal(
		t,
		1,
		countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project),
	)
	f.assertOneApp(t)
}

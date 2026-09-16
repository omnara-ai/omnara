//go:build integration

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/stretchr/testify/require"
)

func TestIntegrationConnectionGitHubPublicJourney(t *testing.T) {
	t.Parallel()
	f := newConnectionSetupFixture(t)
	profile := createSlackReadyHTTPProfile(t, f.handler, f.project, "setup-profile", f.project.AdminToken)
	profileID := channelReceiptString(t, profile, "id")
	flow := f.start(t, profileID)
	location := f.callback(t, flow, f.browser, http.StatusFound)
	require.Equal(t, "select_repository", location.Query().Get("integration_oauth"))
	require.Equal(t, flow.publicID, location.Query().Get("integration_oauth_flow_id"))
	require.Equal(t, "/settings/integrations", location.Path)
	f.assertNoInstall(t)
	installs := f.request(t, http.MethodGet, flow.path+"/installations", nil, http.StatusOK)
	require.Equal(t, "https://github.com/apps/setup-reviewer/installations/new", installs["installation_url"])
	require.Equal(t, []any{map[string]any{"id": "72", "account_login": "acme", "suspended": false}}, installs["data"])
	repositories := f.request(t, http.MethodGet, flow.path+"/installations/72/repositories", nil, http.StatusOK)
	require.Equal(t, []any{map[string]any{
		"id": "73", "full_name": "acme/reviews", "private": true, "can_connect": true,
	}}, repositories["data"])
	f.assertNoInstall(t)
	pageCalls := f.provider.repositoryPageCalls()
	response := f.request(t, http.MethodPost, flow.path+"/complete", setupSelection(), http.StatusOK)
	require.Equal(t, pageCalls, f.provider.repositoryPageCalls(), "completion must not crawl repository inventory pages")
	installID := mustPublicHTTPID(t, publicid.KindIntegrationInstall, channelReceiptString(t, response, "id"))
	install, err := f.project.Store.Integrations().GetIntegrationInstall(t.Context(), f.project.ProjectUUID, installID)
	require.NoError(t, err)
	require.Equal(t, f.appID, install.IntegrationAppID)
	require.Equal(t, "72", install.ProviderTenantID)
	require.Equal(t, "73", install.ProviderAccountRef)
	require.Equal(t, "acme/reviews", install.DisplayName)
	require.Equal(t, uuid.Nil, install.CredentialSecretID,
		"temporary user OAuth is never stored as connection credentials")
	require.Equal(t, f.userID, install.InstalledBy.ID)
	require.JSONEq(t, `{"repository_owner":"acme","repository_name":"reviews","repository_node_id":"R_reviews"}`,
		string(install.ProviderIdentity))
	routes, err := f.project.Store.Integrations().ListActiveIntegrationRoutes(
		t.Context(), f.project.ProjectUUID, install.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, mustPublicHTTPID(t, publicid.KindAgentProfile, profileID), routes[0].AgentProfileID)
	require.Equal(t, "github_pr", routes[0].BehaviorKey)
	f.request(t, http.MethodPost, flow.path+"/complete", setupSelection(), http.StatusUnauthorized)
	f.request(t, http.MethodGet, flow.path+"/installations", nil, http.StatusUnauthorized)
	f.callback(t, flow, f.browser, http.StatusUnauthorized)

	otherProfile := createPublicHTTPAgentProfile(t, f.handler, f.project, "other-setup", "Another setup profile",
		channelReceiptString(t, profile, "current_config_id"), f.project.AdminToken, http.StatusCreated)
	reconnect := f.start(t, channelReceiptString(t, otherProfile, "id"))
	f.callback(t, reconnect, f.browser, http.StatusFound)
	again := f.request(t, http.MethodPost, reconnect.path+"/complete", setupSelection(), http.StatusOK)
	require.Equal(t, response["id"], again["id"])
	sameRoutes, err := f.project.Store.Integrations().ListActiveIntegrationRoutes(
		t.Context(), f.project.ProjectUUID, install.ID)
	require.NoError(t, err)
	require.Equal(t, routes, sameRoutes, "reconnecting never silently reassigns an existing behavior profile")
	for _, projection := range []any{response, installs, repositories, again, location.String()} {
		assertConnectionSetupRedacted(t, projection)
	}
}

func TestIntegrationConnectionGitHubFlowCannotCrossUserOrProject(t *testing.T) {
	t.Parallel()
	f := newConnectionSetupFixture(t)
	flow := f.start(t, "")
	f.callback(t, flow, f.project.AdminSession, http.StatusForbidden)
	require.Zero(t, f.provider.tokenCalls())
	f.callback(t, flow, f.browser, http.StatusFound)
	for _, suffix := range []string{"/installations", "/installations/72/repositories", "/complete"} {
		method, body := http.MethodGet, ""
		if suffix == "/complete" {
			method, body = http.MethodPost, workflowHTTPJSON(t, setupSelection())
		}
		requestJSONWithHeaders(t, f.handler, method, flow.path+suffix, body, "",
			http.StatusUnauthorized, authHeaders(f.project.AdminToken))
	}
	otherProject := f.publicApps().otherProject(t)
	f.grantProject(t, mustPublicHTTPID(t, publicid.KindProject, otherProject))
	otherPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + otherProject +
		"/integration-oauth/" + flow.publicID + "/github"
	f.request(t, http.MethodGet, otherPath+"/installations", nil, http.StatusUnauthorized)
	f.request(t, http.MethodPost, otherPath+"/complete", setupSelection(), http.StatusUnauthorized)
	f.assertNoInstall(t)
	f.request(t, http.MethodPost, flow.path+"/complete", setupSelection(), http.StatusOK)
}

func TestIntegrationConnectionGitHubRequiresFreshRepositoryAdmin(t *testing.T) {
	t.Parallel()
	f := newConnectionSetupFixture(t)
	flow := f.start(t, "")
	f.callback(t, flow, f.browser, http.StatusFound)
	listed := f.request(t, http.MethodGet, flow.path+"/installations/72/repositories", nil, http.StatusOK)
	repository := testutil.RequireType[map[string]any](t, testutil.RequireType[[]any](t, listed["data"])[0])
	require.Equal(t, true, repository["can_connect"])
	f.provider.mu.Lock()
	f.provider.admin = false
	f.provider.mu.Unlock()
	f.request(t, http.MethodPost, flow.path+"/complete", setupSelection(), http.StatusForbidden)
	for _, extra := range []map[string]any{
		{"admin": true}, {"full_name": "browser/forged"},
	} {
		forged := setupSelection()
		for key, value := range extra {
			forged[key] = value
		}
		f.request(t, http.MethodPost, flow.path+"/complete", forged, http.StatusBadRequest)
	}
	f.assertNoInstall(t)
	f.provider.mu.Lock()
	f.provider.admin = true
	f.provider.mu.Unlock()
	wrongID := setupSelection()
	wrongID["repository_id"] = "999"
	f.request(t, http.MethodPost, flow.path+"/complete", wrongID, http.StatusBadRequest)
	wrongName := setupSelection()
	wrongName["repository_full_name"] = "acme/other"
	f.request(t, http.MethodPost, flow.path+"/complete", wrongName, http.StatusBadRequest)
	missingName := setupSelection()
	delete(missingName, "repository_full_name")
	f.request(t, http.MethodPost, flow.path+"/complete", missingName, http.StatusBadRequest)
	f.assertNoInstall(t)
	f.request(t, http.MethodPost, flow.path+"/complete", setupSelection(), http.StatusOK)
}

func TestIntegrationConnectionAppEligibilityAndUserAuthority(t *testing.T) {
	t.Parallel()
	f := newConnectionSetupFixture(t)
	apps := f.publicApps()
	otherProject := apps.otherProject(t)
	otherSecret := apps.secret(t, f.project.OrgID, otherProject)
	otherApp := apps.create(t, otherSecret, otherProject)
	startPath := f.project.ProjectPath + "/integration-oauth/setup"
	f.request(t, http.MethodPost, startPath, map[string]any{"integration_app_id": otherApp}, http.StatusNotFound)
	state := integrationstore.IntegrationAppStateDisabled
	_, err := f.project.Store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
		OrgID: f.project.OrgUUID, ID: f.appID, State: &state,
	})
	require.NoError(t, err)
	f.request(t, http.MethodPost, startPath, map[string]any{"integration_app_id": f.publicAppID}, http.StatusNotFound)
	state = integrationstore.IntegrationAppStateActive
	_, err = f.project.Store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
		OrgID: f.project.OrgUUID, ID: f.appID, State: &state,
	})
	require.NoError(t, err)
	key, _ := createChannelHTTPKey(t, f.project, "admin")
	requestJSONWithHeaders(t, f.handler, http.MethodPost, startPath,
		workflowHTTPJSON(t, map[string]any{"integration_app_id": f.publicAppID}), "", http.StatusForbidden, authHeaders(key))
	// Project management permits setup, while the org-owned App credential remains invisible.
	f.request(t, http.MethodGet, "/api/v1/orgs/"+f.project.OrgID+"/secrets/"+f.secretID, nil, http.StatusNotFound)
	f.start(t, "")
	require.Zero(t, f.provider.tokenCalls(), "starting OAuth never exchanges a code or probes repository contents")
}

func TestIntegrationConnectionGitHubStaleAppVerification(t *testing.T) {
	t.Parallel()
	for _, point := range []string{"before_callback", "after_callback", "during_verification", "secret_rotation"} {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			f := newConnectionSetupFixture(t)
			flow := f.start(t, "")
			if point != "before_callback" {
				f.callback(t, flow, f.browser, http.StatusFound)
			}
			change := func() error {
				name := "App changed after verification"
				input := integrationstore.UpdateIntegrationAppInput{
					OrgID: f.project.OrgUUID, ID: f.appID, DisplayName: &name,
				}
				_, err := f.project.Store.Integrations().UpdateIntegrationApp(t.Context(), input)
				return err
			}
			switch point {
			case "during_verification":
				f.provider.mu.Lock()
				f.provider.beforeRepository = change
				f.provider.mu.Unlock()
			case "secret_rotation":
				_, _, err := f.project.Store.Secrets().CreateSecretVersion(t.Context(), secretstore.CreateSecretVersionInput{
					OrgID: f.project.OrgUUID, SecretID: mustPublicHTTPID(t, publicid.KindSecret, f.secretID),
					Actor: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
					Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{
						"private_key": f.provider.privateKey, "client_secret": "rotated-local-client-secret",
					}},
				})
				require.NoError(t, err)
			default:
				require.NoError(t, change())
			}
			if point == "before_callback" {
				location := f.callback(t, flow, f.browser, http.StatusFound)
				require.Equal(t, "connection_failed", location.Query().Get("integration_oauth_error"))
				require.Zero(t, f.provider.tokenCalls())
			} else {
				f.request(t, http.MethodPost, flow.path+"/complete", setupSelection(), http.StatusConflict)
			}
			f.assertNoInstall(t)
			consumed, err := f.project.Store.Integrations().IntegrationOAuthFlowConsumed(t.Context(), flow.state.FlowID)
			require.NoError(t, err)
			require.False(t, consumed)
		})
	}
}

func TestIntegrationConnectionGitHubExpiredCallback(t *testing.T) {
	t.Parallel()
	f := newConnectionSetupFixture(t)
	flow := f.start(t, "")
	flow.state.ExpiresAt = time.Now().Add(-time.Minute)
	var err error
	flow.stateToken, err = f.server.encodeIntegrationOAuthState(t.Context(), flow.state)
	require.NoError(t, err)
	f.callback(t, flow, f.browser, http.StatusUnauthorized)
	require.Zero(t, f.provider.tokenCalls())
	f.assertNoInstall(t)
}

func TestIntegrationConnectionDiscordUsesVerifiedGuildAndSeedsRuntime(t *testing.T) {
	t.Parallel()
	var applicationFailure, exchanges atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			exchanges.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			if r.Method != http.MethodPost || r.PostForm.Get("client_id") != "31" ||
				r.PostForm.Get("client_secret") != "local-setup-client-secret" || r.PostForm.Get("code") != "verified-code" {
				t.Error("Discord exchange must use registered credentials and the callback code")
			}
			writeJSON(w, http.StatusOK, map[string]any{"token_type": "Bearer", "scope": "bot",
				"guild": map[string]any{"id": "32", "name": "Authorized guild"}})
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bot local-discord-bot" {
			t.Error("Discord setup verification must use the registered bot")
		}
		switch r.URL.Path {
		case "/applications/@me":
			application := map[string]any{"id": "31", "flags": 262144, "bot_require_code_grant": true}
			switch applicationFailure.Load() {
			case 1:
				application["id"] = "999"
			case 2:
				application["bot_require_code_grant"] = false
			case 3:
				application["flags"] = 0
			}
			writeJSON(w, http.StatusOK, application)
		case "/users/@me":
			writeJSON(w, http.StatusOK, map[string]any{"id": "33", "bot": true})
		case "/guilds/32":
			writeJSON(w, http.StatusOK, map[string]any{"id": "32", "name": "Authorized guild"})
		case "/gateway/bot":
			writeJSON(w, http.StatusOK, map[string]any{"shards": 2})
		default:
			t.Errorf("unexpected Discord request: %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool, WithPublicURL("https://omnara.test"), setupGatewayOption(t),
		WithDiscordSetup(discord.SetupConfig{APIURL: provider.URL, TokenURL: provider.URL + "/token",
			AuthorizeURL: provider.URL + "/authorize", HTTPClient: provider.Client()}))
	project := bootstrapPublicHTTPProject(t, handler, "discord-setup")
	apps := publicAppFixture{handler: handler, project: project,
		apps: "/api/v1/orgs/" + project.OrgID + "/integration-apps"}
	secret := apps.request(t, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/secrets", map[string]any{
		"name": "Discord setup credentials", "owner": map[string]any{"kind": "org"},
		"material": map[string]any{"kind": "integration_credentials", "values": map[string]any{
			"client_secret": "local-setup-client-secret", "bot_token": "local-discord-bot",
		}},
	}, http.StatusCreated)
	body := publicAppCreateBody(channelReceiptString(t, secret, "id"), "")
	body["provider"], body["provider_app_ref"], body["provider_config"] = "discord", "31", map[string]any{}
	app := apps.request(t, http.MethodPost, apps.apps, body, http.StatusCreated)
	setupBody := map[string]any{"integration_app_id": app["id"], "return_to": "/settings/integrations"}
	for _, failure := range []int32{1, 2, 3} {
		applicationFailure.Store(failure)
		invalid := apps.request(t, http.MethodPost, project.ProjectPath+"/integration-oauth/setup",
			setupBody, http.StatusBadRequest)
		require.NotContains(t, invalid, "oauth_url", "unready apps cannot send the project user into authorization")
		assertConnectionSetupRedacted(t, invalid)
	}
	require.Zero(t, exchanges.Load())
	applicationFailure.Store(0)
	setup := apps.request(t, http.MethodPost, project.ProjectPath+"/integration-oauth/setup",
		setupBody, http.StatusCreated)
	link, err := url.Parse(channelReceiptString(t, setup, "oauth_url"))
	require.NoError(t, err)
	require.Equal(t, "bot", link.Query().Get("scope"))
	require.Equal(t, "31", link.Query().Get("client_id"))
	query := url.Values{"code": {"verified-code"}, "state": {link.Query().Get("state")}, "guild_id": {"999"}}
	req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+integrationOAuthCallbackPath+"?"+query.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: project.AdminSession})
	response := performRequest(handler, req)
	require.Equal(t, http.StatusFound, response.Code, response.Body.String())
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "success", location.Query().Get("integration_oauth"))
	appID := mustPublicHTTPID(t, publicid.KindIntegrationApp, channelReceiptString(t, app, "id"))
	install, err := project.Store.Integrations().FindIntegrationInstall(
		t.Context(), project.ProjectUUID, appID, "32", "33")
	require.NoError(t, err)
	require.Equal(t, "gateway", install.ConnectionMode)
	require.Equal(t, uuid.Nil, install.CredentialSecretID)
	var units, correctlyConfigured int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (
		WHERE configuration->>'shard_count'='2' AND configuration->>'shard_id' IN ('0','1'))
		FROM integration_runtime_units WHERE integration_app_id=$1`, appID).Scan(&units, &correctlyConfigured))
	require.Equal(t, 2, units)
	require.Equal(t, units, correctlyConfigured)
	assertConnectionSetupRedacted(t, setup)
	assertConnectionSetupRedacted(t, location.String())
}

type connectionSetupFixture struct {
	handler                      http.Handler
	server                       *Server
	project                      publicHTTPProject
	provider                     *githubSetupFake
	appID, userID                uuid.UUID
	publicAppID, secretID, token string
	browser                      string
}

type connectionSetupFlow struct {
	publicID, path, stateToken string
	state                      integrationOAuthState
}

func newConnectionSetupFixture(t *testing.T) *connectionSetupFixture {
	t.Helper()
	f := &connectionSetupFixture{provider: newGitHubSetupFake(t), browser: "setup-manager-browser"}
	pool := openIntegrationDB(t, t.Context())
	redis := integrationredis.OpenClient(t)
	f.handler = newIntegrationServer(pool, WithPublicURL("https://omnara.test"),
		setupGatewayOption(t), WithRedisBackedAuth(redis), WithAuthRateLimiter(allowAllAuthLimiter{}),
		WithGitHubSetup(github.Config{
			APIURL: f.provider.http.URL, AuthorizeURL: f.provider.http.URL + "/authorize",
			TokenURL: f.provider.http.URL + "/token", HTTPClient: f.provider.http.Client(),
		}), func(server *Server) { f.server = server })
	f.project = bootstrapPublicHTTPProject(t, f.handler, "connection-setup")
	user, token := createHTTPOrgMemberToken(t, t.Context(), pool, f.project.Store, f.project.OrgUUID, "setup-manager")
	f.userID, f.token = user.ID, token
	f.grantProject(t, f.project.ProjectUUID)
	createBrowserSessionForHTTPTest(t, t.Context(), f.project.Store, f.userID, f.browser, "setup-csrf")
	apps := f.publicApps()
	secret := apps.request(t, http.MethodPost, "/api/v1/orgs/"+f.project.OrgID+"/secrets", map[string]any{
		"name": "GitHub setup credentials", "owner": map[string]any{"kind": "org"},
		"material": map[string]any{"kind": "integration_credentials", "values": map[string]any{
			"private_key": f.provider.privateKey, "client_secret": "local-setup-client-secret",
		}},
	}, http.StatusCreated)
	f.secretID = channelReceiptString(t, secret, "id")
	body := publicAppCreateBody(f.secretID, "")
	body["provider_app_ref"], body["provider_config"] = "71", map[string]any{"client_id": "Iv1.setup-client"}
	app := apps.request(t, http.MethodPost, apps.apps, body, http.StatusCreated)
	f.publicAppID = channelReceiptString(t, app, "id")
	f.appID = mustPublicHTTPID(t, publicid.KindIntegrationApp, f.publicAppID)
	return f
}

func setupGatewayOption(t *testing.T) Option {
	t.Helper()
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{{
		ID: "setup-gateway", Token: token, Capabilities: []channelconnector.Capability{
			{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "github"},
			{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "discord"},
		},
	}})
	require.NoError(t, err)
	return WithChannelConnectorAuthenticator(auth)
}

func (f *connectionSetupFixture) publicApps() publicAppFixture {
	return publicAppFixture{handler: f.handler, project: f.project,
		apps: "/api/v1/orgs/" + f.project.OrgID + "/integration-apps", eligible: f.project.ProjectPath + "/integration-apps"}
}

func (f *connectionSetupFixture) grantProject(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	_, err := f.project.Store.Identity().AddProjectMembership(t.Context(), identitystore.AddProjectMembershipInput{
		OrgID: f.project.OrgUUID, ProjectID: projectID, UserID: f.userID, Role: authz.ProjectRoleDeveloper,
	})
	require.NoError(t, err)
}

func (f *connectionSetupFixture) request(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	var raw string
	if body != nil {
		raw = workflowHTTPJSON(t, body)
	}
	response := requestJSONWithHeaders(t, f.handler, method, path, raw, "", status, authHeaders(f.token))
	assertConnectionSetupRedacted(t, response)
	return response
}

func (f *connectionSetupFixture) start(t *testing.T, profileID string) connectionSetupFlow {
	t.Helper()
	body := map[string]any{"integration_app_id": f.publicAppID, "return_to": "/settings/integrations"}
	if profileID != "" {
		body["agent_profile_id"] = profileID
	}
	response := f.request(t, http.MethodPost, f.project.ProjectPath+"/integration-oauth/setup", body, http.StatusCreated)
	link, err := url.Parse(channelReceiptString(t, response, "oauth_url"))
	require.NoError(t, err)
	query := link.Query()
	require.Equal(t, "Iv1.setup-client", query.Get("client_id"))
	require.Equal(t, "https://omnara.test"+integrationOAuthCallbackPath, query.Get("redirect_uri"))
	require.Equal(t, "S256", query.Get("code_challenge_method"))
	require.False(t, query.Has("scope"), "GitHub App setup requests no OAuth repository source scopes")
	state, err := f.server.decodeIntegrationOAuthState(t.Context(), query.Get("state"))
	require.NoError(t, err)
	require.Equal(t, identitystore.PKCES256Challenge(state.CodeVerifier), query.Get("code_challenge"))
	require.Equal(t, f.userID, state.InstalledByUserID)
	require.Equal(t, f.appID, state.IntegrationAppID)
	require.Empty(t, state.ClientSecret)
	require.Empty(t, state.SigningSecret)
	flowID := channelReceiptString(t, response, "flow_id")
	f.provider.mu.Lock()
	f.provider.challenges[flowID] = query.Get("code_challenge")
	f.provider.mu.Unlock()
	t.Cleanup(func() {
		_, _, _ = f.server.integrationSetupRedis.GetDelBytes(context.Background(), integrationSetupSessionKey(state.FlowID))
	})
	return connectionSetupFlow{publicID: flowID, state: state, stateToken: query.Get("state"),
		path: f.project.ProjectPath + "/integration-oauth/" + flowID + "/github"}
}

func (f *connectionSetupFixture) callback(t *testing.T, flow connectionSetupFlow, browser string, status int) *url.URL {
	t.Helper()
	query := url.Values{"code": {flow.publicID}, "state": {flow.stateToken}, "installation_id": {"999"}}
	req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+integrationOAuthCallbackPath+"?"+query.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: browser})
	response := performRequest(f.handler, req)
	require.Equal(t, status, response.Code, response.Body.String())
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	assertConnectionSetupRedacted(t, location.String())
	return location
}

func (f *connectionSetupFixture) assertNoInstall(t *testing.T) {
	t.Helper()
	var count int
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT count(*) FROM integration_installs WHERE project_id=$1`, f.project.ProjectUUID).Scan(&count))
	require.Zero(t, count)
}

func setupSelection() map[string]any {
	return map[string]any{"installation_id": "72", "repository_id": "73", "repository_full_name": "acme/reviews"}
}

func assertConnectionSetupRedacted(t *testing.T, value any) {
	t.Helper()
	raw := workflowHTTPJSON(t, value)
	for _, secret := range []string{"ghu_setup-only-user", "local-setup-client-secret", "BEGIN RSA PRIVATE KEY",
		"rotated-local-client-secret", "local-discord-bot"} {
		require.NotContains(t, raw, secret)
	}
}

type githubSetupFake struct {
	http             *httptest.Server
	privateKey       string
	mu               sync.Mutex
	challenges       map[string]string
	usedCodes        map[string]bool
	exchanges        int
	repositoryPages  int
	admin            bool
	beforeRepository func() error
}

func newGitHubSetupFake(t *testing.T) *githubSetupFake {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fake := &githubSetupFake{admin: true, challenges: map[string]string{}, usedCodes: map[string]bool{},
		privateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}
	fake.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.serve(t, w, r)
	}))
	t.Cleanup(fake.http.Close)
	return fake
}

func (f *githubSetupFake) tokenCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges
}

func (f *githubSetupFake) repositoryPageCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repositoryPages
}

func (f *githubSetupFake) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if r.URL.Path == "/token" {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		code := r.PostForm.Get("code")
		challenge, known := f.challenges[code]
		valid := known && !f.usedCodes[code] && identitystore.PKCES256Challenge(r.PostForm.Get("code_verifier")) == challenge
		f.usedCodes[code], f.exchanges = true, f.exchanges+1
		f.mu.Unlock()
		if !valid || r.Method != http.MethodPost || r.PostForm.Get("client_id") != "Iv1.setup-client" ||
			r.PostForm.Get("client_secret") != "local-setup-client-secret" || r.PostForm.Has("scope") ||
			r.PostForm.Get("redirect_uri") != "https://omnara.test"+integrationOAuthCallbackPath {
			t.Error("OAuth exchange did not preserve the exact App and PKCE challenge")
			http.Error(w, "bad exchange", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "ghu_setup-only-user", "token_type": "bearer", "scope": "", "expires_in": 3600,
		})
		return
	}
	if r.Method != http.MethodGet {
		t.Errorf("unexpected provider mutation: %s %s", r.Method, r.URL.Path)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	owner := map[string]any{"id": 8, "node_id": "O_acme", "login": "acme", "type": "Organization"}
	permissions := map[string]any{"pull_requests": "write", "issues": "read", "metadata": "read"}
	installation := map[string]any{"id": 72, "app_id": 71, "account": owner,
		"permissions": permissions, "suspended_at": nil}
	userRequest := strings.HasPrefix(r.URL.Path, "/user/") ||
		strings.HasPrefix(r.URL.Path, "/repos/") && !strings.HasSuffix(r.URL.Path, "/installation")
	if userRequest && r.Header.Get("Authorization") != "Bearer ghu_setup-only-user" {
		t.Error("user-accessible inventories must use the temporary user token")
	}
	if !userRequest && strings.Count(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".") != 2 {
		t.Error("App identity and installation checks must use the registered App JWT")
	}
	switch r.URL.Path {
	case "/app":
		writeJSON(w, http.StatusOK, map[string]any{
			"id": 71, "client_id": "Iv1.setup-client", "slug": "setup-reviewer", "owner": owner, "permissions": permissions,
		})
	case "/app/installations/72", "/repos/acme/reviews/installation":
		writeJSON(w, http.StatusOK, installation)
	case "/user/installations":
		writeJSON(w, http.StatusOK, map[string]any{"total_count": 1, "installations": []any{installation}})
	case "/user/installations/72/repositories":
		f.mu.Lock()
		f.repositoryPages++
		admin := f.admin
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"total_count": 1, "repositories": []any{map[string]any{
			"id": 73, "node_id": "R_reviews", "name": "reviews", "full_name": "acme/reviews", "owner": owner,
			"private": true, "permissions": map[string]any{"admin": admin, "push": true},
		}}})
	case "/repos/acme/reviews", "/repos/acme/other":
		f.mu.Lock()
		admin, hook := f.admin, f.beforeRepository
		f.beforeRepository = nil
		f.mu.Unlock()
		if hook != nil {
			if err := hook(); err != nil {
				t.Error(err)
				http.Error(w, "fixture update failed", http.StatusInternalServerError)
				return
			}
		}
		id, name := 73, "reviews"
		if strings.HasSuffix(r.URL.Path, "/other") {
			id, name = 74, "other"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": id, "node_id": "R_reviews", "name": name, "full_name": "acme/" + name, "owner": owner,
			"private": true, "permissions": map[string]any{"admin": admin, "push": true},
		})
	default:
		t.Errorf("unexpected provider path (no source access or installation token minting allowed): %s", r.URL.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

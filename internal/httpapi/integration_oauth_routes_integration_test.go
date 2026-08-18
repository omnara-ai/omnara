//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

func TestSlackOAuthSetupAndCallbackCreatesProfileIntegrationInstall(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	var exchangeForm url.Values
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse slack form: %v", err)
				}
				exchangeForm = r.PostForm
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-installed-token"),
				)
			case "/users.info":
				writeSlackLookupTestResponse(t, w, r)
			default:
				t.Fatalf("unexpected slack path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("https://app.omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				AccessURL:    slackServer.URL + "/oauth.v2.access",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(t, handler, "slack-oauth")
	profile := createHTTPProfileWithoutIntegrationSendTool(
		t,
		handler,
		project,
		"slack-oauth",
		project.AdminToken,
	)
	profileID := profile["id"].(string)
	profileUUID := mustPublicHTTPID(t, publicid.KindAgentProfile, profileID)
	initialConfigID := mustPublicHTTPID(
		t,
		publicid.KindAgentConfig,
		profile["current_config_id"].(string),
	)

	setup := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profileID+"/integration-oauth/setup",
		`{"provider":"slack","client_id":"client-123","client_secret":"client-secret",`+
			`"signing_secret":"signing-secret","return_to":"/settings/integrations"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if setup["events_url"] != "https://app.omnara.test"+integrationEventsPath ||
		setup["actions_url"] != "https://app.omnara.test"+integrationActionsPath {
		t.Fatalf("unexpected setup callback urls: %v", setup)
	}
	updatedProfile, err := project.Store.Execution().GetAgentProfile(ctx, project.ProjectUUID, profileUUID)
	if err != nil {
		t.Fatalf("get updated agent profile: %v", err)
	}
	if updatedProfile.CurrentConfigID == initialConfigID ||
		!agentConfigHasIntegrationSendTool(updatedProfile.CurrentConfig) {
		t.Fatalf("agent profile config was not updated: %+v", updatedProfile)
	}
	if got := countSlackOAuthCredentialSecrets(t, ctx, project.Store, project); got != 0 {
		t.Fatalf("Slack OAuth credential secret count = %d want 0", got)
	}
	oauthURL, err := url.Parse(setup["oauth_url"].(string))
	if err != nil {
		t.Fatalf("parse oauth url: %v", err)
	}
	query := oauthURL.Query()
	if query.Get("client_id") != "client-123" ||
		query.Get("redirect_uri") != "https://app.omnara.test"+integrationOAuthCallbackPath ||
		query.Get("state") == "" {
		t.Fatalf("unexpected oauth query: %v", query)
	}
	requestedScopes := slack.GrantedScopes(query.Get("scope"))
	for _, scope := range slack.RequiredBotScopes {
		if !requestedScopes[scope] {
			t.Fatalf(
				"oauth scope %q missing from %q",
				scope,
				query.Get("scope"),
			)
		}
	}

	unauthCallback := httptest.NewRequest(
		http.MethodGet,
		"https://app.omnara.test"+integrationOAuthCallbackPath+"?code=callback-code&state="+url.QueryEscape(
			query.Get("state"),
		),
		nil,
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, unauthCallback)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated callback status=%d want=%d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	patCallback := httptest.NewRequest(
		http.MethodGet,
		"https://app.omnara.test"+integrationOAuthCallbackPath+"?code=callback-code&state="+url.QueryEscape(
			query.Get("state"),
		),
		nil,
	)
	for name, value := range authHeaders(project.AdminToken) {
		patCallback.Header.Set(name, value)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, patCallback)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("PAT-authenticated callback status=%d want=%d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	createBrowserSessionForHTTPTest(
		t,
		ctx,
		project.Store,
		project.AdminUserUUID,
		"slack-browser-session",
		"slack-csrf",
	)
	callback := httptest.NewRequest(
		http.MethodGet,
		"https://app.omnara.test"+integrationOAuthCallbackPath+"?code=callback-code&state="+url.QueryEscape(
			query.Get("state"),
		),
		nil,
	)
	callback.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: "slack-browser-session"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, callback)
	if rec.Code != http.StatusFound {
		t.Fatalf(
			"callback status=%d want=%d body=%s",
			rec.Code,
			http.StatusFound,
			rec.Body.String(),
		)
	}
	if exchangeForm.Get("client_id") != "client-123" || exchangeForm.Get("client_secret") != "client-secret" ||
		exchangeForm.Get("code") != "callback-code" ||
		exchangeForm.Get("redirect_uri") != "https://app.omnara.test"+integrationOAuthCallbackPath {
		t.Fatalf("unexpected slack exchange form: %v", exchangeForm)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if location.Scheme != "https" || location.Host != "app.omnara.test" || location.Path != "/settings/integrations" ||
		location.Query().Get("integration_oauth") != "success" {
		t.Fatalf("unexpected callback redirect: %s", location.String())
	}
	if len(location.Query()) != 1 {
		t.Fatalf("unexpected callback redirect params: %s", location.String())
	}
	install, err := project.Store.Integrations().GetIntegrationInstallByProviderAccount(
		ctx,
		integrationstore.IntegrationProviderSlack,
		"T123",
		"A123",
	)
	if err != nil {
		t.Fatalf("get integration install: %v", err)
	}
	identity, err := slack.ParseInstallIdentity(install.ProviderIdentity)
	if err != nil {
		t.Fatalf("parse install identity: %v", err)
	}
	var metadata slack.InstallMetadata
	if err := json.Unmarshal(install.ProviderMetadata, &metadata); err != nil {
		t.Fatalf("parse install metadata: %v", err)
	}
	if install.AgentProfileID != profileUUID ||
		install.Provider != integrationstore.IntegrationProviderSlack ||
		install.State != integrationstore.IntegrationInstallStateActive ||
		install.IntegrationKind != slack.IntegrationKindAgentProfile ||
		install.ConnectionMode != slack.ConnectionModeWebhook ||
		install.ProviderAccountRef != "A123" ||
		identity.BotUserID != "U_BOT" ||
		metadata.TeamName != "Acme" ||
		install.ProviderAgentDisplayName != "Omnara" ||
		install.ProviderTenantID != "T123" {
		t.Fatalf("unexpected install: %+v", install)
	}
	botPayload, err := project.Store.Secrets().GetProjectOwnedSecretPayload(
		ctx,
		project.OrgUUID,
		project.ProjectUUID,
		install.CredentialSecretID,
	)
	if err != nil {
		t.Fatalf("get bot secret payload: %v", err)
	}
	credentials, err := slack.AppCredentialsFromPayload(botPayload)
	if err != nil {
		t.Fatalf("read integration credentials: %v", err)
	}
	if credentials.BotToken != "xoxb-installed-token" || credentials.ClientID != "client-123" ||
		credentials.ClientSecret != "client-secret" || credentials.SigningSecret != "signing-secret" {
		t.Fatalf("unexpected integration credentials: %+v", credentials)
	}

	replay := httptest.NewRequest(
		http.MethodGet,
		"https://app.omnara.test"+integrationOAuthCallbackPath+"?code=callback-code&state="+url.QueryEscape(
			query.Get("state"),
		),
		nil,
	)
	replay.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: "slack-browser-session"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, replay)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf(
			"replayed callback status=%d want=%d body=%s",
			rec.Code,
			http.StatusUnauthorized,
			rec.Body.String(),
		)
	}
	if countSlackOAuthCredentialSecrets(t, ctx, project.Store, project) != 1 {
		t.Fatalf("replayed callback should not create another bot secret")
	}
}

func TestSlackOAuthCallbackAllowsMultipleActiveAppsForProfileWorkspace(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()

	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/users.info" {
				writeSlackLookupTestResponse(t, w, r)
				return
			}
			if r.URL.Path != "/oauth.v2.access" {
				t.Fatalf("unexpected slack path %s", r.URL.Path)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse slack form: %v", err)
			}
			switch r.PostForm.Get("code") {
			case "first-code":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponseForProviderIdentity(
						"xoxb-first-token",
						"A_FIRST",
						"U_FIRST_BOT",
						"T123",
					),
				)
			case "second-code":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponseForProviderIdentity(
						"xoxb-second-token",
						"A_SECOND",
						"U_SECOND_BOT",
						"T123",
					),
				)
			default:
				t.Fatalf("unexpected oauth code %q", r.PostForm.Get("code"))
			}
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("https://app.omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				AccessURL:    slackServer.URL + "/oauth.v2.access",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(t, handler, "slack-oauth-multiple-apps")
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"slack-oauth-multiple-apps",
		project.AdminToken,
	)
	profileID := profile["id"].(string)
	createBrowserSessionForHTTPTest(
		t,
		ctx,
		project.Store,
		project.AdminUserUUID,
		"slack-multiple-apps-browser-session",
		"slack-multiple-apps-csrf",
	)

	complete := func(code, appID string) integrationstore.IntegrationInstallRecord {
		t.Helper()
		setup := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			project.ProjectPath+"/agent-profiles/"+profileID+"/integration-oauth/setup",
			`{"provider":"slack","client_id":"client-123","client_secret":"client-secret",`+
				`"signing_secret":"signing-secret","return_to":"/settings/integrations"}`,
			"",
			http.StatusCreated,
			authHeaders(project.AdminToken),
		)
		oauthURL, err := url.Parse(setup["oauth_url"].(string))
		if err != nil {
			t.Fatalf("parse oauth url: %v", err)
		}
		callback := httptest.NewRequest(
			http.MethodGet,
			"https://app.omnara.test"+integrationOAuthCallbackPath+"?code="+url.QueryEscape(
				code,
			)+"&state="+url.QueryEscape(
				oauthURL.Query().Get("state"),
			),
			nil,
		)
		callback.AddCookie(
			&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: "slack-multiple-apps-browser-session"},
		)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, callback)
		if rec.Code != http.StatusFound {
			t.Fatalf(
				"callback status=%d want=%d body=%s",
				rec.Code,
				http.StatusFound,
				rec.Body.String(),
			)
		}
		location, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatalf("parse callback redirect: %v", err)
		}
		if location.Query().Get("integration_oauth") != "success" {
			t.Fatalf("callback redirect = %s, want success", location.String())
		}
		install, err := project.Store.Integrations().GetIntegrationInstallByProviderAccount(
			ctx,
			integrationstore.IntegrationProviderSlack,
			"T123",
			appID,
		)
		if err != nil {
			t.Fatalf("get integration install %s: %v", appID, err)
		}
		return install
	}

	first := complete("first-code", "A_FIRST")
	second := complete("second-code", "A_SECOND")
	if first.ID == second.ID ||
		first.AgentProfileID != second.AgentProfileID ||
		first.ProviderTenantID != second.ProviderTenantID ||
		first.State != integrationstore.IntegrationInstallStateActive ||
		second.State != integrationstore.IntegrationInstallStateActive {
		t.Fatalf("unexpected installs: first=%+v second=%+v", first, second)
	}
}

func TestSlackSetupCreatesManifestAppAndStartsOAuth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	var manifest map[string]any
	var manifestToken string
	var iconToken string
	var iconAppID string
	var iconFilename string
	var iconContentType string
	var iconContent []byte
	var exchangeForm url.Values
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/apps.manifest.create":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse slack manifest form: %v", err)
				}
				manifestToken = r.PostForm.Get("token")
				if err := json.Unmarshal([]byte(r.PostForm.Get("manifest")), &manifest); err != nil {
					t.Fatalf("decode manifest: %v", err)
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":     true,
					"app_id": "A_MANIFEST",
					"credentials": map[string]string{
						"client_id":      "manifest-client",
						"client_secret":  "manifest-secret",
						"signing_secret": "manifest-signing",
					},
				})
			case "/apps.icon.set":
				iconToken, iconAppID, iconFilename, iconContentType, iconContent = readSlackIconSetRequest(t, r)
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "icon_upload_failed"})
			case "/oauth.v2.access":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse slack oauth form: %v", err)
				}
				exchangeForm = r.PostForm
				response := slackOAuthTestResponse("xoxb-manifest-token")
				response["app_id"] = "A_MANIFEST"
				response["bot_user_id"] = "U_MANIFEST_BOT"
				writeJSON(w, http.StatusOK, response)
			default:
				t.Fatalf("unexpected slack path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("https://app.omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				AccessURL:    slackServer.URL + "/oauth.v2.access",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(t, handler, "slack-manifest-setup")
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"slack-manifest-setup",
		project.AdminToken,
	)
	profileUUID := mustPublicHTTPID(t, publicid.KindAgentProfile, profile["id"].(string))
	initialConfigID := mustPublicHTTPID(
		t,
		publicid.KindAgentConfig,
		profile["current_config_id"].(string),
	)
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/slack-setup",
		`{"app_name":"Omnara Test","app_configuration_token":"xoxe-config-token","return_to":"/settings/integrations"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if manifestToken != "xoxe-config-token" {
		t.Fatalf("manifest token = %q", manifestToken)
	}
	unchangedProfile, err := project.Store.Execution().GetAgentProfile(ctx, project.ProjectUUID, profileUUID)
	if err != nil {
		t.Fatalf("get unchanged agent profile: %v", err)
	}
	if unchangedProfile.CurrentConfigID != initialConfigID {
		t.Fatalf(
			"ready agent profile config changed from %s to %s",
			initialConfigID,
			unchangedProfile.CurrentConfigID,
		)
	}
	defaultIcon := slack.DefaultAppIcon()
	if iconToken != "xoxe-config-token" || iconAppID != "A_MANIFEST" {
		t.Fatalf("unexpected icon setup token=%q app_id=%q", iconToken, iconAppID)
	}
	if iconFilename != defaultIcon.Filename || iconContentType != defaultIcon.ContentType ||
		!bytes.Equal(iconContent, defaultIcon.Content) {
		t.Fatalf("unexpected icon upload filename=%q content_type=%q bytes=%d", iconFilename, iconContentType, len(iconContent))
	}
	settings := manifest["settings"].(map[string]any)
	events := settings["event_subscriptions"].(map[string]any)
	if events["request_url"] != "https://app.omnara.test"+integrationEventsPath {
		t.Fatalf("events request_url = %v", events["request_url"])
	}
	interactivity := settings["interactivity"].(map[string]any)
	if interactivity["request_url"] != "https://app.omnara.test"+integrationActionsPath {
		t.Fatalf("actions request_url = %v", interactivity["request_url"])
	}
	oauth := manifest["oauth_config"].(map[string]any)
	redirects := oauth["redirect_urls"].([]any)
	if len(redirects) != 1 || redirects[0] != "https://app.omnara.test"+integrationOAuthCallbackPath {
		t.Fatalf("redirect_urls = %v", redirects)
	}
	scopes := oauth["scopes"].(map[string]any)["bot"].([]any)
	gotScopes := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		gotScopes[scope.(string)] = true
	}
	for _, scope := range slack.RequiredBotScopes {
		if !gotScopes[scope] {
			t.Fatalf("manifest scope %q missing from %v", scope, gotScopes)
		}
	}
	features := manifest["features"].(map[string]any)
	display := manifest["display_information"].(map[string]any)
	if display["background_color"] != "#000000" {
		t.Fatalf("display_information = %v", display)
	}
	appHome := features["app_home"].(map[string]any)
	if appHome["messages_tab_enabled"] != true ||
		appHome["messages_tab_read_only_enabled"] != false {
		t.Fatalf("app_home = %v", appHome)
	}
	if response["provider"] != integrationstore.IntegrationProviderSlack || response["slack_app_id"] != "A_MANIFEST" ||
		response["events_url"] != "https://app.omnara.test"+integrationEventsPath ||
		response["actions_url"] != "https://app.omnara.test"+integrationActionsPath {
		t.Fatalf("unexpected setup response: %v", response)
	}
	oauthURL, err := url.Parse(response["oauth_url"].(string))
	if err != nil {
		t.Fatalf("parse oauth url: %v", err)
	}
	if oauthURL.Query().Get("client_id") != "manifest-client" ||
		oauthURL.Query().Get("redirect_uri") != "https://app.omnara.test"+integrationOAuthCallbackPath ||
		oauthURL.Query().Get("state") == "" {
		t.Fatalf("unexpected oauth query: %v", oauthURL.Query())
	}

	createBrowserSessionForHTTPTest(
		t,
		ctx,
		project.Store,
		project.AdminUserUUID,
		"manifest-browser-session",
		"slack-csrf",
	)
	callback := httptest.NewRequest(
		http.MethodGet,
		"https://app.omnara.test"+integrationOAuthCallbackPath+"?code=manifest-code&state="+url.QueryEscape(
			oauthURL.Query().Get("state"),
		),
		nil,
	)
	callback.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: "manifest-browser-session"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, callback)
	if rec.Code != http.StatusFound {
		t.Fatalf(
			"callback status=%d want=%d body=%s",
			rec.Code,
			http.StatusFound,
			rec.Body.String(),
		)
	}
	if exchangeForm.Get("client_id") != "manifest-client" ||
		exchangeForm.Get("client_secret") != "manifest-secret" ||
		exchangeForm.Get("code") != "manifest-code" {
		t.Fatalf("unexpected manifest oauth exchange form: %v", exchangeForm)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if location.Scheme != "https" || location.Host != "app.omnara.test" || location.Path != "/settings/integrations" ||
		location.Query().Get("integration_oauth") != "success" {
		t.Fatalf("unexpected callback redirect: %s", location.String())
	}
	if len(location.Query()) != 1 {
		t.Fatalf("unexpected callback redirect params: %s", location.String())
	}
	install, err := project.Store.Integrations().GetIntegrationInstallByProviderAccount(
		ctx,
		integrationstore.IntegrationProviderSlack,
		"T123",
		"A_MANIFEST",
	)
	if err != nil {
		t.Fatalf("get manifest-created install: %v", err)
	}
	identity, err := slack.ParseInstallIdentity(install.ProviderIdentity)
	if err != nil {
		t.Fatalf("parse manifest-created identity: %v", err)
	}
	if identity.BotUserID != "U_MANIFEST_BOT" ||
		install.ProviderAccountRef != "A_MANIFEST" ||
		install.ProviderTenantID != "T123" {
		t.Fatalf("unexpected manifest-created install: %+v", install)
	}
	assertSlackSetupSecretName(t, ctx, project.Store, project, install.CredentialSecretID, "slack-credentials-")
}

func TestSlackSetupUploadsCustomAppIcon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	customIcon := slack.DefaultAppIcon()
	var iconFilename string
	var iconContentType string
	var iconContent []byte
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/apps.manifest.create":
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":     true,
					"app_id": "A_CUSTOM_ICON",
					"credentials": map[string]string{
						"client_id":      "custom-icon-client",
						"client_secret":  "custom-icon-secret",
						"signing_secret": "custom-icon-signing",
					},
				})
			case "/apps.icon.set":
				_, _, iconFilename, iconContentType, iconContent = readSlackIconSetRequest(t, r)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("https://app.omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				AccessURL:    slackServer.URL + "/oauth.v2.access",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(t, handler, "slack-custom-icon")
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"slack-custom-icon",
		project.AdminToken,
	)
	body := `{"app_name":"Icon Test","app_configuration_token":"xoxe-config-token","icon":{"filename":"custom.png","data_base64":"` +
		base64.StdEncoding.EncodeToString(customIcon.Content) + `"}}`
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/slack-setup",
		body,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if iconFilename != "custom.png" || iconContentType != customIcon.ContentType ||
		!bytes.Equal(iconContent, customIcon.Content) {
		t.Fatalf("unexpected custom icon upload filename=%q content_type=%q bytes=%d", iconFilename, iconContentType, len(iconContent))
	}
}

func readSlackIconSetRequest(t *testing.T, r *http.Request) (string, string, string, string, []byte) {
	t.Helper()
	if err := r.ParseMultipartForm(maxSlackSetupRequestBodyBytes); err != nil {
		t.Fatalf("parse slack icon multipart form: %v", err)
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		t.Fatalf("slack icon files = %d, want 1", len(files))
	}
	file, err := files[0].Open()
	if err != nil {
		t.Fatalf("open slack icon file: %v", err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read slack icon file: %v", err)
	}
	return r.FormValue("token"),
		r.FormValue("app_id"),
		files[0].Filename,
		files[0].Header.Get("Content-Type"),
		content
}

func assertSlackSetupSecretName(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	secretID storage.ID,
	prefix string,
) {
	t.Helper()
	secret, err := store.Secrets().GetSecret(ctx, project.OrgUUID, secretID)
	if err != nil {
		t.Fatalf("get slack setup secret: %v", err)
	}
	if !strings.HasPrefix(secret.Name, prefix) {
		t.Fatalf("secret name = %q, want prefix %q", secret.Name, prefix)
	}
	if secret.Kind != secretstore.SecretKindSlackAppCredentials {
		t.Fatalf("secret kind = %q, want %q", secret.Kind, secretstore.SecretKindSlackAppCredentials)
	}
	suffix := strings.TrimPrefix(secret.Name, prefix)
	if len(suffix) != 8 {
		t.Fatalf("secret name = %q, want 8 character suffix", secret.Name)
	}
}

func TestSlackSetupRejectsNonPublicPublicURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()

	var manifestCalled bool
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			manifestCalled = true
			writeJSON(
				w,
				http.StatusInternalServerError,
				map[string]string{"error": "unexpected slack call"},
			)
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://localhost:5173"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"slack-manifest-local-public-url",
	)
	body := requestRawWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/aprf_aaaaaaaaaaaaaaaaaaaaaaaaaa/slack-setup",
		`{"app_name":"Omnara Test","app_configuration_token":"xoxe-config-token"}`,
		http.StatusServiceUnavailable,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(body, "OMNARA_PUBLIC_URL") {
		t.Fatalf("response body missing OMNARA_PUBLIC_URL: %s", body)
	}
	if manifestCalled {
		t.Fatal("slack manifest creation was called for non-public public URL")
	}
}

func TestIntegrationOAuthSetupRejectsNonPublicSlackPublicURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://localhost:5173"),
	)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"slack-oauth-local-public-url",
	)
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"slack-oauth-local-public-url",
		project.AdminToken,
	)
	body := requestRawWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/integration-oauth/setup",
		`{"provider":"slack","client_id":"client-123","client_secret":"client-secret",`+
			`"signing_secret":"signing-secret"}`,
		http.StatusServiceUnavailable,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(body, "OMNARA_PUBLIC_URL") {
		t.Fatalf("response body missing OMNARA_PUBLIC_URL: %s", body)
	}
}

func TestSlackSetupEnablesIntegrationSendTool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	var manifestCalls int
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/apps.manifest.create":
				manifestCalls++
				if manifestCalls == 1 {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "temporary_failure"})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":     true,
					"app_id": "A_MISSING_TOOL",
					"credentials": map[string]string{
						"client_id":      "missing-tool-client",
						"client_secret":  "missing-tool-secret",
						"signing_secret": "missing-tool-signing",
					},
				})
			case "/apps.icon.set":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("https://omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"slack-manifest-missing-tool",
	)
	profile := createHTTPProfileWithoutIntegrationSendTool(
		t,
		handler,
		project,
		"slack-manifest-missing-tool",
		project.AdminToken,
	)
	profileUUID := mustPublicHTTPID(t, publicid.KindAgentProfile, profile["id"].(string))
	initialConfigID := mustPublicHTTPID(
		t,
		publicid.KindAgentConfig,
		profile["current_config_id"].(string),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/slack-setup",
		`{"app_name":"Omnara Test","app_configuration_token":"xoxe-config-token"}`,
		"temporary_failure",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if manifestCalls != 1 {
		t.Fatalf("slack manifest creation calls = %d want 1", manifestCalls)
	}
	updatedProfile, err := project.Store.Execution().GetAgentProfile(ctx, project.ProjectUUID, profileUUID)
	if err != nil {
		t.Fatalf("get updated agent profile: %v", err)
	}
	if updatedProfile.CurrentConfigID == initialConfigID ||
		!agentConfigHasIntegrationSendTool(updatedProfile.CurrentConfig) {
		t.Fatalf("agent profile config was not updated: %+v", updatedProfile)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/slack-setup",
		`{"app_name":"Omnara Test","app_configuration_token":"xoxe-config-token"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if manifestCalls != 2 {
		t.Fatalf("slack manifest creation calls = %d want 2", manifestCalls)
	}
	retriedProfile, err := project.Store.Execution().GetAgentProfile(ctx, project.ProjectUUID, profileUUID)
	if err != nil {
		t.Fatalf("get retried agent profile: %v", err)
	}
	if retriedProfile.CurrentConfigID != updatedProfile.CurrentConfigID {
		t.Fatalf(
			"retry changed agent profile config from %s to %s",
			updatedProfile.CurrentConfigID,
			retriedProfile.CurrentConfigID,
		)
	}
}

func TestSlackSetupRejectsAppNameOverSlackLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	var manifestCalled bool
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			manifestCalled = true
			writeJSON(
				w,
				http.StatusInternalServerError,
				map[string]string{"error": "unexpected slack call"},
			)
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("https://omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"slack-manifest-long-name",
	)
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"slack-manifest-long-name",
		project.AdminToken,
	)
	body := `{"app_name":"Omnara Local Runtime Error Test Tunnel","app_configuration_token":"xoxe-config-token"}`
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/slack-setup",
		body,
		"app_name must be 35 characters or fewer",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if manifestCalled {
		t.Fatal(
			"slack manifest creation was called for app name over Slack limit",
		)
	}
}

func TestSlackSetupRejectsReservedAppName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()

	var manifestCalled bool
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			manifestCalled = true
			writeJSON(
				w,
				http.StatusInternalServerError,
				map[string]string{"error": "unexpected slack call"},
			)
		}),
	)
	defer slackServer.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("https://omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AuthorizeURL: "http://slack.test/oauth/v2/authorize",
				APIURL:       slackServer.URL,
				HTTPClient:   slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"slack-manifest-reserved-name",
	)
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"slack-manifest-reserved-name",
		project.AdminToken,
	)
	body := `{"app_name":"slackbot","app_configuration_token":"xoxe-config-token"}`
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/slack-setup",
		body,
		"Slack reserves this app name. Choose a different name.",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if manifestCalled {
		t.Fatal(
			"slack manifest creation was called for reserved app name",
		)
	}
}

func createSlackReadyHTTPProfile(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed string,
	token string,
) map[string]any {
	t.Helper()
	sourceYAML := "instruction: Help the user make progress.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\ntools:\n  send_integration_message: {}\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		seed+"-slack-ready",
		"yaml",
		sourceYAML,
		token,
		http.StatusCreated,
	)
	return createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		seed,
		seed+" Agent",
		config["id"].(string),
		token,
		http.StatusCreated,
	)
}

func createHTTPProfileWithoutIntegrationSendTool(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed string,
	token string,
) map[string]any {
	t.Helper()
	sourceYAML := "instruction: Help the user make progress.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		seed+"-missing-integration-send",
		"yaml",
		sourceYAML,
		token,
		http.StatusCreated,
	)
	return createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		seed,
		seed+" Agent",
		config["id"].(string),
		token,
		http.StatusCreated,
	)
}

func createBrowserSessionForHTTPTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	token, csrf string,
) {
	t.Helper()
	if _, err := store.Identity().CreateBrowserSession(ctx, identitystore.CreateBrowserSessionInput{
		UserID:    userID,
		Token:     token,
		CSRFToken: csrf,
		TTL:       time.Hour,
	}); err != nil {
		t.Fatalf("create browser session: %v", err)
	}
}

func completeSlackOAuthInstall(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	profileID, browserSessionToken, code string,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	setup := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profileID+"/integration-oauth/setup",
		`{"provider":"slack","client_id":"client-123","client_secret":"client-secret",`+
			`"signing_secret":"signing-secret","return_to":"/settings/integrations"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	oauthURL, err := url.Parse(setup["oauth_url"].(string))
	if err != nil {
		t.Fatalf("parse oauth url: %v", err)
	}
	callback := httptest.NewRequest(
		http.MethodGet,
		"https://omnara.test"+integrationOAuthCallbackPath+"?code="+url.QueryEscape(
			code,
		)+"&state="+url.QueryEscape(
			oauthURL.Query().Get("state"),
		),
		nil,
	)
	callback.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: browserSessionToken})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, callback)
	if rec.Code != http.StatusFound {
		t.Fatalf(
			"callback status=%d want=%d body=%s",
			rec.Code,
			http.StatusFound,
			rec.Body.String(),
		)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if location.Scheme != "https" || location.Host != "omnara.test" || location.Path != "/settings/integrations" ||
		location.Query().Get("integration_oauth") != "success" {
		t.Fatalf("callback redirect = %s, want success", location.String())
	}
	if len(location.Query()) != 1 {
		t.Fatalf("unexpected callback redirect params: %s", location.String())
	}
	install, err := project.Store.Integrations().GetIntegrationInstallByProviderAccount(
		context.Background(),
		integrationstore.IntegrationProviderSlack,
		"T123",
		"A123",
	)
	if err != nil {
		t.Fatalf("get integration install: %v", err)
	}
	return install
}

func slackOAuthTestResponse(token string) map[string]any {
	return slackOAuthTestResponseForProviderIdentity(token, "A123", "U_BOT", "T123")
}

func slackOAuthTestResponseForProviderIdentity(
	token, appID, botUserID, teamID string,
) map[string]any {
	return map[string]any{
		"ok":           true,
		"access_token": token,
		"token_type":   "bot",
		"scope":        strings.Join(slack.RequiredBotScopes, ","),
		"bot_user_id":  botUserID,
		"app_id":       appID,
		"team": map[string]string{
			"id":   teamID,
			"name": "Acme",
		},
		"is_enterprise_install": false,
	}
}

func countSlackOAuthCredentialSecrets(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
) int {
	t.Helper()
	page, err := store.Secrets().ListProjectAvailableSecrets(
		ctx,
		secretstore.ListProjectAvailableSecretsInput{
			OrgID:     project.OrgUUID,
			ProjectID: project.ProjectUUID,
			Limit:     100,
		},
	)
	if err != nil {
		t.Fatalf("list slack oauth secrets: %v", err)
	}
	count := 0
	for _, access := range page.Accesses {
		if strings.HasPrefix(access.Secret.Name, "slack-credentials-") {
			count++
		}
	}
	return count
}

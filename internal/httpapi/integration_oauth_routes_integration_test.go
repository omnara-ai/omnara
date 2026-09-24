//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestIntegrationOAuthScopeAndInputValidation(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	integrationRef := testPublicID(t, publicid.KindProjectIntegration, f.integration.ID)
	path, body := projectSlackSetupRequest(t, f.project, integrationRef, false)
	for _, invalid := range []string{
		`{}`, `{"client_id":"client","client_secret":"secret","signing_secret":""}`,
		`{"provider":"discord","client_id":"client","client_secret":"secret","signing_secret":"signing"}`,
	} {
		requestJSONWithHeaders(
			t,
			f.handler,
			http.MethodPost,
			path,
			invalid,
			"",
			http.StatusBadRequest,
			authHeaders(f.project.AdminToken),
		)
	}
	other := createSetupHTTPIntegration(t, f.handler, f.project, "github", integrationdefinition.GitHubPR)
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		strings.Replace(path, integrationRef, testPublicID(t, publicid.KindProjectIntegration, other.ID), 1),
		body,
		"",
		http.StatusBadRequest,
		authHeaders(f.project.AdminToken),
	)
	for _, manifest := range []bool{false, true} {
		path, body := projectSlackSetupRequest(
			t,
			f.project,
			testPublicID(t, publicid.KindProjectIntegration, uuid.New()),
			manifest,
		)
		requestJSONWithHeaders(
			t,
			f.handler,
			http.MethodPost,
			path,
			body,
			"",
			http.StatusNotFound,
			authHeaders(f.project.AdminToken),
		)
	}
	require.Zero(t, f.exchanges.Load())
	require.Zero(t, f.manifests.Load())
}

func TestIntegrationSlackSetupNameIconAndPublicURLValidation(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	path := f.project.ProjectPath + "/integrations/" + testPublicID(
		t,
		publicid.KindProjectIntegration,
		f.integration.ID,
	) + "/slack-setup"
	for _, name := range []string{"", strings.Repeat("x", 36), "SlackBot"} {
		requestJSONWithHeaders(
			t,
			f.handler,
			http.MethodPost,
			path,
			projectIntegrationHTTPJSON(
				t,
				map[string]any{"app_name": name, "app_configuration_token": "configuration-token"},
			),
			"",
			http.StatusBadRequest,
			authHeaders(f.project.AdminToken),
		)
	}
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		path,
		`{"app_name":"Valid","app_configuration_token":"token","icon":{"data_base64":"invalid!"}}`,
		"",
		http.StatusBadRequest,
		authHeaders(f.project.AdminToken),
	)
	require.Zero(t, f.manifests.Load())
	for _, publicURL := range []string{"http://omnara.test", "https://localhost", "https://127.0.0.1"} {
		handler := newIntegrationServer(f.pool, WithPublicURL(publicURL))
		for _, manifest := range []bool{false, true} {
			route, body := projectSlackSetupRequest(
				t,
				f.project,
				testPublicID(t, publicid.KindProjectIntegration, f.integration.ID),
				manifest,
			)
			requestJSONWithHeaders(
				t,
				handler,
				http.MethodPost,
				route,
				body,
				"",
				http.StatusServiceUnavailable,
				authHeaders(f.project.AdminToken),
			)
		}
	}
}

func TestIntegrationOAuthRejectsForeignIdentityWithoutLosingCurrentCredentials(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	token, _ := f.start(t, false)
	current := f.complete(t, token, "original")
	for _, code := range []string{"foreign-app", "foreign-workspace", "foreign-bot"} {
		token, _ = f.start(t, false)
		rec := projectSlackCallback(f.handler, token, code, f.project.AdminSession)
		require.Equal(t, http.StatusFound, rec.Code)
		location, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		require.Equal(t, "setup_save_failed", location.Query().Get("integration_oauth_error"))
		after, err := f.project.Store.Integrations().
			GetProjectIntegration(t.Context(), f.project.ProjectUUID, current.ID)
		require.NoError(t, err)
		require.Equal(t, current, after)
		require.Equal(
			t,
			1,
			countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project),
		)
	}
	path, body := projectSlackSetupRequest(
		t,
		f.project,
		testPublicID(t, publicid.KindProjectIntegration, f.integration.ID),
		true,
	)
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusBadRequest,
		authHeaders(f.project.AdminToken),
	)
	require.Zero(t, f.manifests.Load(), "reconnect must not create a different physical Slack app")
}

func TestIntegrationOAuthCallbackStateExpiryAndProviderErrors(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	token, state := f.start(t, false)
	require.Equal(
		t,
		http.StatusUnauthorized,
		projectSlackCallback(f.handler, token+"tampered", "code", f.project.AdminSession).Code,
	)
	state.ExpiresAt = time.Now().Add(-time.Second)
	encoder := &Server{secretKeyWrapper: integrationKeyWrapper()}
	expired, err := encoder.encodeIntegrationOAuthState(t.Context(), state)
	require.NoError(t, err)
	require.Equal(
		t,
		http.StatusUnauthorized,
		projectSlackCallback(f.handler, expired, "code", f.project.AdminSession).Code,
	)
	missing := projectSlackCallback(f.handler, token, "", f.project.AdminSession)
	require.Equal(t, http.StatusFound, missing.Code)
	require.Contains(t, missing.Header().Get("Location"), "missing_code")
	require.Zero(t, f.exchanges.Load())
	require.Zero(t, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
}

func TestIntegrationOAuthProviderFailuresLeaveSavedIntegrationDisconnected(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	for _, tc := range []struct{ code, outcome string }{
		{"missing-scope", "missing_scope"}, {"exchange-failure", "exchange_failed"},
	} {
		token, state := f.start(t, false)
		rec := projectSlackCallback(f.handler, token, tc.code, f.project.AdminSession)
		require.Equal(t, http.StatusFound, rec.Code)
		location, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		require.Equal(t, tc.outcome, location.Query().Get("integration_oauth_error"))
		consumed, err := f.project.Store.Integrations().
			IntegrationOAuthFlowConsumed(t.Context(), state.FlowID)
		require.NoError(t, err)
		require.False(t, consumed)
	}
	token, _ := f.start(t, false)
	req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+integrationOAuthCallbackPath+
		"?error=access_denied&state="+url.QueryEscape(token), nil)
	req.AddCookie(
		&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: f.project.AdminSession},
	)
	response := performRequest(f.handler, req)
	require.Equal(t, http.StatusFound, response.Code)
	require.Contains(t, response.Header().Get("Location"), "integration_oauth_error=access_denied")
	require.EqualValues(t, 2, f.exchanges.Load(), "provider denial must not exchange a code")
	require.Zero(t, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
	current, err := f.project.Store.Integrations().
		GetProjectIntegration(t.Context(), f.project.ProjectUUID, f.integration.ID)
	require.NoError(t, err)
	require.Equal(t, f.integration, current)
}

type slackIconSetRequest struct {
	token       string
	appID       string
	filename    string
	contentType string
	content     []byte
}

func readSlackIconSetRequest(r *http.Request) (slackIconSetRequest, error) {
	if err := r.ParseMultipartForm(maxSlackSetupRequestBodyBytes); err != nil {
		return slackIconSetRequest{}, fmt.Errorf("parse slack icon multipart form: %w", err)
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		return slackIconSetRequest{}, fmt.Errorf("slack icon files = %d, want 1", len(files))
	}
	file, err := files[0].Open()
	if err != nil {
		return slackIconSetRequest{}, fmt.Errorf("open slack icon file: %w", err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(file)
	if err != nil {
		return slackIconSetRequest{}, fmt.Errorf("read slack icon file: %w", err)
	}
	return slackIconSetRequest{
		token: r.FormValue("token"), appID: r.FormValue("app_id"),
		filename: files[0].Filename, contentType: files[0].Header.Get("Content-Type"),
		content: content,
	}, nil
}

func createSlackReadyHTTPProfile(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed string,
	token string,
) map[string]any {
	t.Helper()
	sourceYAML := "instruction: Help the user make progress.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n"
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
		testutil.RequireType[string](t, config["id"]),
		token,
		http.StatusCreated,
	)
}

func createBrowserSessionForHTTPTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID uuid.UUID,
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
) integrationstore.ProjectIntegrationRecord {
	t.Helper()
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/integrations",
		projectIntegrationHTTPJSON(t, map[string]any{
			"name":             "slack-" + uuid.NewString()[:8],
			"integration_type": integrationdefinition.SlackThread,
			"settings": map[string]any{
				"launcher": map[string]any{
					"trigger":    "mention",
					"scope_kind": "workspace",
					"scope_ref":  "T123",
					"slots": []any{
						map[string]any{"key": "default", "agent_profile_id": profileID},
					},
				},
			},
		}),
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	integrationRef := testutil.RequireType[string](t, created["id"])
	path, body := projectSlackSetupRequest(t, project, integrationRef, false)
	setup := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	oauthURL, err := url.Parse(testutil.RequireType[string](t, setup["oauth_url"]))
	require.NoError(t, err)
	rec := projectSlackCallback(handler, oauthURL.Query().Get("state"), code, browserSessionToken)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "success", location.Query().Get("integration_oauth"))
	require.Equal(t, integrationRef, location.Query().Get("integration_id"))
	integration, err := project.Store.Integrations().
		GetProjectIntegration(
			t.Context(),
			project.ProjectUUID,
			mustPublicHTTPID(t, publicid.KindProjectIntegration, integrationRef),
		)
	require.NoError(t, err)
	return integration
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

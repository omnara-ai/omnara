//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func appSetupHTTPBody(tenant, account string) map[string]any {
	return map[string]any{
		"provider_tenant_id":      tenant,
		"provider_account_ref":    account,
		"expected_setup_revision": int64(1),
	}
}

func createSetupHTTPApp(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	name string,
	appType appdefinition.Type,
) integrationstore.ProjectAppRecord {
	t.Helper()
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/apps",
		projectAppHTTPJSON(
			t,
			map[string]any{"name": name, "app_type": appType, "settings": map[string]any{}},
		),
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	id := mustPublicHTTPID(
		t,
		publicid.KindProjectApp,
		testutil.RequireType[string](t, response["id"]),
	)
	app, err := project.Store.Integrations().GetProjectApp(t.Context(), project.ProjectUUID, id)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, app.State)
	require.Equal(t, uuid.Nil, app.CredentialSecretID)
	return app
}

func appSetupPath(
	t *testing.T,
	project publicHTTPProject,
	app integrationstore.ProjectAppRecord,
) string {
	t.Helper()
	return project.ProjectPath + "/apps/" + testPublicID(
		t,
		publicid.KindProjectApp,
		app.ID,
	) + "/setup"
}

func TestProjectAppCredentialSetupAndDisconnectAuthorization(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(
		openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(appSetupDiscordConfig(t)),
	)
	project := bootstrapPublicHTTPProject(t, handler, "app-setup-auth")
	app := createSetupHTTPApp(t, handler, project, "discord", appdefinition.DiscordThread)
	body := appSetupDiscordBody(t, handler, project, "111")
	path := appSetupPath(t, project, app)
	encoded := projectAppHTTPJSON(t, body)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		encoded,
		"",
		http.StatusUnauthorized,
		nil,
	)
	other := bootstrapPublicHTTPProject(t, handler, "app-setup-foreign")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		encoded,
		"",
		http.StatusNotFound,
		authHeaders(other.AdminToken),
	)
	for _, role := range []string{"viewer", "operator"} {
		user, token := createHTTPOrgMemberToken(
			t,
			t.Context(),
			integrationPoolForHandler(t, handler),
			project.Store,
			project.OrgUUID,
			"setup-"+role,
		)
		_, err := project.Store.Identity().
			AddProjectMembership(t.Context(), identitystore.AddProjectMembershipInput{
				OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: user.ID, Role: role,
			})
		require.NoError(t, err)
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			path,
			encoded,
			"",
			http.StatusForbidden,
			authHeaders(token),
		)
	}
	browser := project.adminBrowserAuthHeaders()
	key := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/api-keys",
		`{"name":"setup-key","org_role":"admin"}`,
		"",
		http.StatusCreated,
		browser,
	)
	keyHeaders := authHeaders(testutil.RequireType[string](t, key["token"]))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		encoded,
		"",
		http.StatusForbidden,
		keyHeaders,
	)
	configured := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		encoded,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	require.Equal(t, "active", configured["state"])
	require.Equal(t, float64(2), configured["setup_revision"])
	require.NotContains(t, projectAppHTTPJSON(t, configured), "bot_token")
	disconnected := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		strings.TrimSuffix(path, "/setup")+"/disconnect",
		"",
		"",
		http.StatusOK,
		keyHeaders,
	)
	require.Equal(
		t,
		"disconnected",
		disconnected["state"],
		"management keys may disconnect without gaining credential-setup permission",
	)
	conflict := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		encoded,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	require.Equal(t, "conflict", conflict["code"])
	require.Equal(t, "conflict: app setup changed; refresh the app and start setup again", conflict["error"])
	body["expected_setup_revision"] = disconnected["setup_revision"]
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
}

func TestProjectAppSetupValidationAndConfigChanges(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(
		openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(appSetupDiscordConfig(t)),
	)
	project := bootstrapPublicHTTPProject(t, handler, "app-setup-validation")
	app := createSetupHTTPApp(t, handler, project, "discord", appdefinition.DiscordThread)
	body := appSetupDiscordBody(t, handler, project, "111")
	path := appSetupPath(t, project, app)
	for _, config := range []map[string]any{
		{"shard_count": 1},
		{"public_key": "bad"},
		{"credentials": "must-not-persist"},
	} {
		body["provider_config"] = config
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			path,
			projectAppHTTPJSON(t, body),
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}
	body["provider_config"] = map[string]any{
		"public_key": strings.Repeat("ab", 32),
	}
	first := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	body["expected_setup_revision"] = first["setup_revision"]
	body["provider_config"] = map[string]any{"public_key": strings.Repeat("cd", 32)}
	second := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	require.Greater(
		t,
		testutil.RequireType[float64](t, second["setup_revision"]),
		testutil.RequireType[float64](t, first["setup_revision"]),
	)
	body["expected_setup_revision"] = second["setup_revision"]
	body["provider_account_ref"] = "999"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	// Slack can only establish or rotate credentials through its verified OAuth exchange.
	slackApp := createSetupHTTPApp(t, handler, project, "slack", appdefinition.SlackThread)
	body["expected_setup_revision"] = slackApp.SetupRevision
	rejected := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		appSetupPath(t, project, slackApp),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	require.Contains(t, projectAppHTTPJSON(t, rejected), "OAuth")
	// Reusing the same physical bot is independent setup, not an upsert.
	duplicate := createSetupHTTPApp(t, handler, project, "another-discord", appdefinition.DiscordThread)
	body["expected_setup_revision"], body["provider_account_ref"] = duplicate.SetupRevision, "111"
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		appSetupPath(t, project, duplicate),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	require.Equal(t, testPublicID(t, publicid.KindProjectApp, duplicate.ID), created["id"])
}

func TestProjectAppSetupCredentialScopeAndKind(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(
		openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(appSetupDiscordConfig(t)),
	)
	project := bootstrapPublicHTTPProject(t, handler, "app-credential-scope")
	app := createSetupHTTPApp(t, handler, project, "discord", appdefinition.DiscordThread)
	other := projectAppHTTPSecondProject(t, handler, project)
	foreign := bootstrapPublicHTTPProject(t, handler, "app-credential-foreign")
	for _, owner := range []publicHTTPProject{other, foreign} {
		body := appSetupDiscordBody(t, handler, owner, "111")
		requestJSONWithHeaders(t, handler, http.MethodPost, appSetupPath(t, project, app),
			projectAppHTTPJSON(t, body), "", http.StatusNotFound, authHeaders(project.AdminToken))
	}
	body := appSetupHTTPBody("111", "111")
	body["credential_secret_id"] = testPublicID(t, publicid.KindSecret, uuid.New())
	requestJSONWithHeaders(t, handler, http.MethodPost, appSetupPath(t, project, app),
		projectAppHTTPJSON(t, body), "", http.StatusNotFound, authHeaders(project.AdminToken))
	// An available secret still needs the exact credential kind for this app.
	body["credential_secret_id"] = createAppSetupHTTPSecret(
		t,
		handler,
		project,
		"wrong-kind",
		map[string]any{
			"kind":              "aws_credentials",
			"access_key_id":     "access",
			"secret_access_key": "secret",
		},
	)
	requestJSONWithHeaders(t, handler, http.MethodPost, appSetupPath(t, project, app),
		projectAppHTTPJSON(t, body), "", http.StatusBadRequest, authHeaders(project.AdminToken))
	current, err := project.Store.Integrations().
		GetProjectApp(t.Context(), project.ProjectUUID, app.ID)
	require.NoError(t, err)
	require.Equal(t, app, current, "rejected credentials must leave the saved app unchanged")
}

func createAppSetupHTTPSecret(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	name string,
	material map[string]any,
) string {
	t.Helper()
	secret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		projectAppHTTPJSON(
			t,
			map[string]any{
				"name":     name,
				"owner":    map[string]any{"kind": "project", "project_id": project.ProjectID},
				"material": material,
			},
		),
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	return testutil.RequireType[string](t, secret["id"])
}

func createSlackHTTPApp(
	t *testing.T,
	ctx context.Context,
	project publicHTTPProject,
	appID, workspaceID, displayName string,
) integrationstore.ProjectAppRecord {
	t.Helper()
	payload, err := slack.CredentialPayload(
		slack.AppCredentials{
			BotToken:      "xoxb-" + appID,
			ClientID:      "client-" + appID,
			ClientSecret:  "secret-" + appID,
			SigningSecret: "signing-" + appID,
		},
	)
	require.NoError(t, err)
	credential := createSlackHTTPInstallSecret(t, ctx, project, appID+"-credentials", payload)
	secret, err := project.Store.Secrets().GetSecret(ctx, project.OrgUUID, credential)
	require.NoError(t, err)
	app, err := project.Store.Integrations().
		CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
			OrgID:     project.OrgUUID,
			ProjectID: project.ProjectUUID,
			Name:      "slack-" + uuid.NewString()[:8],
			AppType:   appdefinition.SlackThread,
		})
	require.NoError(t, err)
	app, err = project.Store.Integrations().
		ConfigureProjectApp(ctx, integrationstore.ConfigureProjectAppInput{
			OrgID:                    project.OrgUUID,
			ProjectID:                project.ProjectUUID,
			AppID:                    app.ID,
			ExpectedSetupRevision:    app.SetupRevision,
			InstalledByUserID:        project.AdminUserUUID,
			Provider:                 "slack",
			ProviderTenantID:         workspaceID,
			ProviderAccountRef:       appID,
			ProviderAgentDisplayName: displayName,
			CredentialSecretID:       credential,
			CredentialVersionID:      secret.CurrentVersionID,
			OAuthFlowID:              uuid.Must(uuid.NewV7()),
			ProviderIdentity:         json.RawMessage(`{"bot_user_id":"U_BOT"}`),
		})
	require.NoError(t, err)
	return app
}

// Each local token identifies one application/bot pair, allowing CRUD coverage
// to exercise provider identity verification without contacting Discord.
func appSetupDiscordConfig(t *testing.T) discord.Config {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bot ")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v10/users/@me":
			fmt.Fprintf(w, `{"id":%q,"bot":true}`, id)
		case "/api/v10/applications/@me":
			fmt.Fprintf(w, `{"id":%q}`, id)
		default:
			t.Errorf("unexpected Discord request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	return discord.Config{APIURL: provider.URL + "/api/v10", HTTPClient: provider.Client()}
}

func appSetupDiscordBody(
	t *testing.T, handler http.Handler, project publicHTTPProject, id string,
) map[string]any {
	t.Helper()
	secret := createAppSetupHTTPSecret(t, handler, project, "discord-"+id,
		map[string]any{"kind": "generic", "value": id})
	body := appSetupHTTPBody(id, id)
	body["credential_secret_id"] = secret
	return body
}

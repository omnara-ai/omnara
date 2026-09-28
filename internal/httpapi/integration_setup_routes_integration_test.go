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
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func integrationSetupHTTPBody(tenant, account string) map[string]any {
	return map[string]any{
		"provider_tenant_id":      tenant,
		"provider_account_ref":    account,
		"expected_setup_revision": int64(1),
	}
}

func createSetupHTTPIntegration(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	name string,
	integrationKind integrationdefinition.Kind,
) integrationstore.IntegrationRecord {
	t.Helper()
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/integrations",
		integrationHTTPJSON(
			t,
			map[string]any{"name": name, "integration_kind": integrationKind, "settings": map[string]any{}},
		),
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	id := mustPublicHTTPID(
		t,
		publicid.KindIntegration,
		testutil.RequireType[string](t, response["id"]),
	)
	integration, err := project.Store.Integrations().GetIntegration(t.Context(), project.ProjectUUID, id)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationStateDisconnected, integration.State)
	require.Equal(t, uuid.Nil, integration.CredentialSecretID)
	return integration
}

func integrationSetupPath(
	t *testing.T,
	project publicHTTPProject,
	integration integrationstore.IntegrationRecord,
) string {
	t.Helper()
	return project.ProjectPath + "/integrations/" + testPublicID(
		t,
		publicid.KindIntegration,
		integration.ID,
	) + "/setup"
}

func TestIntegrationCredentialSetupAndDisconnectAuthorization(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(
		openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(integrationSetupDiscordConfig(t)),
	)
	project := bootstrapPublicHTTPProject(t, handler, "integration-setup-auth")
	integration := createSetupHTTPIntegration(t, handler, project, "discord", integrationdefinition.DiscordThread)
	body := integrationSetupDiscordBody(t, handler, project, "111")
	path := integrationSetupPath(t, project, integration)
	encoded := integrationHTTPJSON(t, body)
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
	other := bootstrapPublicHTTPProject(t, handler, "integration-setup-foreign")
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
	require.NotContains(t, integrationHTTPJSON(t, configured), "bot_token")
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
	require.Equal(
		t,
		"conflict: integration setup changed; refresh the integration and start setup again",
		conflict["error"],
	)
	body["expected_setup_revision"] = disconnected["setup_revision"]
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		integrationHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
}

func TestIntegrationSetupValidationAndConfigChanges(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(
		openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(integrationSetupDiscordConfig(t)),
	)
	project := bootstrapPublicHTTPProject(t, handler, "integration-setup-validation")
	integration := createSetupHTTPIntegration(t, handler, project, "discord", integrationdefinition.DiscordThread)
	body := integrationSetupDiscordBody(t, handler, project, "111")
	path := integrationSetupPath(t, project, integration)
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
			integrationHTTPJSON(t, body),
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
		integrationHTTPJSON(t, body),
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
		integrationHTTPJSON(t, body),
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
		integrationHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	slackIntegration := createSetupHTTPIntegration(t, handler, project, "slack", integrationdefinition.SlackThread)
	body["expected_setup_revision"] = slackIntegration.SetupRevision
	rejected := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		integrationSetupPath(t, project, slackIntegration),
		integrationHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	require.Contains(t, integrationHTTPJSON(t, rejected), "OAuth")
	duplicate := createSetupHTTPIntegration(t, handler, project, "another-discord", integrationdefinition.DiscordThread)
	body["expected_setup_revision"], body["provider_account_ref"] = duplicate.SetupRevision, "111"
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		integrationSetupPath(t, project, duplicate),
		integrationHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	require.Equal(t, testPublicID(t, publicid.KindIntegration, duplicate.ID), created["id"])
}

func TestIntegrationSetupCredentialScopeAndKind(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(
		openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(integrationSetupDiscordConfig(t)),
	)
	project := bootstrapPublicHTTPProject(t, handler, "integration-credential-scope")
	integration := createSetupHTTPIntegration(t, handler, project, "discord", integrationdefinition.DiscordThread)
	other := integrationHTTPSecondProject(t, handler, project)
	foreign := bootstrapPublicHTTPProject(t, handler, "integration-credential-foreign")
	for _, owner := range []publicHTTPProject{other, foreign} {
		body := integrationSetupDiscordBody(t, handler, owner, "111")
		requestJSONWithHeaders(t, handler, http.MethodPost, integrationSetupPath(t, project, integration),
			integrationHTTPJSON(t, body), "", http.StatusNotFound, authHeaders(project.AdminToken))
	}
	body := integrationSetupHTTPBody("111", "111")
	body["credential_secret_id"] = testPublicID(t, publicid.KindSecret, uuid.New())
	requestJSONWithHeaders(t, handler, http.MethodPost, integrationSetupPath(t, project, integration),
		integrationHTTPJSON(t, body), "", http.StatusNotFound, authHeaders(project.AdminToken))
	body["credential_secret_id"] = createIntegrationSetupHTTPSecret(
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
	requestJSONWithHeaders(t, handler, http.MethodPost, integrationSetupPath(t, project, integration),
		integrationHTTPJSON(t, body), "", http.StatusBadRequest, authHeaders(project.AdminToken))
	current, err := project.Store.Integrations().
		GetIntegration(t.Context(), project.ProjectUUID, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration, current, "rejected credentials must leave the saved integration unchanged")
}

func createIntegrationSetupHTTPSecret(
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
		integrationHTTPJSON(
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

func createSlackHTTPIntegration(
	t *testing.T,
	ctx context.Context,
	project publicHTTPProject,
	appID, workspaceID, displayName string,
) integrationstore.IntegrationRecord {
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
	integration, err := project.Store.Integrations().
		CreateIntegration(ctx, integrationstore.SaveIntegrationInput{
			OrgID:           project.OrgUUID,
			ProjectID:       project.ProjectUUID,
			Name:            "slack-" + uuid.NewString()[:8],
			IntegrationKind: integrationdefinition.SlackThread,
		})
	require.NoError(t, err)
	integration, err = project.Store.Integrations().
		ConfigureIntegration(ctx, integrationstore.ConfigureIntegrationInput{
			OrgID:                    project.OrgUUID,
			ProjectID:                project.ProjectUUID,
			IntegrationID:            integration.ID,
			ExpectedSetupRevision:    integration.SetupRevision,
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
	return integration
}

func integrationSetupDiscordConfig(t *testing.T) discord.Config {
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

func integrationSetupDiscordBody(
	t *testing.T, handler http.Handler, project publicHTTPProject, id string,
) map[string]any {
	t.Helper()
	secret := createIntegrationSetupHTTPSecret(t, handler, project, "discord-"+id,
		map[string]any{"kind": "generic", "value": id})
	body := integrationSetupHTTPBody(id, id)
	body["credential_secret_id"] = secret
	return body
}

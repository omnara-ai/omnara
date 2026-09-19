//go:build integration

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func connectionHTTPBody(provider, tenant, account string) map[string]any {
	return map[string]any{"provider": provider, "provider_tenant_id": tenant, "provider_account_ref": account}
}

func TestIntegrationConnectionHTTPCRUDAndPagination(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(
		openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(connectionCRUDDiscordConfig(t)),
	)
	project := bootstrapPublicHTTPProject(t, handler, "connection-crud")
	path := project.ProjectPath + "/integration-connections"
	headers := authHeaders(project.AdminToken)
	body := connectionCRUDDiscordBody(t, handler, project, "111")
	alphaSecret := body["credential_secret_id"]
	body["provider_agent_display_name"] = "Alpha"
	alpha := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	id := testutil.RequireType[string](t, alpha["id"])
	mustPublicHTTPID(t, publicid.KindIntegrationConnection, id)
	require.Equal(t, project.ProjectID, alpha["project_id"])
	require.Equal(t, "active", alpha["state"])
	require.Equal(t, map[string]any{"shard_count": float64(1)}, alpha["provider_config"])
	require.Equal(t, alphaSecret, alpha["credential_secret_id"])
	for _, removed := range []string{
		"agent_profile_id",
		"agent_id",
		"integration_kind",
		"connection_mode",
		"provider_identity",
		"provider_metadata",
	} {
		require.NotContains(t, alpha, removed)
	}
	require.Equal(
		t,
		alpha,
		requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+id, "", "", http.StatusOK, headers),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusConflict,
		headers,
	)
	body = connectionCRUDDiscordBody(t, handler, project, "222")
	body["provider_agent_display_name"] = "Beta"
	beta := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	page := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=1", "", "", http.StatusOK, headers)
	data := testutil.RequireType[[]any](t, page["data"])
	require.Len(t, data, 1)
	require.Equal(t, beta["id"], testutil.RequireType[map[string]any](t, data[0])["id"])
	cursor := testutil.RequireType[string](t, page["next_cursor"])
	last := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"?limit=1&cursor="+url.QueryEscape(cursor),
		"",
		"",
		http.StatusOK,
		headers,
	)
	require.Len(t, testutil.RequireType[[]any](t, last["data"]), 1)
	require.Equal(t, id, testutil.RequireType[map[string]any](t, testutil.RequireType[[]any](t, last["data"])[0])["id"])
	require.Nil(t, last["next_cursor"])
	named := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?name=Beta*", "", "", http.StatusOK, headers)
	require.Len(t, testutil.RequireType[[]any](t, named["data"]), 1)
	for _, query := range []string{"?limit=0", "?cursor=invalid", "?oauth_flow_id=invalid", "?agent_profile_id=removed"} {
		requestJSONWithHeaders(t, handler, http.MethodGet, path+query, "", "", http.StatusBadRequest, headers)
	}
	// Account identity is immutable even when the caller manages both accounts.
	rejected := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+id,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		headers,
	)
	require.Contains(t, rejected["error"], "immutable")
	body["provider_tenant_id"], body["provider_account_ref"] = "111", "111"
	body["credential_secret_id"] = alphaSecret
	body["provider_agent_display_name"], body["state"] = "Renamed", "disabled"
	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+id,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(t, id, updated["id"])
	require.Equal(t, alpha["created_at"], updated["created_at"])
	require.Equal(t, "disabled", updated["state"])
	require.Equal(t, "Renamed", updated["provider_agent_display_name"])
	body["state"] = "active"
	// Provider display labels can exceed resource-name length and can be cleared.
	for _, label := range []string{strings.Repeat("x", 100), ""} {
		body["provider_agent_display_name"] = label
		edited := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			path+"/"+id,
			projectAppHTTPJSON(t, body),
			"",
			http.StatusOK,
			headers,
		)
		require.Equal(t, label, edited["provider_agent_display_name"])
	}
	requestJSONWithHeaders(t, handler, http.MethodDelete, path+"/"+id, "", "", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+id, "", "", http.StatusNotFound, headers)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+id,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	replacement := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	require.NotEqual(t, id, replacement["id"])
}

func TestIntegrationConnectionHTTPAuthorizationAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool, WithDiscordClientConfig(connectionCRUDDiscordConfig(t)))
	project := bootstrapPublicHTTPProject(t, handler, "connection-auth")
	path := project.ProjectPath + "/integration-connections"
	body := projectAppHTTPJSON(t, connectionCRUDDiscordBody(t, handler, project, "111"))
	saved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		body,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	id := testutil.RequireType[string](t, saved["id"])
	operations := []struct{ method, path, body string }{
		{http.MethodGet, path, ""},
		{http.MethodGet, path + "/" + id, ""},
		{http.MethodPost, path, body},
		{http.MethodPut, path + "/" + id, body},
		{http.MethodDelete, path + "/" + id, ""},
	}
	_, unassigned := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "connection-unassigned")
	for _, op := range operations {
		requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", http.StatusUnauthorized, nil)
		requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", http.StatusNotFound, authHeaders(unassigned))
	}
	for _, role := range []string{"viewer", "operator"} {
		user, token := createHTTPOrgMemberToken(t, ctx, pool, project.Store, project.OrgUUID, "connection-"+role)
		_, err := project.Store.Identity().AddProjectMembership(
			ctx,
			identitystore.AddProjectMembershipInput{
				OrgID:     project.OrgUUID,
				ProjectID: project.ProjectUUID,
				UserID:    user.ID,
				Role:      role,
			},
		)
		require.NoError(t, err)
		for _, op := range operations {
			status := http.StatusForbidden
			if op.method == http.MethodGet {
				status = http.StatusOK
			}
			response := requestJSONWithHeaders(t, handler, op.method, op.path, op.body, "", status, authHeaders(token))
			if status == http.StatusForbidden {
				require.Equal(t, "forbidden", response["code"])
			}
		}
	}
	second := projectAppHTTPSecondProject(t, handler, project)
	otherPath := second.ProjectPath + "/integration-connections"
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		requestBody := ""
		if method == http.MethodPut {
			requestBody = body
		}
		requestJSONWithHeaders(
			t,
			handler,
			method,
			otherPath+"/"+id,
			requestBody,
			"",
			http.StatusNotFound,
			authHeaders(project.AdminToken),
		)
	}
	// A second project needs its own hosted identity and credential.
	otherBody := projectAppHTTPJSON(t, connectionCRUDDiscordBody(t, handler, second, "333"))
	other := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		otherPath,
		otherBody,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	require.NotEqual(t, id, other["id"])
	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		otherPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	require.Len(t, testutil.RequireType[[]any](t, listed["data"]), 1)
}

func TestIntegrationConnectionHTTPGitHubCredentials(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()), WithGitHubClientConfig(githubSetupTestConfig(t)))
	project := bootstrapPublicHTTPProject(t, handler, "connection-github")
	other := projectAppHTTPSecondProject(t, handler, project)
	path := project.ProjectPath + "/integration-connections"
	headers := authHeaders(project.AdminToken)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	material := map[string]any{
		"kind":           "github_app_credentials",
		"app_id":         "123",
		"private_key":    privateKey,
		"webhook_secret": "private-webhook-secret",
	}
	secret := createConnectionHTTPSecret(t, handler, other, "github-credential", material)
	body := connectionHTTPBody("github", "00123", "00456")
	body["credential_secret_id"] = secret
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	secretPath := "/api/v1/orgs/" + project.OrgID + "/secrets/" + secret
	grant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		secretPath+"/grants",
		projectAppHTTPJSON(t, map[string]any{"target_project_id": project.ProjectID}),
		"",
		http.StatusCreated,
		headers,
	)
	body["provider_tenant_id"] = "124"
	rejected := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		headers,
	)
	require.Contains(t, rejected["error"], "App ID")
	body["provider_tenant_id"] = "00123"
	saved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	require.Equal(t, "123", saved["provider_tenant_id"])
	require.Equal(t, "456", saved["provider_account_ref"])
	require.Equal(t, secret, saved["credential_secret_id"])
	for _, sensitive := range []string{privateKey, "private-webhook-secret", "private_key"} {
		require.NotContains(t, projectAppHTTPJSON(t, saved), sensitive)
	}
	body["provider_tenant_id"], body["provider_account_ref"] = "123", "456"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusConflict,
		headers,
	)
	body["provider_account_ref"] = "789"
	wrongKind := createConnectionHTTPSecret(
		t,
		handler,
		project,
		"wrong-kind",
		map[string]any{"kind": "generic", "value": "private-token"},
	)
	body["credential_secret_id"] = wrongKind
	rejected = requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		headers,
	)
	require.Contains(t, rejected["error"], "kind")
	material["private_key"] = "invalid-pem"
	invalidKey := createConnectionHTTPSecret(t, handler, project, "invalid-key", material)
	body["credential_secret_id"] = invalidKey
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		headers,
	)
	body["credential_secret_id"] = secret
	body["provider_config"] = map[string]any{"api_url": "https://example.com"}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusBadRequest,
		headers,
	)
	delete(body, "provider_config")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		secretPath+"/grants/"+testutil.RequireType[string](t, grant["id"]),
		"",
		"",
		http.StatusNoContent,
		headers,
	)
	body["provider_account_ref"] = "456"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		path+"/"+testutil.RequireType[string](t, saved["id"]),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	// Revoked credentials cannot make a connection undeletable.
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		path+"/"+testutil.RequireType[string](t, saved["id"]),
		"",
		"",
		http.StatusNoContent,
		headers,
	)
}

func TestIntegrationConnectionHTTPDiscordAndValidation(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()), WithDiscordClientConfig(discordSetupTestConfig(t)))
	project := bootstrapPublicHTTPProject(t, handler, "connection-discord")
	path := project.ProjectPath + "/integration-connections"
	headers := authHeaders(project.AdminToken)
	secret := createConnectionHTTPSecret(
		t,
		handler,
		project,
		"discord-token",
		map[string]any{"kind": "generic", "value": "private-discord-token"},
	)
	body := connectionHTTPBody("discord", "111", "222")
	body["credential_secret_id"] = secret
	body["provider_config"] = map[string]any{"public_key": strings.Repeat("AB", 32)}
	saved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	require.Equal(
		t,
		map[string]any{"public_key": strings.Repeat("ab", 32), "shard_count": float64(1)},
		saved["provider_config"],
	)
	require.NotContains(t, projectAppHTTPJSON(t, saved), "private-discord-token")

	connectionPath := path + "/" + testutil.RequireType[string](t, saved["id"])
	body["provider_config"] = map[string]any{"public_key": strings.Repeat("AB", 32), "shard_count": 4096}
	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		connectionPath,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(
		t,
		map[string]any{"public_key": strings.Repeat("ab", 32), "shard_count": float64(4096)},
		updated["provider_config"],
	)
	oldTime, err := time.Parse(time.RFC3339Nano, testutil.RequireType[string](t, saved["updated_at"]))
	require.NoError(t, err)
	newTime, err := time.Parse(time.RFC3339Nano, testutil.RequireType[string](t, updated["updated_at"]))
	require.NoError(t, err)
	require.True(
		t,
		newTime.After(oldTime),
		"topology edits must advance the connection revision used to fence old workers",
	)
	require.Equal(
		t,
		updated,
		requestJSONWithHeaders(t, handler, http.MethodGet, connectionPath, "", "", http.StatusOK, headers),
	)
	for _, badCount := range []any{0, -1, 4097, 1.5, 1e100, nil, true, "2", []any{}, map[string]any{}} {
		body["provider_config"] = map[string]any{"public_key": strings.Repeat("AB", 32), "shard_count": badCount}
		rejected := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			connectionPath,
			projectAppHTTPJSON(t, body),
			"",
			http.StatusBadRequest,
			headers,
		)
		require.Equal(t, "invalid_request", rejected["code"])
		require.Contains(t, rejected["error"], "shard_count")
	}
	require.Equal(
		t,
		updated,
		requestJSONWithHeaders(t, handler, http.MethodGet, connectionPath, "", "", http.StatusOK, headers),
		"invalid counts must leave the configuration and revision unchanged",
	)
	delete(body, "provider_config")
	reset := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		connectionPath,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(t, map[string]any{"shard_count": float64(1)}, reset["provider_config"])
	slackCreate := connectionHTTPBody("slack", "T123", "A123")
	slackCreate["credential_secret_id"] = secret
	for _, bad := range []map[string]any{
		slackCreate,
		connectionHTTPBody("github", "123", "456"),
		connectionHTTPBody("discord", "111", "222"),
		{
			"provider":             "unsupported",
			"provider_tenant_id":   "tenant",
			"provider_account_ref": "router",
			"credential_secret_id": secret,
		},
		{"provider": "discord", "provider_tenant_id": "0111", "provider_account_ref": "222", "credential_secret_id": secret},
		{
			"provider":             "discord",
			"provider_tenant_id":   "111",
			"provider_account_ref": "333",
			"credential_secret_id": secret,
			"provider_config":      map[string]any{"public_key": "bad"},
		},
		{
			"provider":             "discord",
			"provider_tenant_id":   "111",
			"provider_account_ref": "333",
			"credential_secret_id": secret,
			"provider_config":      map[string]any{"bot_token": "copied-secret"},
		},
		{
			"provider": "discord", "provider_tenant_id": "111", "provider_account_ref": "222",
			"credential_secret_id": secret, "agent_profile_id": "removed",
		},
	} {
		rejected := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			path,
			projectAppHTTPJSON(t, bad),
			"",
			http.StatusBadRequest,
			headers,
		)
		if _, hasCredential := bad["credential_secret_id"]; !hasCredential {
			require.Equal(t, "validation_failed", rejected["code"])
			require.Contains(t, projectAppHTTPJSON(t, rejected), "credential_secret_id")
		} else if bad["provider"] == "unsupported" {
			require.Equal(t, "validation_failed", rejected["code"])
		} else if _, removed := bad["agent_profile_id"]; removed {
			require.Equal(t, "validation_failed", rejected["code"])
			require.Contains(t, projectAppHTTPJSON(t, rejected), "agent_profile_id")
		} else {
			require.Equal(t, "invalid_request", rejected["code"])
			if bad["provider"] == "slack" {
				require.Contains(t, projectAppHTTPJSON(t, rejected), "create Slack connections through OAuth setup")
			}
		}
		require.NotEmpty(t, rejected["error"])
	}
}

func createConnectionHTTPSecret(
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

func createSlackHTTPConnection(
	t *testing.T,
	ctx context.Context,
	project publicHTTPProject,
	appID,
	workspaceID,
	displayName string,
) integrationstore.IntegrationConnectionRecord {
	t.Helper()
	payload, err := slack.CredentialPayload(
		slack.AppCredentials{
			BotToken:      "xoxb-" + appID,
			ClientID:      "client-id-" + appID,
			ClientSecret:  "client-secret-" + appID,
			SigningSecret: "signing-secret-" + appID,
		},
	)
	require.NoError(t, err)
	credential := createSlackHTTPInstallSecret(t, ctx, project, appID+"-credentials", payload)
	connection, err := project.Store.Integrations().CreateIntegrationConnection(
		ctx,
		integrationstore.SaveIntegrationConnectionInput{
			OrgID:                    project.OrgUUID,
			ProjectID:                project.ProjectUUID,
			InstalledByUserID:        project.AdminUserUUID,
			Provider:                 integrationstore.IntegrationProviderSlack,
			State:                    integrationstore.IntegrationConnectionStateActive,
			ProviderTenantID:         workspaceID,
			ProviderAccountRef:       appID,
			ProviderAgentDisplayName: displayName,
			CredentialSecretID:       credential,
		},
	)
	require.NoError(t, err)
	return connection
}

// Each local token identifies one application/bot pair, allowing CRUD coverage
// to exercise provider identity verification without contacting Discord.
func connectionCRUDDiscordConfig(t *testing.T) discord.Config {
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

func connectionCRUDDiscordBody(
	t *testing.T, handler http.Handler, project publicHTTPProject, id string,
) map[string]any {
	t.Helper()
	secret := createConnectionHTTPSecret(t, handler, project, "discord-"+id,
		map[string]any{"kind": "generic", "value": id})
	body := connectionHTTPBody("discord", id, id)
	body["credential_secret_id"] = secret
	return body
}

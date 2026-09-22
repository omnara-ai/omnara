//go:build integration

package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

const disconnectedSlackEvent = `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
	`"event_id":"Ev-disconnected","authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],` +
	`"event":{"type":"app_mention","user":"U123","text":"<@U_BOT> help","channel":"C123","ts":"111.222"}}`

func TestSlackDisconnectedEventsVerifyRetainedCredentialsAndReconnect(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	provider := newSlackEventsTestServer(t)
	t.Cleanup(provider.Close)
	f := newSlackEventsIntegrationFixture(t, ctx, pool, provider, "disconnected-events")
	apps := f.Project.Store.Integrations()
	applied, err := apps.DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: f.Install.ProjectID, AppID: f.Install.ID, ExpectedSetupRevision: &f.Install.SetupRevision,
	})
	require.NoError(t, err)
	require.True(t, applied)
	disconnected, err := apps.GetProjectApp(ctx, f.Install.ProjectID, f.Install.ID)
	require.NoError(t, err)
	require.Equal(t, f.Install.CredentialSecretID, disconnected.CredentialSecretID)
	require.Equal(t, f.Install.SetupRevision+1, disconnected.SetupRevision)
	request := func(body, signingSecret string, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "", status,
			unitSlackSignedHeaders(body, signingSecret))
	}
	assertReceipts := func(want int) {
		t.Helper()
		var count int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.Install.ID).Scan(&count))
		require.Equal(t, want, count)
	}
	request(disconnectedSlackEvent, "wrong-signing-secret", http.StatusUnauthorized)
	require.Equal(t, "ignored", request(disconnectedSlackEvent, "signing-secret", http.StatusOK)["ok"])
	assertReceipts(0)
	unchanged, err := apps.GetProjectApp(ctx, disconnected.ProjectID, disconnected.ID)
	require.NoError(t, err)
	require.Equal(t, disconnected, unchanged, "acknowledging an event must not mutate the disconnected setup")

	payload, err := slack.CredentialPayload(slack.AppCredentials{
		BotToken: "xoxb-reconnected", ClientID: "client", ClientSecret: "client-secret", SigningSecret: "new-signing-secret",
	})
	require.NoError(t, err)
	secretID := createSlackHTTPInstallSecret(t, ctx, f.Project, "reconnected-credentials", payload)
	secret, err := f.Project.Store.Secrets().GetSecret(ctx, f.Install.OrgID, secretID)
	require.NoError(t, err)
	setup := integrationstore.ConfigureProjectAppInput{
		OrgID: f.Install.OrgID, ProjectID: f.Install.ProjectID, AppID: f.Install.ID,
		ExpectedSetupRevision: f.Install.SetupRevision, InstalledByUserID: f.Project.AdminUserUUID,
		Provider: "slack", ProviderTenantID: "T123", ProviderAccountRef: "A123",
		CredentialSecretID: secret.ID, CredentialVersionID: secret.CurrentVersionID,
		ProviderIdentity: disconnected.ProviderIdentity, OAuthFlowID: uuid.Must(uuid.NewV7()),
	}
	_, err = apps.ConfigureProjectApp(ctx, setup)
	require.ErrorIs(t, err, storeerr.ErrConflict, "stale setup must not change which signature is trusted")
	request(disconnectedSlackEvent, "new-signing-secret", http.StatusUnauthorized)
	require.Equal(t, "ignored", request(disconnectedSlackEvent, "signing-secret", http.StatusOK)["ok"])
	setup.ExpectedSetupRevision = disconnected.SetupRevision
	reconnected, err := apps.ConfigureProjectApp(ctx, setup)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ProjectAppStateActive, reconnected.State)
	require.Equal(t, disconnected.SetupRevision+1, reconnected.SetupRevision)
	request(disconnectedSlackEvent, "signing-secret", http.StatusUnauthorized)
	assertReceipts(0)
	for range 2 {
		require.Equal(t, "received", request(disconnectedSlackEvent, "new-signing-secret", http.StatusOK)["ok"])
	}
	assertReceipts(1)
	var captured []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT payload FROM integration_inbox WHERE app_id=$1`, f.Install.ID).Scan(&captured))
	require.Equal(t, disconnectedSlackEvent, string(captured), "ignored delivery must not consume the inbox dedup key")
	require.NoError(t, apps.DeleteProjectApp(ctx, f.Install.OrgID, f.Install.ProjectID, f.Install.ID))
	request(strings.ReplaceAll(disconnectedSlackEvent, "Ev-disconnected", "Ev-deleted"),
		"new-signing-secret", http.StatusUnauthorized)
	assertReceipts(1)
}

func TestSlackDisconnectedSiblingCannotAuthorizeIntakeOrPoisonActiveApp(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	provider := newSlackEventsTestServer(t)
	t.Cleanup(provider.Close)
	f := newSlackEventsIntegrationFixture(t, ctx, pool, provider, "disconnected-sibling")
	second := projectAppHTTPSecondProject(t, f.Handler, f.Project)
	profile := createSlackReadyHTTPProfile(t, f.Handler, second, "second-profile", second.AdminToken)
	profileID := mustPublicHTTPID(t, publicid.KindAgentProfile, testutil.RequireType[string](t, profile["id"]))
	different := createSlackHTTPInstall(t, ctx, second, profileID, "A123", "T123", "U_BOT", "disconnected-signing-secret")
	same := createSlackHTTPInstall(t, ctx, second, profileID, "A123", "T123", "U_BOT", "signing-secret")
	apps := f.Project.Store.Integrations()
	for _, app := range []integrationstore.ProjectAppRecord{different, same} {
		applied, err := apps.DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
			ProjectID: app.ProjectID, AppID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
		})
		require.NoError(t, err)
		require.True(t, applied)
	}
	active, err := apps.ListProjectAppsByProviderIdentity(ctx, "slack", "T123", "A123", uuid.Nil, 100)
	require.NoError(t, err)
	require.Len(t, active, 1, "ordinary ingress lookup remains active-only")
	require.Equal(t, f.Install.ID, active[0].ID)
	tenant, err := apps.ListProjectAppsByProviderTenant(ctx, "slack", "T123", uuid.Nil, 100)
	require.NoError(t, err)
	require.Equal(t, active, tenant, "tenant lookup must also retain active-only semantics")
	var candidateIDs []uuid.UUID
	after := uuid.Nil
	for {
		page, err := apps.ListProjectAppsForProviderEventVerification(ctx, "slack", "T123", "A123", after, 1)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.Len(t, page, 1)
		require.Greater(t, page[0].ID.String(), after.String())
		after = page[0].ID
		candidateIDs = append(candidateIDs, after)
	}
	require.ElementsMatch(t, []uuid.UUID{f.Install.ID, different.ID, same.ID}, candidateIDs)
	request := func(body, signingSecret string, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "", status,
			unitSlackSignedHeaders(body, signingSecret))
	}
	assertReceipts := func(activeCount int) {
		t.Helper()
		for _, app := range []integrationstore.ProjectAppRecord{f.Install, different, same} {
			var count int
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, app.ID).Scan(&count))
			want := 0
			if app.ID == f.Install.ID {
				want = activeCount
			}
			require.Equal(t, want, count, "each app must verify its own credentials and be active before admission")
		}
	}
	request(disconnectedSlackEvent, "wrong-signing-secret", http.StatusUnauthorized)
	require.Equal(t, "ignored", request(disconnectedSlackEvent, "disconnected-signing-secret", http.StatusOK)["ok"])
	assertReceipts(0)
	require.Equal(t, "received", request(disconnectedSlackEvent, "signing-secret", http.StatusOK)["ok"])
	assertReceipts(1)

	var version uuid.UUID
	var wrapped []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT version.id, version.encrypted_dek FROM secret_versions version
		JOIN secrets secret ON secret.current_version_id=version.id WHERE secret.id=$1`, different.CredentialSecretID).
		Scan(&version, &wrapped))
	require.NotEmpty(t, wrapped)
	corrupt := append([]byte(nil), wrapped...)
	corrupt[0] ^= 1
	_, err = pool.Exec(ctx, `UPDATE secret_versions SET encrypted_dek=$2 WHERE id=$1`, version, corrupt)
	require.NoError(t, err)
	body := strings.ReplaceAll(disconnectedSlackEvent, "Ev-disconnected", "Ev-unwrap-failure")
	var logs bytes.Buffer
	r := httptest.NewRequest(http.MethodPost, integrationEventsPath, strings.NewReader(body))
	r = r.WithContext(log.WithLogger(ctx, slog.New(slog.NewJSONHandler(&logs, nil))))
	for key, value := range unitSlackSignedHeaders(body, "signing-secret") {
		r.Header.Set(key, value)
	}
	server := &Server{store: f.Project.Store}
	response := performRequest(http.HandlerFunc(server.integrationEventsRoute), r)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"ok":"received"}`, response.Body.String())
	assertReceipts(2)
	require.Contains(t, logs.String(), "provider event app failed")
	require.Contains(t, logs.String(), different.ID.String())
	require.Contains(t, logs.String(), `"app_state":"disconnected"`)
	require.Contains(t, logs.String(), `"stage":"credential_verification"`)
	require.Contains(t, logs.String(), `"retryable":true`)
	require.NotContains(t, logs.String(), "disconnected-signing-secret")
	request(body, "disconnected-signing-secret", http.StatusServiceUnavailable)
	assertReceipts(2)
	_, err = pool.Exec(ctx, `UPDATE secret_versions SET encrypted_dek=$2 WHERE id=$1`, version, wrapped)
	require.NoError(t, err)
	require.Equal(t, "received", request(body, "signing-secret", http.StatusOK)["ok"])
	require.Equal(t, "ignored", request(body, "disconnected-signing-secret", http.StatusOK)["ok"])
	assertReceipts(2)

	require.NoError(t, pool.QueryRow(ctx, `SELECT version.id, version.encrypted_dek FROM secret_versions version
		JOIN secrets secret ON secret.current_version_id=version.id WHERE secret.id=$1`, f.Install.CredentialSecretID).
		Scan(&version, &wrapped))
	require.NotEmpty(t, wrapped)
	corrupt = append([]byte(nil), wrapped...)
	corrupt[0] ^= 1
	_, err = pool.Exec(ctx, `UPDATE secret_versions SET encrypted_dek=$2 WHERE id=$1`, version, corrupt)
	require.NoError(t, err)
	body = strings.ReplaceAll(disconnectedSlackEvent, "Ev-disconnected", "Ev-active-unwrap-failure")
	request(body, "signing-secret", http.StatusServiceUnavailable)
	assertReceipts(2)
	_, err = pool.Exec(ctx, `UPDATE secret_versions SET encrypted_dek=$2 WHERE id=$1`, version, wrapped)
	require.NoError(t, err)
	require.Equal(t, "received", request(body, "signing-secret", http.StatusOK)["ok"])
	assertReceipts(3)
}

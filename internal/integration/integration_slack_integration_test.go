//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackInboxUsesGrantedCredentialsAndStopsAfterRevocation(t *testing.T) {
	pool, _, ids, integrationID := integrationWorkerFixture(t)
	ctx := t.Context()
	wrapper, err := secrets.NewLocalKeyWrapper(
		"slack-inbox",
		map[string][]byte{"slack-inbox": []byte("0123456789abcdef0123456789abcdef")},
	)
	require.NoError(t, err)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper))
	_, err = pool.Exec(
		ctx,
		`INSERT INTO org_memberships(org_id,user_id,role,created_at) VALUES($1,$2,'owner',now()) ON CONFLICT DO NOTHING`,
		ids.OrgID,
		ids.ProviderAdminUserID,
	)
	require.NoError(t, err)
	ownerProject := uuid.New()
	storagefixture.InsertProject(
		t,
		ctx,
		pool,
		ids.OrgID,
		ownerProject,
		"credential-owner",
		"credential-owner",
		time.Now(),
	)
	actor := identitystore.NewUserPrincipal(ids.ProviderAdminUserID)
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          ids.OrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: ownerProject,
		Name:           "slack-granted",
		Actor:          actor,
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken:   "test-token",
			ClientID:      "client",
			ClientSecret:  "client-secret",
			SigningSecret: "signing-secret",
		},
	})
	require.NoError(t, err)
	grant, err := store.Secrets().
		CreateSecretGrant(
			ctx,
			secretstore.CreateSecretGrantInput{
				OrgID:           ids.OrgID,
				SecretID:        credential.ID,
				TargetProjectID: ids.ProjectID,
				Actor:           actor,
			},
		)
	require.NoError(t, err)
	_, err = pool.Exec(
		ctx,
		`UPDATE project_integrations SET credential_secret_id=$2,provider_identity='{"bot_user_id":"UBOT"}' WHERE id=$1`,
		integrationID,
		credential.ID,
	)
	require.NoError(t, err)
	integrationSetup, err := store.Integrations().GetProjectIntegration(ctx, ids.ProjectID, integrationID)
	require.NoError(t, err)
	_, err = store.Secrets().GetProjectOwnedSecretPayload(ctx, ids.OrgID, ids.ProjectID, credential.ID)
	require.Error(t, err, "this fixture must require the grant-aware read")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth.test" {
			_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
			return
		}
		requests.Add(1)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"display_name":"Alex"}},"channel":{"name":"reviews"}}`))
	}))
	defer server.Close()
	provider := NewSlackIntegrationInboxProvider(
		slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		store.Secrets(),
		store.Integrations(),
		store.Execution(),
	)
	payload := slackInboxTestPayload(
		t,
		slack.Event{Type: "message", Channel: "C123", TS: "1.2", User: "U123", Text: "hello"},
	)
	expanded, err := provider.Expand(ctx, integrationSetup, payload)
	require.NoError(t, err)
	require.Len(t, expanded.Events, 1)
	before := requests.Load()
	require.Positive(t, before)
	_, err = store.Secrets().
		DeleteSecretGrant(
			ctx,
			secretstore.DeleteSecretGrantInput{
				OrgID:    ids.OrgID,
				SecretID: credential.ID,
				GrantID:  grant.ID,
				Actor:    actor,
			},
		)
	require.NoError(t, err)
	_, err = provider.Expand(ctx, integrationSetup, payload)
	require.Error(t, err)
	require.Equal(t, before, requests.Load(), "revoked project grant must prevent every provider request")
}

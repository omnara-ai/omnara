//go:build integration

package executionstore_test

import (
	"testing"

	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookSigningReferenceSerializesWithSecretDeletion(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"webhook", "deletion"} {
		t.Run(first, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "webhook_secret_"+first)
			admin := createIntegrationProjectAdmin(t, ctx, f.Store, "signing-admin@example.com")
			secret, version, err := f.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: testProjectID,
				Name: "signing-key", Actor: userPrincipal(admin.ID),
				Material: secrets.GenericMaterial{Value: "whsec_MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="},
			})
			require.NoError(t, err)
			create := func() (executionstore.EventWebhookRecord, error) {
				return f.Store.Execution().CreateEventWebhook(ctx, executionstore.CreateEventWebhookInput{
					ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{Type: executionstore.EventWebhookScopeProject},
					Settings: webhookconfig.Settings{
						URL: "https://example.com/events", EventTypes: []string{"tool_call_update"}, SigningSecretID: secret.ID,
					},
				})
			}
			deleteSecret := func() (secretstore.SecretRecord, error) {
				return f.Store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
					OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(admin.ID),
				})
			}
			blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
			if first == "webhook" {
				_, err = blocker.Exec(ctx, "LOCK TABLE event_webhooks IN SHARE MODE")
				require.NoError(t, err)
				created := integrationdb.RunAsync(create)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "InsertEventWebhook", 1)
				deleted := integrationdb.RunAsync(deleteSecret)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockSecret", 1)
				require.NoError(t, blocker.Commit(ctx))
				webhook := integrationdb.AwaitSuccess(t, created, "webhook admission before secret deletion")
				require.ErrorIs(t, integrationdb.Await(t, deleted, "deletion after admission").Err, storeerr.ErrConflict)
				require.NoError(t, f.Store.Execution().DeleteEventWebhook(ctx, testProjectID, webhook.ID))
				_, err = deleteSecret()
				require.NoError(t, err, "a deleted webhook no longer uses its signing secret")
				return
			}

			_, err = blocker.Exec(ctx, `SELECT id FROM secret_versions
				WHERE org_id = $1 AND secret_id = $2 AND id = $3 FOR UPDATE`, testOrgID, secret.ID, version.ID)
			require.NoError(t, err)
			deleted := integrationdb.RunAsync(deleteSecret)
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "DeleteSecretVersions", 1)
			created := integrationdb.RunAsync(create)
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockSecretForReference", 1)
			require.NoError(t, blocker.Commit(ctx))
			integrationdb.AwaitSuccess(t, deleted, "secret deletion before webhook admission")
			require.ErrorIs(t, integrationdb.Await(t, created, "admission after deletion").Err, storeerr.ErrNotFound)
		})
	}
}

func TestEventWebhookSigningGrantCanBeRevoked(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "webhook_signing_grant")
	admin := createIntegrationProjectAdmin(t, ctx, f.Store, "signing-admin@example.com")
	secret, _, err := f.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "shared-signing-key", Actor: userPrincipal(admin.ID),
		Material: secrets.GenericMaterial{Value: "whsec_MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="},
	})
	require.NoError(t, err)
	grant, err := f.Store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID: testOrgID, SecretID: secret.ID, TargetProjectID: testProjectID, Actor: userPrincipal(admin.ID),
	})
	require.NoError(t, err)
	webhook, err := f.Store.Execution().CreateEventWebhook(ctx, executionstore.CreateEventWebhookInput{
		ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{Type: executionstore.EventWebhookScopeProject},
		Settings: webhookconfig.Settings{
			URL: "https://example.com/events", EventTypes: []string{"tool_call_update"}, SigningSecretID: secret.ID,
		},
	})
	require.NoError(t, err)
	endpoint := executionstore.EventWebhookEndpoint{
		OrgID: testOrgID, ProjectID: testProjectID, SigningSecretID: secret.ID,
	}
	_, err = f.Store.Execution().ReadEventWebhookSigningSecret(ctx, endpoint)
	require.NoError(t, err)
	_, err = f.Store.Secrets().DeleteSecretGrant(ctx, secretstore.DeleteSecretGrantInput{
		OrgID: testOrgID, SecretID: secret.ID, GrantID: grant.ID, Actor: userPrincipal(admin.ID),
	})
	require.NoError(t, err, "sharing remains revocable even while a webhook references the secret")
	_, err = f.Store.Execution().ReadEventWebhookSigningSecret(ctx, endpoint)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.Store.Execution().UpdateEventWebhook(ctx, executionstore.UpdateEventWebhookInput{
		ProjectID: testProjectID, WebhookID: webhook.ID,
		EventTypes: []string{"model_output"},
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound, "updates must revalidate current secret access")
}

func TestEventWebhookPatchLocksSecretBeforeProfileTeardown(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	ctx := t.Context()
	secret, _, err := f.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: testProjectID,
		Name: "profile-signing-key", Actor: userPrincipal(f.user.ID),
		Material: secrets.GenericMaterial{Value: "whsec_MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="},
	})
	require.NoError(t, err)
	webhook, err := f.store.Execution().CreateEventWebhook(ctx, executionstore.CreateEventWebhookInput{
		ProjectID: testProjectID,
		Scope:     executionstore.EventWebhookScope{Type: executionstore.EventWebhookScopeAgentProfile, ID: f.profile.ID},
		Settings: webhookconfig.Settings{
			URL: "https://example.test/profile", EventTypes: []string{"tool_call_update"}, SigningSecretID: secret.ID,
		},
	})
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, ctx, f.store.pool)
	_, err = blocker.Exec(ctx, "SELECT id FROM secrets WHERE id = $1 FOR UPDATE", secret.ID)
	require.NoError(t, err)
	updated := integrationdb.RunAsync(func() (executionstore.EventWebhookRecord, error) {
		return f.store.Execution().UpdateEventWebhook(ctx, executionstore.UpdateEventWebhookInput{
			ProjectID: testProjectID, WebhookID: webhook.ID, EventTypes: []string{"model_output"},
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockSecretForReference", 1)
	deleted := integrationdb.RunAsyncError(func() error {
		return f.store.Execution().DeleteAgentProfile(ctx, testProjectID, f.profile.ID)
	})
	require.NoError(t, integrationdb.Await(t, deleted, "profile deletion while patch waits for secret"))
	require.NoError(t, blocker.Commit(ctx))
	require.ErrorIs(t, integrationdb.Await(t, updated, "patch after profile deletion").Err, storeerr.ErrNotFound)
	var retired bool
	require.NoError(t, f.store.pool.QueryRow(ctx,
		"SELECT deleted_at IS NOT NULL AND signing_secret_id IS NULL FROM event_webhooks WHERE id = $1", webhook.ID,
	).Scan(&retired))
	require.True(t, retired)
}

//go:build integration

package storage

import (
	"testing"

	"github.com/google/uuid"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestProjectSecretDeletionRequiresItsExclusiveLifecycleGate(t *testing.T) {
	t.Parallel()
	f := newSecretConsumerFixture(t, "project")
	_, err := f.createWebhook()
	require.NoError(t, err)
	ctx := t.Context()
	unit, err := agentexecution.BeginTestUnit(ctx, agentexecution.NewCell(f.store.pool, nil, nil))
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(ctx) }()
	require.ErrorIs(t, unit.LockProjectSecretsForDeletion(ctx, testOrgID, testProjectID), agentexecution.ErrLockPlan)
	require.NoError(t, unit.LockProjectDeletion(ctx, testOrgID, f.consumer.ID))
	require.ErrorIs(t, unit.LockProjectSecretsForDeletion(ctx, testOrgID, testProjectID), agentexecution.ErrLockPlan,
		"another project's gate cannot authorize excluding the secret owner's consumers")
	require.NoError(t, unit.LockProjectDeletion(ctx, testOrgID, testProjectID))
	require.ErrorIs(t, unit.LockProjectSecretsForDeletion(ctx, testOrgID, testProjectID), storeerr.ErrConflict,
		"holding both gates must not exclude the other project's live consumer")
}

// Each owner-deletion entry point must preserve ciphertext while another project
// can use it, but revoking that access must leave the owner free to delete it.
func TestSecretConsumerOwnerDeletion(t *testing.T) {
	t.Parallel()
	for _, ownerDeletion := range []string{"secret", "project", "account", "account_owner", "member"} {
		t.Run(ownerDeletion, func(t *testing.T) {
			t.Parallel()
			f := newSecretConsumerFixture(t, ownerDeletion)
			webhook, err := f.createWebhook()
			require.NoError(t, err)
			require.ErrorIs(t, f.deleteOwner(), storeerr.ErrConflict)
			var versions int
			require.NoError(t, f.store.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM secret_versions WHERE secret_id = $1", f.secret.ID).Scan(&versions))
			require.Equal(t, 1, versions)
			_, err = f.store.Secrets().DeleteSecretGrant(t.Context(), secretstore.DeleteSecretGrantInput{
				OrgID: testOrgID, SecretID: f.secret.ID, GrantID: f.grant.ID, Actor: userPrincipal(f.user.ID),
			})
			require.NoError(t, err)
			require.NoError(t, f.deleteOwner())
			remaining, err := f.store.Execution().GetEventWebhook(t.Context(), f.consumer.ID, webhook.ID)
			require.NoError(t, err)
			require.Equal(t, f.secret.ID, remaining.Settings.SigningSecretID,
				"revocation and owner deletion must never convert a live webhook to unsigned")
			_, err = f.store.Execution().ReadEventWebhookSigningSecret(t.Context(), executionstore.EventWebhookEndpoint{
				OrgID: testOrgID, ProjectID: f.consumer.ID, SigningSecretID: f.secret.ID,
			})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		})
	}
}

func TestSecretConsumerAdmissionSerializesWithOwnerDeletion(t *testing.T) {
	t.Parallel()
	for _, ownerDeletion := range []string{"project", "account", "account_owner", "member"} {
		for _, first := range []string{"admission", "deletion"} {
			t.Run(ownerDeletion+"/"+first, func(t *testing.T) {
				t.Parallel()
				f := newSecretConsumerFixture(t, ownerDeletion)
				ctx := t.Context()
				blocker := integrationdb.BeginTx(t, ctx, f.store.pool)
				if first == "admission" {
					_, err := blocker.Exec(ctx, "LOCK TABLE event_webhooks IN SHARE MODE")
					require.NoError(t, err)
					created := integrationdb.RunAsync(f.createWebhook)
					integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "InsertEventWebhook", 1)
					deleted := integrationdb.RunAsyncError(f.deleteOwner)
					lockQuery := map[string]string{
						"project":       "LockProjectOwnedSecretsForDeletion",
						"account":       "LockUserOwnedSecretsForDeletion",
						"account_owner": "LockUserOwnedSecretsForDeletion",
						"member":        "LockOrgMemberOwnedSecretsForDeletion",
					}[ownerDeletion]
					integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, lockQuery, 1)
					require.NoError(t, blocker.Commit(ctx))
					integrationdb.AwaitSuccess(t, created, "webhook admission")
					require.ErrorIs(t, integrationdb.Await(t, deleted, "owner deletion"), storeerr.ErrConflict)
					return
				}
				_, err := blocker.Exec(ctx, "SELECT id FROM secret_versions WHERE secret_id = $1 FOR UPDATE", f.secret.ID)
				require.NoError(t, err)
				deleted := integrationdb.RunAsyncError(f.deleteOwner)
				deleteQuery := map[string]string{
					"project":       "DeleteProjectSecretVersions",
					"account":       "DeleteUserOwnedSecretVersionsForUser",
					"account_owner": "DeleteUserOwnedSecretVersionsForUser",
					"member":        "DeleteUserOwnedSecretVersionsForOrgMember",
				}[ownerDeletion]
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, deleteQuery, 1)
				created := integrationdb.RunAsync(f.createWebhook)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockSecretForReference", 1)
				require.NoError(t, blocker.Commit(ctx))
				require.NoError(t, integrationdb.Await(t, deleted, "owner deletion"))
				require.ErrorIs(t, integrationdb.Await(t, created, "webhook admission").Err, storeerr.ErrNotFound)
			})
		}
	}
}

func TestDeletingWebhookConsumerScopeReleasesSigningSecret(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"project", "organization"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			f := newSecretConsumerFixture(t, "secret")
			webhook, err := f.createWebhook()
			require.NoError(t, err)
			if scope == "project" {
				_, err = f.store.Organizations().DeleteProject(t.Context(), testOrgID, f.consumer.ID, userPrincipal(f.admin.ID))
				require.NoError(t, err)
				require.NoError(t, f.deleteOwner())
			} else {
				_, err = f.store.Organizations().DeleteOrganization(t.Context(), testOrgID, userPrincipal(f.admin.ID))
				require.NoError(t, err)
			}
			var retired bool
			require.NoError(t, f.store.pool.QueryRow(t.Context(),
				"SELECT deleted_at IS NOT NULL AND signing_secret_id IS NULL FROM event_webhooks WHERE id = $1", webhook.ID).Scan(&retired))
			require.True(t, retired)
		})
	}
}

func TestProjectDeletionExcludesItsOwnSecretConsumers(t *testing.T) {
	t.Parallel()
	f := newSecretConsumerFixture(t, "project")
	_, err := f.store.Execution().CreateEventWebhook(t.Context(), executionstore.CreateEventWebhookInput{
		ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{Type: executionstore.EventWebhookScopeProject},
		Settings: webhookconfig.Settings{
			URL: "https://example.test/owned", EventTypes: []string{"tool_call_update"}, SigningSecretID: f.secret.ID,
		},
	})
	require.NoError(t, err)
	require.NoError(t, f.deleteOwner(), "the project's own consumer is retired by the same transaction")
}

type secretConsumerFixture struct {
	store         *Store
	user, admin   identitystore.UserRecord
	consumer      identitystore.ProjectRecord
	secret        secretstore.SecretRecord
	grant         secretstore.SecretGrantRecord
	createWebhook func() (executionstore.EventWebhookRecord, error)
	deleteOwner   func() error
}

func newSecretConsumerFixture(t *testing.T, deletion string) secretConsumerFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	f := secretConsumerFixture{store: newSecretIntegrationStore(pool)}
	f.admin = createSecretTestUser(t, ctx, f.store, "Lifecycle operator", "owner")
	role := "admin"
	if deletion == "account_owner" {
		role = "owner"
	}
	f.user = createSecretTestUser(t, ctx, f.store, "Secret owner", role)
	var err error
	f.consumer, err = f.store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID: testOrgID, Creator: userPrincipal(f.admin.ID), Name: "Consumer", IdempotencyKey: "consumer",
	})
	require.NoError(t, err)
	ownerKind := secretstore.SecretOwnerOrg
	var ownerProject, ownerUser uuid.UUID
	switch deletion {
	case "project":
		ownerKind, ownerProject = secretstore.SecretOwnerProject, testProjectID
	case "account", "account_owner", "member":
		ownerKind, ownerUser = secretstore.SecretOwnerUser, f.user.ID
	}
	f.secret, _, err = f.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: ownerKind, OwnerProjectID: ownerProject, OwnerUserID: ownerUser,
		Name: "signing-key", Actor: userPrincipal(f.user.ID),
		Material: secrets.GenericMaterial{Value: "whsec_MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="},
	})
	require.NoError(t, err)
	f.grant, err = f.store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID: testOrgID, SecretID: f.secret.ID, TargetProjectID: f.consumer.ID, Actor: userPrincipal(f.user.ID),
	})
	require.NoError(t, err)
	f.createWebhook = func() (executionstore.EventWebhookRecord, error) {
		return f.store.Execution().CreateEventWebhook(ctx, executionstore.CreateEventWebhookInput{
			ProjectID: f.consumer.ID, Scope: executionstore.EventWebhookScope{Type: executionstore.EventWebhookScopeProject},
			Settings: webhookconfig.Settings{
				URL: "https://example.test/events", EventTypes: []string{"tool_call_update"}, SigningSecretID: f.secret.ID,
			},
		})
	}
	f.deleteOwner = func() error {
		switch deletion {
		case "project":
			_, err := f.store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, userPrincipal(f.admin.ID))
			return err
		case "account", "account_owner":
			return f.store.Identity().DeleteUserAccount(ctx, f.user.ID)
		case "member":
			return f.store.Identity().RemoveOrgMember(ctx, identitystore.RemoveOrgMemberInput{
				OrgID: testOrgID, UserID: f.user.ID, ActorPrincipal: userPrincipal(f.admin.ID),
			})
		default:
			_, err := f.store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
				OrgID: testOrgID, SecretID: f.secret.ID, Actor: userPrincipal(f.user.ID),
			})
			return err
		}
	}
	return f
}

func TestSecretConsumerIntegrationAdmissionSerializesWithInstallerDeletion(t *testing.T) {
	t.Parallel()
	for _, deletion := range []string{"account", "member"} {
		t.Run(deletion, func(t *testing.T) {
			t.Parallel()
			f := newSecretConsumerFixture(t, deletion)
			ctx := t.Context()
			integration, err := f.store.Integrations().CreateIntegration(ctx, integrationstore.SaveIntegrationInput{
				OrgID: testOrgID, ProjectID: f.consumer.ID, Name: "discord", IntegrationKind: integrationdefinition.DiscordThread,
			})
			require.NoError(t, err)
			blocker := integrationdb.BeginTx(t, ctx, f.store.pool)
			_, err = blocker.Exec(ctx, "LOCK TABLE integrations IN SHARE MODE")
			require.NoError(t, err)
			configured := integrationdb.RunAsync(func() (integrationstore.IntegrationRecord, error) {
				return f.store.Integrations().ConfigureIntegration(ctx, integrationstore.ConfigureIntegrationInput{
					OrgID: testOrgID, ProjectID: f.consumer.ID, IntegrationID: integration.ID, InstalledByUserID: f.user.ID,
					Provider: integrationdefinition.ProviderDiscord, ProviderTenantID: "123", ProviderAccountRef: "456",
					CredentialSecretID: f.secret.ID, CredentialVersionID: f.secret.CurrentVersionID,
					ExpectedSetupRevision: integration.SetupRevision,
				})
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "ConfigureIntegration", 1)
			deleted := integrationdb.RunAsyncError(f.deleteOwner)
			query := "LockUserOwnedSecretsForDeletion"
			if deletion == "member" {
				query = "LockOrgMemberOwnedSecretsForDeletion"
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, query, 1)
			require.NoError(t, blocker.Commit(ctx))
			integrationdb.AwaitSuccess(t, configured, "integration admission")
			require.ErrorIs(t, integrationdb.Await(t, deleted, "installer deletion"), storeerr.ErrConflict)
		})
	}
}

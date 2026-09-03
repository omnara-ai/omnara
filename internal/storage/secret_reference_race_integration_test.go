//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestDeleteSecretSerializesWithIntegrationAppAssociation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Channel Credential Race", authz.OrgRoleAdmin)
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: admin.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("add project membership: %v", err)
	}
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "channel-credential-race",
		Material:       secrets.GenericMaterial{Value: "provider-token"},
		Actor:          userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create credential secret: %v", err)
	}

	associationTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app association: %v", err)
	}
	defer func() { _ = associationTx.Rollback(ctx) }()
	var associationPID int32
	if err := associationTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&associationPID); err != nil {
		t.Fatalf("get app association backend: %v", err)
	}
	if _, err := associationTx.Exec(ctx, `
INSERT INTO integration_apps(
  org_id, owner_project_id, provider, provider_app_ref, display_name, connector_key,
  credential_secret_id, provider_config,
  provider_metadata, configuration_revision, state, created_at, updated_at
) VALUES (
  $1, $2, 'discord', 'credential-race-app', 'Credential race app',
  'chat_sdk_v1', $3, '{}'::jsonb, '{}'::jsonb, 1, 'active',
  statement_timestamp(), statement_timestamp()
)`, testOrgID, testProjectID, secret.ID); err != nil {
		t.Fatalf("associate app credential: %v", err)
	}

	type deleteResult struct{ err error }
	deleted := make(chan deleteResult, 1)
	go func() {
		_, deleteErr := store.Secrets().DeleteSecret(
			context.Background(),
			secretstore.DeleteSecretInput{
				OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(admin.ID),
			},
		)
		deleted <- deleteResult{err: deleteErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteSecret ",
		associationPID,
	)
	if err := associationTx.Commit(ctx); err != nil {
		t.Fatalf("commit app association: %v", err)
	}

	if result := <-deleted; !errors.Is(result.err, storeerr.ErrConflict) {
		t.Fatalf("concurrent secret deletion error = %v, want conflict", result.err)
	}
	if _, err := store.Secrets().GetSecret(ctx, testOrgID, secret.ID); err != nil {
		t.Fatalf("referenced secret was deleted: %v", err)
	}
}

func TestDeleteSecretSerializesWithIntegrationInstallAssociation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Install Credential Race", authz.OrgRoleAdmin)
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: admin.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("add project membership: %v", err)
	}
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "installation-credential-race",
		Material:       secrets.GenericMaterial{Value: "installation-token"},
		Actor:          userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create installation credential: %v", err)
	}
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: "discord", ProviderAppRef: "install-race-app",
			DisplayName: "Install race app", ConnectorKey: "chat_sdk_v1",
			InstallationCredentialKind: "generic",
			State:                      integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create integration app: %v", err)
	}
	associationTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin install association: %v", err)
	}
	defer func() { _ = associationTx.Rollback(ctx) }()
	var associationPID int32
	if err := associationTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&associationPID); err != nil {
		t.Fatalf("get install association backend: %v", err)
	}
	if _, err := associationTx.Exec(ctx, `
INSERT INTO integration_installs(
  org_id, project_id, integration_app_id,
  installed_by_user_id, provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name,
  credential_secret_id, provider_config, provider_identity, provider_metadata,
  created_at, updated_at
) VALUES (
  $1, $2, $3, $4, 'discord', 'mention', 'gateway', 'active',
  'credential-race-tenant', 'credential-race-account', 'Race bot', $5,
  '{}'::jsonb, '{}'::jsonb, '{}'::jsonb,
  statement_timestamp(), statement_timestamp()
)`, testOrgID, testProjectID, app.ID, admin.ID, secret.ID); err != nil {
		t.Fatalf("associate install credential: %v", err)
	}

	type deleteResult struct{ err error }
	deleted := make(chan deleteResult, 1)
	go func() {
		_, deleteErr := store.Secrets().DeleteSecret(
			context.Background(),
			secretstore.DeleteSecretInput{
				OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(admin.ID),
			},
		)
		deleted <- deleteResult{err: deleteErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteSecret ",
		associationPID,
	)
	if err := associationTx.Commit(ctx); err != nil {
		t.Fatalf("commit install association: %v", err)
	}

	if result := <-deleted; !errors.Is(result.err, storeerr.ErrConflict) {
		t.Fatalf("concurrent secret deletion error = %v, want conflict", result.err)
	}
	if _, err := store.Secrets().GetSecret(ctx, testOrgID, secret.ID); err != nil {
		t.Fatalf("referenced installation secret was deleted: %v", err)
	}
}

func TestIntegrationCredentialAssociationRejectsSecretDeletedFirst(t *testing.T) {
	for _, associationKind := range []string{"app", "install"} {
		associationKind := associationKind
		t.Run(associationKind, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			admin := createSecretTestUser(
				t,
				ctx,
				store,
				"Deleted First "+associationKind,
				authz.OrgRoleAdmin,
			)
			if _, err := store.Identity().AddProjectMembership(
				ctx,
				identitystore.AddProjectMembershipInput{
					OrgID: testOrgID, ProjectID: testProjectID, UserID: admin.ID, Role: "admin",
				},
			); err != nil {
				t.Fatalf("add project membership: %v", err)
			}
			secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
				OrgID:          testOrgID,
				OwnerKind:      secretstore.SecretOwnerProject,
				OwnerProjectID: testProjectID,
				Name:           "deleted-first-" + associationKind,
				Material:       secrets.GenericMaterial{Value: "provider-token"},
				Actor:          userPrincipal(admin.ID),
			})
			if err != nil {
				t.Fatalf("create credential secret: %v", err)
			}

			var associationSQL, associationQueryFragment, countSQL string
			var associationArgs []any
			if associationKind == "app" {
				associationSQL = `
INSERT INTO integration_apps(
  org_id, owner_project_id, provider, provider_app_ref, display_name, connector_key,
  credential_secret_id, provider_config,
  provider_metadata, configuration_revision, state, created_at, updated_at
) VALUES (
  $1, $2, 'discord', 'deleted-first-app', 'Deleted first app',
  'chat_sdk_v1', $3, '{}'::jsonb, '{}'::jsonb, 1, 'active',
  statement_timestamp(), statement_timestamp()
)`
				associationArgs = []any{testOrgID, testProjectID, secret.ID}
				associationQueryFragment = "INSERT INTO integration_apps"
				countSQL = `SELECT count(*) FROM integration_apps WHERE credential_secret_id = $1`
			} else {
				app, err := store.Integrations().CreateIntegrationApp(
					ctx,
					integrationstore.CreateIntegrationAppInput{
						OrgID: testOrgID, OwnerProjectID: testProjectID,
						Provider: "discord", ProviderAppRef: "deleted-first-install-app",
						DisplayName: "Deleted first install app", ConnectorKey: "chat_sdk_v1",
						InstallationCredentialKind: "generic",
						State:                      integrationstore.IntegrationAppStateActive,
					},
				)
				if err != nil {
					t.Fatalf("create installation app: %v", err)
				}
				associationSQL = `
INSERT INTO integration_installs(
  org_id, project_id, integration_app_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name,
  credential_secret_id, provider_config, provider_identity, provider_metadata,
  created_at, updated_at
) VALUES (
  $1, $2, $3, $4, 'discord', 'all_messages', 'gateway', 'active',
  'deleted-first-tenant', 'deleted-first-account', 'Deleted first bot', $5,
  '{}'::jsonb, '{}'::jsonb, '{}'::jsonb,
  statement_timestamp(), statement_timestamp()
)`
				associationArgs = []any{testOrgID, testProjectID, app.ID, admin.ID, secret.ID}
				associationQueryFragment = "INSERT INTO integration_installs"
				countSQL = `SELECT count(*) FROM integration_installs WHERE credential_secret_id = $1`
			}

			deletionTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin credential deletion: %v", err)
			}
			t.Cleanup(func() { _ = deletionTx.Rollback(ctx) })
			var deletionPID int32
			if err := deletionTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&deletionPID); err != nil {
				t.Fatalf("load credential deletion backend: %v", err)
			}
			if _, err := deletionTx.Exec(ctx, `
UPDATE secrets
SET deleted_at = statement_timestamp(), current_version_id = NULL,
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
`, testOrgID, secret.ID); err != nil {
				t.Fatalf("soft-delete credential: %v", err)
			}
			if _, err := deletionTx.Exec(
				ctx,
				`DELETE FROM secret_versions WHERE org_id = $1 AND secret_id = $2`,
				testOrgID,
				secret.ID,
			); err != nil {
				t.Fatalf("destroy credential ciphertext: %v", err)
			}

			associationDone := make(chan error, 1)
			go func() {
				_, err := pool.Exec(context.Background(), associationSQL, associationArgs...)
				associationDone <- err
			}()
			integrationdb.WaitForLockWaitBlockedBy(
				t,
				ctx,
				pool,
				associationQueryFragment,
				deletionPID,
			)
			if err := deletionTx.Commit(ctx); err != nil {
				t.Fatalf("commit credential deletion: %v", err)
			}
			select {
			case err := <-associationDone:
				if err == nil {
					t.Fatal("credential association committed after credential deletion")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("credential association did not finish after deletion")
			}
			var associations int
			if err := pool.QueryRow(ctx, countSQL, secret.ID).Scan(&associations); err != nil {
				t.Fatalf("count credential associations: %v", err)
			}
			if associations != 0 {
				t.Fatalf("credential associations = %d, want 0", associations)
			}
		})
	}
}

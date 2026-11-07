//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestCoOwnerAccountAndOrganizationDeletionSerialize(t *testing.T) {
	for _, first := range []string{"organization", "account"} {
		t.Run(first, func(t *testing.T) {
			f := newSecretConsumerFixture(t, "account_owner")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			config := f.store.pool.Config()
			application := "co-owner-deletion-" + uuid.NewString()
			config.ConnConfig.RuntimeParams["application_name"] = application
			secondPool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer secondPool.Close()
			secondStore := newIntegrationStore(secondPool)
			control := integrationdb.BeginTx(t, ctx, f.store.pool)
			defer func() { _ = control.Rollback(ctx) }()
			deleteOrganization := func(store *Store) error {
				_, deleteErr := store.Organizations().DeleteOrganizationOnceForIntegration(
					ctx, testOrgID, userPrincipal(f.admin.ID),
				)
				return deleteErr
			}
			var accountDone, organizationDone <-chan error
			if first == "organization" {
				_, err = control.Exec(ctx, "SELECT id FROM secrets WHERE id = $1 FOR UPDATE", f.secret.ID)
				require.NoError(t, err)
				organizationDone = integrationdb.RunAsyncError(func() error { return deleteOrganization(f.store) })
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockOrganizationSecretsForDeletion", 1)
				accountDone = integrationdb.RunAsyncError(func() error {
					return secondStore.Identity().DeleteUserAccount(ctx, f.user.ID)
				})
			} else {
				_, err = control.Exec(ctx,
					"SELECT id FROM org_memberships WHERE org_id = $1 AND user_id = $2 FOR UPDATE", testOrgID, f.user.ID)
				require.NoError(t, err)
				accountDone = integrationdb.RunAsyncError(func() error {
					return f.store.Identity().DeleteUserAccount(ctx, f.user.ID)
				})
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockUserOrgMembershipsForDeletion", 1)
				organizationDone = integrationdb.RunAsyncError(func() error { return deleteOrganization(secondStore) })
			}
			// A co-owner's account deletion locks the organization row before its
			// membership. Organization deletion must use the same order, including
			// when it has already reached owned-secret cleanup.
			integrationdb.WaitForApplicationLockWaiter(t, ctx, f.store.pool, application)
			require.NoError(t, control.Commit(ctx))
			accountErr := integrationdb.Await(t, accountDone, "co-owner account deletion")
			organizationErr := integrationdb.Await(t, organizationDone, "organization deletion")
			require.NoError(t, accountErr)
			require.NoError(t, organizationErr, "the first attempt must succeed without deadlock retries")
			var accountDeleted, organizationDeleted bool
			var memberships, versions int
			require.NoError(t, f.store.pool.QueryRow(ctx, `
SELECT
  (SELECT deleted_at IS NOT NULL FROM users WHERE id = $1),
  (SELECT deleted_at IS NOT NULL FROM orgs WHERE id = $2),
  (SELECT count(*) FROM org_memberships WHERE org_id = $2),
  (SELECT count(*) FROM secret_versions WHERE secret_id = $3)
`, f.user.ID, testOrgID, f.secret.ID).Scan(&accountDeleted, &organizationDeleted, &memberships, &versions))
			require.True(t, accountDeleted)
			require.True(t, organizationDeleted)
			require.Zero(t, memberships)
			require.Zero(t, versions)
		})
	}
}

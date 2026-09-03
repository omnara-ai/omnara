//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestProjectDeletionWaitsForProjectSecretCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "secret-create-before-delete@example.com")

	duplicateHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin duplicate secret holder: %v", err)
	}
	t.Cleanup(func() { _ = duplicateHolder.Rollback(ctx) })
	var holderPID int32
	if err := duplicateHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("load duplicate secret holder backend: %v", err)
	}
	if _, err := duplicateHolder.Exec(ctx, `
INSERT INTO secrets(
  id, org_id, management_kind, owner_kind, owner_project_id, name, kind,
  metadata, current_version_id, created_at, updated_at
)
VALUES (
  uuidv7(), $1, 'tenant', 'project', $2, 'project-secret-create-race',
  'generic', '{}'::jsonb, uuidv7(), transaction_timestamp(), transaction_timestamp()
)
`, testOrgID, testProjectID); err != nil {
		t.Fatalf("insert duplicate secret authority holder: %v", err)
	}

	type createResult struct {
		secret secretstore.SecretRecord
		err    error
	}
	createDone := make(chan createResult, 1)
	go func() {
		secret, _, err := store.Secrets().CreateSecret(
			context.Background(),
			secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject,
				OwnerProjectID: testProjectID, Name: "project-secret-create-race",
				Material: secrets.GenericMaterial{Value: "credential"},
				Actor:    identitystore.NewUserPrincipal(admin.ID),
			},
		)
		createDone <- createResult{secret: secret, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: InsertSecret ",
		holderPID,
	)
	createPID := integrationLifecycleWaiterPID(
		t,
		ctx,
		pool,
		"-- name: InsertSecret ",
		holderPID,
	)

	deleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteProject(
			context.Background(),
			testOrgID,
			testProjectID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		deleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockProjectLifecycleExclusive ",
		createPID,
	)

	if err := duplicateHolder.Rollback(ctx); err != nil {
		t.Fatalf("release duplicate secret authority holder: %v", err)
	}
	var createdID ID
	select {
	case result := <-createDone:
		if result.err != nil {
			t.Fatalf("finish project secret creation: %v", result.err)
		}
		createdID = result.secret.ID
	case <-time.After(5 * time.Second):
		t.Fatal("project secret creation did not finish")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish project deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project deletion did not finish after secret creation")
	}
	if _, err := store.Secrets().GetSecret(ctx, testOrgID, createdID); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("load secret after project deletion error = %v, want not found", err)
	}
}

func TestProjectSecretCreationRejectsProjectDeletedFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "secret-delete-before-create@example.com")
	seed, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID, Name: "project-secret-delete-blocker",
		Material: secrets.GenericMaterial{Value: "seed"},
		Actor:    identitystore.NewUserPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create project secret deletion blocker: %v", err)
	}

	secretHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin secret holder: %v", err)
	}
	t.Cleanup(func() { _ = secretHolder.Rollback(ctx) })
	var holderPID int32
	if err := secretHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("load secret holder backend: %v", err)
	}
	if _, err := secretHolder.Exec(
		ctx,
		`SELECT id FROM secrets WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		seed.ID,
	); err != nil {
		t.Fatalf("lock project secret: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteProject(
			context.Background(),
			testOrgID,
			testProjectID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		deleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteProjectSecret",
		holderPID,
	)
	deletePID := integrationLifecycleWaiterPID(
		t,
		ctx,
		pool,
		"-- name: DeleteProjectSecret",
		holderPID,
	)

	createDone := make(chan error, 1)
	go func() {
		_, _, err := store.Secrets().CreateSecret(
			context.Background(),
			secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject,
				OwnerProjectID: testProjectID, Name: "project-secret-delete-loser",
				Material: secrets.GenericMaterial{Value: "credential"},
				Actor:    identitystore.NewUserPrincipal(admin.ID),
			},
		)
		createDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockProjectLifecycleShared ",
		deletePID,
	)

	if err := secretHolder.Rollback(ctx); err != nil {
		t.Fatalf("release project secret: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish project deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project deletion did not finish after secret unlock")
	}
	select {
	case err := <-createDone:
		if !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("secret creation after project deletion error = %v, want not found", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project secret creation did not reject the deleted project")
	}
	var liveSecrets int
	if err := pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM secrets
WHERE org_id = $1 AND owner_project_id = $2 AND deleted_at IS NULL
`, testOrgID, testProjectID).Scan(&liveSecrets); err != nil {
		t.Fatalf("count live project secrets: %v", err)
	}
	if liveSecrets != 0 {
		t.Fatalf("live project secrets after deletion = %d, want zero", liveSecrets)
	}
}

func TestOrganizationDeletionWaitsForOrganizationSecretCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "org-secret-create-before-delete@example.com")

	duplicateHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin duplicate organization secret holder: %v", err)
	}
	t.Cleanup(func() { _ = duplicateHolder.Rollback(ctx) })
	var holderPID int32
	if err := duplicateHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("load duplicate organization secret holder backend: %v", err)
	}
	if _, err := duplicateHolder.Exec(ctx, `
INSERT INTO secrets(
  id, org_id, management_kind, owner_kind, name, kind,
  metadata, current_version_id, created_at, updated_at
)
VALUES (
  uuidv7(), $1, 'tenant', 'org', 'organization-secret-create-race',
  'generic', '{}'::jsonb, uuidv7(), transaction_timestamp(), transaction_timestamp()
)
`, testOrgID); err != nil {
		t.Fatalf("insert duplicate organization secret authority holder: %v", err)
	}

	type createResult struct {
		secret secretstore.SecretRecord
		err    error
	}
	createDone := make(chan createResult, 1)
	go func() {
		secret, _, err := store.Secrets().CreateSecret(
			context.Background(),
			secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg,
				Name:     "organization-secret-create-race",
				Material: secrets.GenericMaterial{Value: "credential"},
				Actor:    identitystore.NewUserPrincipal(admin.ID),
			},
		)
		createDone <- createResult{secret: secret, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: InsertSecret ",
		holderPID,
	)
	createPID := integrationLifecycleWaiterPID(
		t,
		ctx,
		pool,
		"-- name: InsertSecret ",
		holderPID,
	)

	deleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteOrganization(
			context.Background(),
			testOrgID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		deleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteOrganization ",
		createPID,
	)

	if err := duplicateHolder.Rollback(ctx); err != nil {
		t.Fatalf("release duplicate organization secret authority holder: %v", err)
	}
	var createdID ID
	select {
	case result := <-createDone:
		if result.err != nil {
			t.Fatalf("finish organization secret creation: %v", result.err)
		}
		createdID = result.secret.ID
	case <-time.After(5 * time.Second):
		t.Fatal("organization secret creation did not finish")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish organization deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organization deletion did not finish after secret creation")
	}
	if _, err := store.Secrets().GetSecret(ctx, testOrgID, createdID); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("load secret after organization deletion error = %v, want not found", err)
	}
}

func TestOrganizationSecretCreationRejectsOrganizationDeletedFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "org-secret-delete-before-create@example.com")
	seed, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg,
		Name:     "organization-secret-delete-blocker",
		Material: secrets.GenericMaterial{Value: "seed"},
		Actor:    identitystore.NewUserPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create organization secret deletion blocker: %v", err)
	}

	secretHolder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin organization secret holder: %v", err)
	}
	t.Cleanup(func() { _ = secretHolder.Rollback(ctx) })
	var holderPID int32
	if err := secretHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("load organization secret holder backend: %v", err)
	}
	if _, err := secretHolder.Exec(
		ctx,
		`SELECT id FROM secrets WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		seed.ID,
	); err != nil {
		t.Fatalf("lock organization secret: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteOrganization(
			context.Background(),
			testOrgID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		deleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteOrganizationSecrets ",
		holderPID,
	)
	deletePID := integrationLifecycleWaiterPID(
		t,
		ctx,
		pool,
		"-- name: DeleteOrganizationSecrets ",
		holderPID,
	)

	createDone := make(chan error, 1)
	go func() {
		_, _, err := store.Secrets().CreateSecret(
			context.Background(),
			secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg,
				Name:     "organization-secret-delete-loser",
				Material: secrets.GenericMaterial{Value: "credential"},
				Actor:    identitystore.NewUserPrincipal(admin.ID),
			},
		)
		createDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockOrganizationLifecycleShared ",
		deletePID,
	)

	if err := secretHolder.Rollback(ctx); err != nil {
		t.Fatalf("release organization secret: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish organization deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organization deletion did not finish after secret unlock")
	}
	select {
	case err := <-createDone:
		if !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("secret creation after organization deletion error = %v, want not found", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organization secret creation did not reject the deleted organization")
	}
	var liveSecrets int
	if err := pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM secrets
WHERE org_id = $1 AND deleted_at IS NULL
`, testOrgID).Scan(&liveSecrets); err != nil {
		t.Fatalf("count live organization secrets: %v", err)
	}
	if liveSecrets != 0 {
		t.Fatalf("live organization secrets after deletion = %d, want zero", liveSecrets)
	}
}

func TestOAuthLeaseAcquisitionFencesOrganizationDeletionBeforeSecretAndLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, secret := createOrganizationOAuthLifecycleFixture(
		t,
		ctx,
		store,
		"oauth-acquire-org-lifecycle@example.com",
		"oauth-acquire-org-lifecycle",
	)

	leaseHolder, holderPID := holdOrganizationOAuthLease(t, ctx, pool, secret.ID)
	type acquireResult struct {
		acquired bool
		err      error
	}
	acquireDone := make(chan acquireResult, 1)
	go func() {
		_, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
			context.Background(),
			secretstore.AcquireProjectOAuthRefreshLeaseInput{
				OrgID: testOrgID, ProjectID: testProjectID, SecretID: secret.ID,
				TTL: time.Minute,
			},
		)
		acquireDone <- acquireResult{acquired: acquired, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: AcquireSecretOAuthRefreshLease ",
		holderPID,
	)
	acquirePID := integrationLifecycleWaiterPID(
		t,
		ctx,
		pool,
		"-- name: AcquireSecretOAuthRefreshLease ",
		holderPID,
	)

	deleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteOrganization(
			context.Background(),
			testOrgID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		deleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteOrganization ",
		acquirePID,
	)

	if err := leaseHolder.Rollback(ctx); err != nil {
		t.Fatalf("release oauth lease row: %v", err)
	}
	select {
	case result := <-acquireDone:
		if result.err != nil || result.acquired {
			t.Fatalf("second oauth lease acquisition acquired=%t err=%v, want false, nil", result.acquired, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("oauth lease acquisition did not finish after lease row release")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("finish organization deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organization deletion did not finish after oauth lease acquisition")
	}
}

func TestManualSecretDeletionFencesOrganizationDeletionBeforeSecretAndLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin, secret := createOrganizationOAuthLifecycleFixture(
		t,
		ctx,
		store,
		"secret-delete-org-lifecycle@example.com",
		"secret-delete-org-lifecycle",
	)

	leaseHolder, holderPID := holdOrganizationOAuthLease(t, ctx, pool, secret.ID)
	secretDeleteDone := make(chan error, 1)
	go func() {
		_, err := store.Secrets().DeleteSecret(
			context.Background(),
			secretstore.DeleteSecretInput{
				OrgID: testOrgID, SecretID: secret.ID,
				Actor: identitystore.NewUserPrincipal(admin.ID),
			},
		)
		secretDeleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteSecretVersions ",
		holderPID,
	)
	secretDeletePID := integrationLifecycleWaiterPID(
		t,
		ctx,
		pool,
		"-- name: DeleteSecretVersions ",
		holderPID,
	)

	orgDeleteDone := make(chan error, 1)
	go func() {
		_, err := store.Organizations().DeleteOrganization(
			context.Background(),
			testOrgID,
			identitystore.NewUserPrincipal(admin.ID),
		)
		orgDeleteDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: DeleteOrganization ",
		secretDeletePID,
	)

	if err := leaseHolder.Rollback(ctx); err != nil {
		t.Fatalf("release oauth lease row: %v", err)
	}
	select {
	case err := <-secretDeleteDone:
		if err != nil {
			t.Fatalf("finish manual secret deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual secret deletion did not finish after lease row release")
	}
	select {
	case err := <-orgDeleteDone:
		if err != nil {
			t.Fatalf("finish organization deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organization deletion did not finish after manual secret deletion")
	}
}

func createOrganizationOAuthLifecycleFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email, name string,
) (identitystore.UserRecord, secretstore.SecretRecord) {
	t.Helper()
	admin := createIntegrationProjectAdmin(t, ctx, store, email)
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: name,
		Material: secrets.OAuthTokenSetMaterial{
			AccessToken:         "access-token",
			AccessTokenLifetime: secrets.FixedOAuthAccessTokenLifetime(time.Hour),
			Refresh: &secrets.OAuthRefreshMaterial{
				RefreshToken: "refresh-token", TokenEndpoint: "https://auth.example.com/token",
				ClientID: "client-id", Resource: "https://example.com",
			},
		},
		Actor: identitystore.NewUserPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create organization oauth secret: %v", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID: testOrgID, SecretID: secret.ID, TargetProjectID: testProjectID,
		Actor: identitystore.NewUserPrincipal(admin.ID),
	}); err != nil {
		t.Fatalf("grant organization oauth secret to project: %v", err)
	}
	_, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID: testOrgID, ProjectID: testProjectID, SecretID: secret.ID,
			TTL: time.Minute,
		},
	)
	if err != nil || !acquired {
		t.Fatalf("seed oauth refresh lease acquired=%t err=%v", acquired, err)
	}
	return admin, secret
}

func holdOrganizationOAuthLease(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	secretID ID,
) (pgx.Tx, int32) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin oauth lease holder: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var pid int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("load oauth lease holder backend: %v", err)
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT owner_token FROM secret_oauth_refresh_leases WHERE org_id = $1 AND secret_id = $2 FOR UPDATE`,
		testOrgID,
		secretID,
	); err != nil {
		t.Fatalf("lock oauth lease row: %v", err)
	}
	return tx, pid
}

func integrationLifecycleWaiterPID(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryFragment string,
	blockingPID int32,
) int32 {
	t.Helper()
	var pid int32
	if err := pool.QueryRow(ctx, `
SELECT pid
FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query ILIKE '%' || $1 || '%'
  AND $2::integer = ANY(pg_blocking_pids(pid))
ORDER BY pid
LIMIT 1
`, queryFragment, blockingPID).Scan(&pid); err != nil {
		t.Fatalf("load lifecycle waiter backend: %v", err)
	}
	return pid
}

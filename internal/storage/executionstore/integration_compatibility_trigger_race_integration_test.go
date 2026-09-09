//go:build integration

package executionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestLegacyInstallTriggerCannotRacePastProjectDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "legacy-trigger-delete-first@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "legacy-trigger-delete-first")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"legacy-trigger-delete-first",
	)

	deletion, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin project deletion: %v", err)
	}
	t.Cleanup(func() { _ = deletion.Rollback(ctx) })
	var deletionPID int32
	if err := deletion.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&deletionPID); err != nil {
		t.Fatalf("load project deletion backend: %v", err)
	}
	if _, err := deletion.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`,
		testProjectID.String(),
	); err != nil {
		t.Fatalf("lock project lifecycle exclusively: %v", err)
	}

	installDone := make(chan error, 1)
	go func() {
		_, err := insertLegacyInstallWithoutApp(
			context.Background(),
			pool,
			profile.ID,
			admin.ID,
			credentialID,
			"delete-first",
		)
		installDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"INSERT INTO integration_installs(",
		deletionPID,
	)
	if _, err := deletion.Exec(
		ctx,
		`UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1`,
		testProjectID,
	); err != nil {
		t.Fatalf("soft-delete project: %v", err)
	}
	if err := deletion.Commit(ctx); err != nil {
		t.Fatalf("commit project deletion: %v", err)
	}
	select {
	case err := <-installDone:
		if err == nil {
			t.Fatal("legacy install committed after project deletion")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("legacy install did not finish after project deletion")
	}
	var apps, installs int
	if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM integration_apps WHERE owner_project_id = $1),
       (SELECT count(*) FROM integration_installs WHERE project_id = $1)
`, testProjectID).Scan(&apps, &installs); err != nil {
		t.Fatalf("count rows after legacy delete-first race: %v", err)
	}
	if apps != 0 || installs != 0 {
		t.Fatalf("legacy delete-first rows apps=%d installs=%d, want none", apps, installs)
	}
}

func TestProjectDeletionSweepsLegacyInstallTriggerThatStartedFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "legacy-trigger-create-first@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "legacy-trigger-create-first")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"legacy-trigger-create-first",
	)

	credentialBlocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin credential blocker: %v", err)
	}
	t.Cleanup(func() { _ = credentialBlocker.Rollback(ctx) })
	var blockerPID int32
	if err := credentialBlocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatalf("load credential blocker backend: %v", err)
	}
	if _, err := credentialBlocker.Exec(
		ctx,
		`SELECT id FROM secrets WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		credentialID,
	); err != nil {
		t.Fatalf("lock legacy integration credential: %v", err)
	}

	type installResult struct {
		id  string
		err error
	}
	installDone := make(chan installResult, 1)
	go func() {
		id, err := insertLegacyInstallWithoutApp(
			context.Background(),
			pool,
			profile.ID,
			admin.ID,
			credentialID,
			"create-first",
		)
		installDone <- installResult{id: id, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"INSERT INTO integration_installs(",
		blockerPID,
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
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleExclusive", 1)
	if err := credentialBlocker.Commit(ctx); err != nil {
		t.Fatalf("release legacy integration credential: %v", err)
	}

	var installID string
	select {
	case result := <-installDone:
		if result.err != nil {
			t.Fatalf("create legacy install before project deletion: %v", result.err)
		}
		installID = result.id
	case <-time.After(5 * time.Second):
		t.Fatal("legacy install did not finish after credential release")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete project after legacy install creation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project deletion did not finish after legacy install creation")
	}
	var installDeleted, appDeleted bool
	if err := pool.QueryRow(ctx, `
SELECT install.deleted_at IS NOT NULL, app.deleted_at IS NOT NULL
FROM integration_installs install
JOIN integration_apps app ON app.id = install.integration_app_id
WHERE install.id = $1
`, installID).Scan(&installDeleted, &appDeleted); err != nil {
		t.Fatalf("load legacy rows after create-first race: %v", err)
	}
	if !installDeleted || !appDeleted {
		t.Fatalf(
			"project deletion left legacy rows active: install=%t app=%t",
			installDeleted,
			appDeleted,
		)
	}
}

func TestProjectRowDeletionTriggerRetiresCompatibilityApp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "legacy-project-trigger@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "legacy-project-trigger")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"legacy-project-trigger",
	)
	installID, err := insertLegacyInstallWithoutApp(
		ctx,
		pool,
		profile.ID,
		admin.ID,
		credentialID,
		"project-trigger",
	)
	if err != nil {
		t.Fatalf("create legacy installation: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1`,
		testProjectID,
	); err != nil {
		t.Fatalf("delete project through legacy owner-row update: %v", err)
	}
	var installDeleted, appDeleted bool
	var appState string
	if err := pool.QueryRow(ctx, `
SELECT install.deleted_at IS NOT NULL, app.deleted_at IS NOT NULL, app.state
FROM integration_installs install
JOIN integration_apps app ON app.id = install.integration_app_id
WHERE install.id = $1
`, installID).Scan(&installDeleted, &appDeleted, &appState); err != nil {
		t.Fatalf("load rows retired by project trigger: %v", err)
	}
	if installDeleted || !appDeleted || appState != "disabled" {
		t.Fatalf(
			"project trigger changed install=%t app=%t state=%q",
			installDeleted,
			appDeleted,
			appState,
		)
	}
}

func insertLegacyInstallWithoutApp(
	ctx context.Context,
	pool *pgxpool.Pool,
	profileID, installedByUserID, credentialID ID,
	suffix string,
) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `
INSERT INTO integration_installs(
    org_id, project_id, agent_profile_id, installed_by_user_id, provider,
    integration_kind, connection_mode, state, provider_tenant_id,
    provider_account_ref, provider_agent_display_name, credential_secret_id,
    provider_config, provider_identity, provider_metadata, created_at, updated_at
)
VALUES (
    $1, $2, $3, $4, 'slack', 'agent_profile', 'webhook', 'active',
    'legacy-workspace-' || $6, 'legacy-bot-' || $6, 'Legacy Omnara', $5,
    '{}'::jsonb, '{}'::jsonb, '{}'::jsonb,
    transaction_timestamp(), transaction_timestamp()
)
RETURNING id::text
`, testOrgID, testProjectID, profileID, installedByUserID, credentialID, suffix).Scan(&id)
	return id, err
}

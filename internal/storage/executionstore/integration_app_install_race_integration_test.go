//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestInstallCannotRacePastAppLifecycleChange(t *testing.T) {
	for _, test := range []struct {
		name         string
		installState integrationstore.IntegrationInstallState
		updateSQL    string
	}{
		{
			name:         "active install after app disable",
			installState: integrationstore.IntegrationInstallStateActive,
			updateSQL:    `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`,
		},
		{
			name:         "disabled install after app deletion",
			installState: integrationstore.IntegrationInstallStateDisabled,
			updateSQL: `UPDATE integration_apps
SET state = 'disabled', deleted_at = statement_timestamp()
WHERE id = $1`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			admin := createIntegrationProjectAdmin(t, ctx, store, "install-app-race@example.com")
			app, err := store.Integrations().CreateIntegrationApp(
				ctx,
				integrationstore.CreateIntegrationAppInput{
					OrgID: testOrgID, OwnerProjectID: testProjectID,
					Provider: testChannelProvider, ProviderAppRef: "install-app-race",
					DisplayName: "Install app race", ConnectorKey: testChannelConnector,
					State: integrationstore.IntegrationAppStateActive,
				},
			)
			if err != nil {
				t.Fatalf("create integration app: %v", err)
			}

			appMutation, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin app lifecycle change: %v", err)
			}
			t.Cleanup(func() { _ = appMutation.Rollback(ctx) })
			var mutationPID int32
			if err := appMutation.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&mutationPID); err != nil {
				t.Fatalf("load app lifecycle backend: %v", err)
			}
			if _, err := appMutation.Exec(
				ctx,
				`SELECT id FROM integration_apps WHERE id = $1 FOR UPDATE`,
				app.ID,
			); err != nil {
				t.Fatalf("lock integration app: %v", err)
			}

			installDone := make(chan error, 1)
			go func() {
				_, err := store.Integrations().UpsertIntegrationInstall(
					context.Background(),
					integrationstore.UpsertIntegrationInstallInput{
						OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
						InstalledByUserID: admin.ID, Provider: testChannelProvider,
						IntegrationKind: "all_messages", ConnectionMode: "gateway",
						State:              test.installState,
						ProviderTenantID:   "install-app-race-tenant",
						ProviderAccountRef: "install-app-race-account",
					},
				)
				installDone <- err
			}()
			integrationdb.WaitForLockWaitBlockedBy(
				t,
				ctx,
				pool,
				"INSERT INTO integration_installs",
				mutationPID,
			)
			if _, err := appMutation.Exec(ctx, test.updateSQL, app.ID); err != nil {
				t.Fatalf("change integration app lifecycle: %v", err)
			}
			if err := appMutation.Commit(ctx); err != nil {
				t.Fatalf("commit integration app lifecycle change: %v", err)
			}
			select {
			case err := <-installDone:
				if err == nil {
					t.Fatal("install committed after its app became unavailable")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("install did not finish after app lifecycle change")
			}
			var installs int
			if err := pool.QueryRow(
				ctx,
				`SELECT count(*) FROM integration_installs WHERE integration_app_id = $1`,
				app.ID,
			).Scan(&installs); err != nil {
				t.Fatalf("count raced integration installs: %v", err)
			}
			if installs != 0 {
				t.Fatalf("raced integration installs = %d, want 0", installs)
			}
		})
	}
}

func TestIntegrationAppCreationCannotRacePastProjectDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	createIntegrationProjectAdmin(t, ctx, store, "app-project-delete-first@example.com")

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

	createDone := make(chan error, 1)
	go func() {
		_, err := store.Integrations().CreateIntegrationApp(
			context.Background(),
			integrationstore.CreateIntegrationAppInput{
				OrgID: testOrgID, OwnerProjectID: testProjectID,
				Provider: testChannelProvider, ProviderAppRef: "project-delete-first",
				DisplayName: "Project delete first", ConnectorKey: testChannelConnector,
				State: integrationstore.IntegrationAppStateActive,
			},
		)
		createDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockProjectLifecycleShared ",
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
	case err := <-createDone:
		if !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("create app after project deletion error = %v, want not found", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app creation did not finish after project deletion")
	}
	var apps int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM integration_apps WHERE owner_project_id = $1`,
		testProjectID,
	).Scan(&apps); err != nil {
		t.Fatalf("count apps after project deletion race: %v", err)
	}
	if apps != 0 {
		t.Fatalf("apps after project deletion race = %d, want 0", apps)
	}
}

func TestProjectDeletionSweepsIntegrationAppCreationThatStartedFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "app-project-create-first@example.com")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "app-project-create-first",
		Material:       secrets.GenericMaterial{Value: "provider-token"},
		Actor:          identitystore.NewUserPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create app credential: %v", err)
	}

	secretBlocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin credential blocker: %v", err)
	}
	t.Cleanup(func() { _ = secretBlocker.Rollback(ctx) })
	var blockerPID int32
	if err := secretBlocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatalf("load credential blocker backend: %v", err)
	}
	if _, err := secretBlocker.Exec(
		ctx,
		`SELECT id FROM secrets WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		secret.ID,
	); err != nil {
		t.Fatalf("lock app credential: %v", err)
	}

	type createResult struct {
		app integrationstore.IntegrationAppRecord
		err error
	}
	createDone := make(chan createResult, 1)
	go func() {
		app, err := store.Integrations().CreateIntegrationApp(
			context.Background(),
			integrationstore.CreateIntegrationAppInput{
				OrgID: testOrgID, OwnerProjectID: testProjectID,
				Provider: testChannelProvider, ProviderAppRef: "project-create-first",
				DisplayName: "Project create first", ConnectorKey: testChannelConnector,
				CredentialSecretID: secret.ID,
				State:              integrationstore.IntegrationAppStateActive,
			},
		)
		createDone <- createResult{app: app, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: InsertIntegrationApp ",
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
	if err := secretBlocker.Commit(ctx); err != nil {
		t.Fatalf("release app credential: %v", err)
	}

	var created integrationstore.IntegrationAppRecord
	select {
	case result := <-createDone:
		if result.err != nil {
			t.Fatalf("create integration app before project deletion: %v", result.err)
		}
		created = result.app
	case <-time.After(5 * time.Second):
		t.Fatal("app creation did not finish after credential release")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete project after app creation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project deletion did not finish after app creation")
	}
	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL FROM integration_apps WHERE id = $1`,
		created.ID,
	).Scan(&deleted); err != nil {
		t.Fatalf("load raced integration app: %v", err)
	}
	if !deleted {
		t.Fatal("project deletion did not sweep the concurrently created integration app")
	}
}

func TestNativeInstallCreationCannotRacePastProjectDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "native-install-delete-first@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "native-install-delete-first")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"native-install-delete-first",
	)
	input := slackIntegrationInstallInput(
		profile.ID,
		integrationstore.NilID,
		admin.ID,
		credentialID,
		"native-install-delete-first",
		"workspace-delete-first",
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
		_, err := store.Integrations().UpsertIntegrationInstall(context.Background(), input)
		installDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: LockProjectLifecycleShared ",
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
			t.Fatal("native install committed after project deletion")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native install did not finish after project deletion")
	}
	var apps, installs int
	if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM integration_apps WHERE owner_project_id = $1),
       (SELECT count(*) FROM integration_installs WHERE project_id = $1)
`, testProjectID).Scan(&apps, &installs); err != nil {
		t.Fatalf("count native integration rows after project deletion race: %v", err)
	}
	if apps != 0 || installs != 0 {
		t.Fatalf("native rows after project deletion race apps=%d installs=%d", apps, installs)
	}
}

func TestProjectDeletionSweepsNativeInstallCreationThatStartedFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "native-install-create-first@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "native-install-create-first")
	credentialID := createIntegrationCredential(
		t,
		ctx,
		store,
		testProjectID,
		admin.ID,
		"native-install-create-first",
	)
	input := slackIntegrationInstallInput(
		profile.ID,
		integrationstore.NilID,
		admin.ID,
		credentialID,
		"native-install-create-first",
		"workspace-create-first",
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
		t.Fatalf("lock native integration credential: %v", err)
	}

	type installResult struct {
		install integrationstore.IntegrationInstallRecord
		err     error
	}
	installDone := make(chan installResult, 1)
	go func() {
		install, err := store.Integrations().UpsertIntegrationInstall(context.Background(), input)
		installDone <- installResult{install: install, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		pool,
		"-- name: InsertIntegrationInstall ",
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
		t.Fatalf("release native integration credential: %v", err)
	}

	var created integrationstore.IntegrationInstallRecord
	select {
	case result := <-installDone:
		if result.err != nil {
			t.Fatalf("create native install before project deletion: %v", result.err)
		}
		created = result.install
	case <-time.After(5 * time.Second):
		t.Fatal("native install did not finish after credential release")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete project after native install creation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project deletion did not finish after native install creation")
	}
	var installDeleted, appDeleted bool
	if err := pool.QueryRow(ctx, `
SELECT install.deleted_at IS NOT NULL, app.deleted_at IS NOT NULL
FROM integration_installs install
JOIN integration_apps app ON app.id = install.integration_app_id
WHERE install.id = $1 AND app.id = $2
`, created.ID, created.IntegrationAppID).Scan(&installDeleted, &appDeleted); err != nil {
		t.Fatalf("load raced native integration rows: %v", err)
	}
	if !installDeleted || !appDeleted {
		t.Fatalf(
			"project deletion left native install/app active: install=%t app=%t",
			installDeleted,
			appDeleted,
		)
	}
}

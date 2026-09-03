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

func TestRestrictedIntegrationAppCannotInstallAcrossProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "restricted-app-scope@example.com")
	otherProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(admin.ID),
			Name: "Restricted app other project", IdempotencyKey: "restricted-app-other-project",
		},
	)
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "restricted-app",
			DisplayName: "Restricted app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create restricted app: %v", err)
	}
	_, err = store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: otherProject.ID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID, Provider: testChannelProvider,
			IntegrationKind: "scope_test", ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "restricted-other-tenant", ProviderAccountRef: "restricted-other-account",
		},
	)
	if !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("cross-project restricted app install error = %v, want unauthorized", err)
	}
	var installs int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM integration_installs WHERE integration_app_id = $1`,
		app.ID,
	).Scan(&installs); err != nil {
		t.Fatalf("count restricted app installs: %v", err)
	}
	if installs != 0 {
		t.Fatalf("restricted app installs = %d, want 0", installs)
	}
}

func TestIntegrationAppAndInstallCredentialsFollowOwnerScopeAndKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "integration-credential-scope@example.com")
	otherProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(admin.ID),
			Name: "Credential scope other project", IdempotencyKey: "credential-scope-other",
		},
	)
	if err != nil {
		t.Fatalf("create credential scope project: %v", err)
	}
	createSecret := func(
		name, ownerKind string,
		ownerProjectID integrationstore.ID,
		material secrets.Material,
	) secretstore.SecretRecord {
		t.Helper()
		secret, _, err := store.Secrets().CreateSecret(
			ctx,
			secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: ownerKind, OwnerProjectID: ownerProjectID,
				Name: name, Material: material,
				Actor: identitystore.NewUserPrincipal(admin.ID),
			},
		)
		if err != nil {
			t.Fatalf("create credential %q: %v", name, err)
		}
		return secret
	}
	integrationMaterial := func(value string) secrets.Material {
		return secrets.IntegrationCredentialsMaterial{Values: map[string]string{"token": value}}
	}
	orgCredential := createSecret(
		"organization connector credential",
		secretstore.SecretOwnerOrg,
		NilID,
		integrationMaterial("org"),
	)
	projectAppCredential := createSecret(
		"project app connector credential",
		secretstore.SecretOwnerProject,
		testProjectID,
		integrationMaterial("project-app"),
	)
	projectInstallCredential := createSecret(
		"project install connector credential",
		secretstore.SecretOwnerProject,
		testProjectID,
		integrationMaterial("project-install"),
	)
	otherProjectCredential := createSecret(
		"other project connector credential",
		secretstore.SecretOwnerProject,
		otherProject.ID,
		integrationMaterial("other-project"),
	)
	wrongKindCredential := createSecret(
		"wrong kind connector credential",
		secretstore.SecretOwnerProject,
		testProjectID,
		secrets.GenericMaterial{Value: "generic"},
	)

	for _, test := range []struct {
		name           string
		ownerProjectID integrationstore.ID
		credentialID   integrationstore.ID
	}{
		{
			name: "shared app with project credential", credentialID: projectAppCredential.ID,
		},
		{
			name:           "restricted app with organization credential",
			ownerProjectID: testProjectID, credentialID: orgCredential.ID,
		},
		{
			name:           "restricted app with another project credential",
			ownerProjectID: testProjectID, credentialID: otherProjectCredential.ID,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.Integrations().CreateIntegrationApp(
				ctx,
				integrationstore.CreateIntegrationAppInput{
					OrgID: testOrgID, OwnerProjectID: test.ownerProjectID,
					Provider:       testChannelProvider,
					ProviderAppRef: "invalid-credential-" + test.name,
					DisplayName:    test.name, ConnectorKey: testChannelConnector,
					CredentialSecretID: test.credentialID,
					State:              integrationstore.IntegrationAppStateActive,
				},
			)
			if err == nil {
				t.Fatal("out-of-scope app credential was accepted")
			}
		})
	}

	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "valid-credential-app",
			DisplayName: "Valid credential app", ConnectorKey: testChannelConnector,
			CredentialSecretID:         projectAppCredential.ID,
			InstallationCredentialKind: string(secrets.KindIntegrationCredentials),
			State:                      integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create valid credential app: %v", err)
	}
	installInput := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
		InstalledByUserID: admin.ID, Provider: testChannelProvider,
		IntegrationKind: "credential_scope", ConnectionMode: "gateway",
		State:            integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: "credential-scope-tenant", ProviderAccountRef: "credential-scope-account",
	}
	for name, credentialID := range map[string]integrationstore.ID{
		"missing":               NilID,
		"organization owned":    orgCredential.ID,
		"another project owned": otherProjectCredential.ID,
		"wrong kind":            wrongKindCredential.ID,
	} {
		t.Run("installation "+name, func(t *testing.T) {
			candidate := installInput
			candidate.CredentialSecretID = credentialID
			if _, err := store.Integrations().UpsertIntegrationInstall(ctx, candidate); err == nil {
				t.Fatal("invalid installation credential was accepted")
			}
		})
	}
	installInput.CredentialSecretID = projectInstallCredential.ID
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, installInput)
	if err != nil {
		t.Fatalf("create installation with matching project credential: %v", err)
	}
	if install.CredentialSecretID != projectInstallCredential.ID {
		t.Fatalf("installation credential = %s, want %s", install.CredentialSecretID, projectInstallCredential.ID)
	}
}

func TestProjectDeletionPreservesSharedIntegrationAppAndOtherProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "shared-app-project-delete@example.com")
	otherProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(admin.ID),
			Name: "Shared app surviving project", IdempotencyKey: "shared-app-surviving-project",
		},
	)
	if err != nil {
		t.Fatalf("create surviving project: %v", err)
	}
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, Provider: testChannelProvider, ProviderAppRef: "shared-project-delete-app",
			DisplayName: "Shared project deletion app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create shared app: %v", err)
	}
	createInstall := func(projectID integrationstore.ID, suffix string) integrationstore.IntegrationInstallRecord {
		t.Helper()
		install, err := store.Integrations().UpsertIntegrationInstall(
			ctx,
			integrationstore.UpsertIntegrationInstallInput{
				OrgID: testOrgID, ProjectID: projectID, IntegrationAppID: app.ID,
				InstalledByUserID: admin.ID, Provider: testChannelProvider,
				IntegrationKind: "shared_scope_test", ConnectionMode: "gateway",
				State:              integrationstore.IntegrationInstallStateActive,
				ProviderTenantID:   "shared-" + suffix + "-tenant",
				ProviderAccountRef: "shared-" + suffix + "-account",
			},
		)
		if err != nil {
			t.Fatalf("create %s shared app install: %v", suffix, err)
		}
		return install
	}
	deletedProjectInstall := createInstall(testProjectID, "deleted")
	survivingInstall := createInstall(otherProject.ID, "surviving")
	appRuntime, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			UnitKey: "shared-app-runtime", RuntimeKind: "provider_gateway",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create shared app runtime: %v", err)
	}
	createInstallRuntime := func(
		projectID, installID integrationstore.ID,
		suffix string,
	) integrationstore.IntegrationRuntimeUnitRecord {
		t.Helper()
		unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				ProjectID: projectID, IntegrationInstallID: installID,
				UnitKey: "shared-" + suffix + "-runtime", RuntimeKind: "provider_socket",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create %s install runtime: %v", suffix, err)
		}
		return unit
	}
	deletedProjectRuntime := createInstallRuntime(
		testProjectID, deletedProjectInstall.ID, "deleted",
	)
	survivingRuntime := createInstallRuntime(
		otherProject.ID, survivingInstall.ID, "surviving",
	)

	if _, err := store.Organizations().DeleteProject(
		ctx,
		testOrgID,
		testProjectID,
		identitystore.NewUserPrincipal(admin.ID),
	); err != nil {
		t.Fatalf("delete project using shared app: %v", err)
	}

	var appState string
	var appDeleted, deletedInstall, survivingInstallDeleted bool
	if err := pool.QueryRow(ctx, `
SELECT app.state, app.deleted_at IS NOT NULL,
       deleted_install.deleted_at IS NOT NULL,
       surviving_install.deleted_at IS NOT NULL
FROM integration_apps app
JOIN integration_installs deleted_install ON deleted_install.id = $2
JOIN integration_installs surviving_install ON surviving_install.id = $3
WHERE app.id = $1
`, app.ID, deletedProjectInstall.ID, survivingInstall.ID).Scan(
		&appState,
		&appDeleted,
		&deletedInstall,
		&survivingInstallDeleted,
	); err != nil {
		t.Fatalf("load shared app project lifecycle: %v", err)
	}
	if appState != "active" || appDeleted || !deletedInstall || survivingInstallDeleted {
		t.Fatalf(
			"shared lifecycle app=%q/%t deleted_install=%t surviving_install=%t",
			appState, appDeleted, deletedInstall, survivingInstallDeleted,
		)
	}
	assertRuntimeLifecycle := func(
		unitID integrationstore.ID,
		wantDesired string,
		wantDeleted bool,
	) {
		t.Helper()
		var desired string
		var deleted bool
		if err := pool.QueryRow(
			ctx,
			`SELECT desired_state, deleted_at IS NOT NULL FROM integration_runtime_units WHERE id = $1`,
			unitID,
		).Scan(&desired, &deleted); err != nil {
			t.Fatalf("load shared runtime lifecycle: %v", err)
		}
		if desired != wantDesired || deleted != wantDeleted {
			t.Fatalf("shared runtime %s desired=%q deleted=%t", unitID, desired, deleted)
		}
	}
	assertRuntimeLifecycle(appRuntime.ID, "running", false)
	assertRuntimeLifecycle(deletedProjectRuntime.ID, "stopped", true)
	assertRuntimeLifecycle(survivingRuntime.ID, "running", false)
}

func TestIntegrationAppDeletionImmediatelyFencesLiveInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "app-delete-install-first@example.com")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, Provider: testChannelProvider, ProviderAppRef: "delete-install-first-app",
			DisplayName: "Delete install first", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create deletable app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID, Provider: testChannelProvider,
			IntegrationKind: "delete_test", ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "delete-first-tenant", ProviderAccountRef: "delete-first-account",
		},
	)
	if err != nil {
		t.Fatalf("create app install: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE integration_apps
SET state = 'disabled', deleted_at = statement_timestamp()
WHERE id = $1
`, app.ID); err != nil {
		t.Fatalf("delete app with live install: %v", err)
	}
	var state string
	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT state, deleted_at IS NOT NULL FROM integration_apps WHERE id = $1`,
		app.ID,
	).Scan(&state, &deleted); err != nil {
		t.Fatalf("load deleted app: %v", err)
	}
	if state != "disabled" || !deleted {
		t.Fatalf("deleted app state=%q deleted=%t", state, deleted)
	}
	if _, err := store.Integrations().GetConnectorIntegrationApp(
		ctx,
		app.ID,
		testChannelCapabilities(testChannelProvider),
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("load deleted connector app error = %v, want not found", err)
	}
	if _, err := store.Integrations().GetConnectorIntegrationInstallByID(
		ctx,
		app.ID,
		install.ID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("load install through deleted app error = %v, want not found", err)
	}
	if err := store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID); err != nil {
		t.Fatalf("clean up fenced installation: %v", err)
	}
}

func TestSharedIntegrationAppCreationCannotRacePastOrganizationDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	createIntegrationProjectAdmin(t, ctx, store, "shared-app-org-delete-first@example.com")

	deletion, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin organization deletion: %v", err)
	}
	t.Cleanup(func() { _ = deletion.Rollback(ctx) })
	var deletionPID int32
	if err := deletion.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&deletionPID); err != nil {
		t.Fatalf("load organization deletion backend: %v", err)
	}
	if _, err := deletion.Exec(
		ctx,
		`UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1`,
		testOrgID,
	); err != nil {
		t.Fatalf("soft-delete organization: %v", err)
	}

	createDone := make(chan error, 1)
	go func() {
		_, err := store.Integrations().CreateIntegrationApp(
			context.Background(),
			integrationstore.CreateIntegrationAppInput{
				OrgID: testOrgID, Provider: testChannelProvider,
				ProviderAppRef: "org-delete-first-shared-app",
				DisplayName:    "Org delete first", ConnectorKey: testChannelConnector,
				State: integrationstore.IntegrationAppStateActive,
			},
		)
		createDone <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t, ctx, pool, "-- name: LockOrganizationLifecycleShared ", deletionPID,
	)
	if err := deletion.Commit(ctx); err != nil {
		t.Fatalf("commit organization deletion: %v", err)
	}
	select {
	case err := <-createDone:
		if !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("create shared app after organization deletion error = %v, want not found", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shared app creation did not finish after organization deletion")
	}
}

func TestOrganizationDeletionSweepsSharedAppCreationThatStartedFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "shared-app-org-create-first@example.com")
	credential, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg,
			Name:     "shared app organization credential",
			Material: secrets.GenericMaterial{Value: "provider-token"},
			Actor:    identitystore.NewUserPrincipal(admin.ID),
		},
	)
	if err != nil {
		t.Fatalf("create shared app credential: %v", err)
	}
	secretBlocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin shared app credential blocker: %v", err)
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
		credential.ID,
	); err != nil {
		t.Fatalf("lock shared app credential: %v", err)
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
				OrgID: testOrgID, Provider: testChannelProvider,
				ProviderAppRef: "org-create-first-shared-app",
				DisplayName:    "Org create first", ConnectorKey: testChannelConnector,
				CredentialSecretID: credential.ID,
				State:              integrationstore.IntegrationAppStateActive,
			},
		)
		createDone <- createResult{app: app, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t, ctx, pool, "-- name: InsertIntegrationApp ", blockerPID,
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
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteOrganization", 1)
	if err := secretBlocker.Commit(ctx); err != nil {
		t.Fatalf("release shared app credential: %v", err)
	}

	var created integrationstore.IntegrationAppRecord
	select {
	case result := <-createDone:
		if result.err != nil {
			t.Fatalf("create shared app before organization deletion: %v", result.err)
		}
		created = result.app
	case <-time.After(5 * time.Second):
		t.Fatal("shared app creation did not finish after credential release")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete organization after shared app creation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("organization deletion did not finish after shared app creation")
	}
	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL FROM integration_apps WHERE id = $1`,
		created.ID,
	).Scan(&deleted); err != nil {
		t.Fatalf("load raced shared app: %v", err)
	}
	if !deleted {
		t.Fatal("organization deletion did not sweep the concurrently created shared app")
	}
}

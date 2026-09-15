//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestOrganizationDeletionRetainsRevokedIntegrationInstaller(t *testing.T) {
	t.Parallel()
	for _, retired := range []bool{false, true} {
		name := "active installation"
		if retired {
			name = "previously removed installation and revoked key"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			user, _, app, install := createChannelLifecycleFixture(t, ctx, store, "installer-org-delete")
			key, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
				OrgID: testOrgID, CreatedByUserID: user.ID, Name: "Installer", OrgRole: "member",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
				OrgID: testOrgID, KeyID: key.Record.ID, ProjectID: testProjectID, Role: "developer",
			}); err != nil {
				t.Fatal(err)
			}
			principal, err := store.Identity().AuthenticateOrgAPIKey(ctx, key.Token)
			if err != nil {
				t.Fatal(err)
			}
			install, err = store.Integrations().UpsertIntegrationInstall(ctx, integrationstore.UpsertIntegrationInstallInput{
				OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
				InstalledBy: principal, Provider: app.Provider, IntegrationKind: install.IntegrationKind,
				ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
				ProviderTenantID: install.ProviderTenantID, ProviderAccountRef: install.ProviderAccountRef,
			})
			if err != nil {
				t.Fatal(err)
			}
			var originalRevokedAt *time.Time
			if retired {
				if err := store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID); err != nil {
					t.Fatal(err)
				}
				record, err := store.Identity().RevokeOrgAPIKey(ctx, testOrgID, key.Record.ID, identitystore.PrincipalRecord{})
				if err != nil {
					t.Fatal(err)
				}
				originalRevokedAt = record.RevokedAt
			}
			if _, err := store.Organizations().DeleteOrganization(
				ctx, testOrgID, identitystore.NewUserPrincipal(user.ID)); err != nil {
				t.Fatalf("delete organization with retained installer: %v", err)
			}
			if _, err := store.Identity().AuthenticateOrgAPIKey(ctx, key.Token); !errors.Is(err, storeerr.ErrUnauthorized) {
				t.Fatalf("deleted organization key authentication = %v, want unauthorized", err)
			}
			var revokedAt time.Time
			var installedBy uuid.UUID
			var deleted bool
			var memberships int
			if err := pool.QueryRow(ctx, `
SELECT key.revoked_at, install.installed_by_org_api_key_id, install.deleted_at IS NOT NULL,
       (SELECT count(*) FROM org_memberships WHERE org_id = key.org_id AND org_api_key_id = key.id)
FROM integration_installs install
JOIN org_api_keys key ON key.org_id = install.org_id AND key.id = install.installed_by_org_api_key_id
WHERE install.id = $1`, install.ID).Scan(&revokedAt, &installedBy, &deleted, &memberships); err != nil {
				t.Fatalf("load retained installer and installation: %v", err)
			}
			if installedBy != key.Record.ID || !deleted || memberships != 0 {
				t.Fatalf("installer=%s deleted=%t memberships=%d", installedBy, deleted, memberships)
			}
			if originalRevokedAt != nil && !revokedAt.Equal(*originalRevokedAt) {
				t.Fatalf("original revocation time changed: %v -> %v", originalRevokedAt, revokedAt)
			}
		})
	}
}

func TestIntegrationInstallerAPIKeyAuthorityAndAttribution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user, _, app, _ := createChannelLifecycleFixture(t, ctx, store, "installer-key")
	key, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID: testOrgID, CreatedByUserID: user.ID, Name: "Installer key", OrgRole: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := store.Identity().AuthenticateOrgAPIKey(ctx, key.Token)
	if err != nil {
		t.Fatal(err)
	}
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
		InstalledBy: principal, Provider: app.Provider,
		IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
		State:            integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: "key-tenant", ProviderAccountRef: "key-account",
	}
	assertUnauthorized := func(input integrationstore.UpsertIntegrationInstallInput) {
		t.Helper()
		if _, err := store.Integrations().UpsertIntegrationInstall(ctx, input); !errors.Is(err, storeerr.ErrUnauthorized) {
			t.Fatalf("install error = %v, want unauthorized", err)
		}
	}
	assertUnauthorized(input) // Organization membership alone does not grant this project.
	grant := identitystore.OrgAPIKeyProjectRoleInput{
		OrgID: testOrgID, KeyID: key.Record.ID, ProjectID: testProjectID, Role: "viewer",
	}
	if _, err := store.Identity().SetOrgAPIKeyProjectRole(ctx, grant); err != nil {
		t.Fatal(err)
	}
	assertUnauthorized(input)
	grant.Role = "developer"
	if _, err := store.Identity().SetOrgAPIKeyProjectRole(ctx, grant); err != nil {
		t.Fatal(err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
	if err != nil {
		t.Fatalf("authorized key install: %v", err)
	}
	assertPrincipal := func(record integrationstore.IntegrationInstallRecord, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record.InstalledBy, principal) {
			t.Fatalf("installer = %+v, want API key %+v", record.InstalledBy, principal)
		}
	}
	assertPrincipal(install, nil)
	assertPrincipal(store.Integrations().GetIntegrationInstall(ctx, testProjectID, install.ID))
	assertPrincipal(store.Integrations().GetIntegrationInstallByID(ctx, install.ID))
	assertPrincipal(store.Integrations().GetConnectorIntegrationInstall(
		ctx, app.ID, input.ProviderTenantID, input.ProviderAccountRef,
	))
	assertPrincipal(store.Integrations().GetConnectorIntegrationInstallByID(ctx, app.ID, install.ID))
	page, err := store.Integrations().ListIntegrationInstallsForProject(
		ctx,
		integrationstore.ListIntegrationInstallsForProjectInput{
			ProjectID: testProjectID, Limit: 10, List: listing.Options{SortField: "created_at", SortDesc: true},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range page.Installs {
		if record.ID == install.ID {
			assertPrincipal(record, nil)
			found = true
		}
	}
	if !found {
		t.Fatal("API-key installation missing from list")
	}
	var userID, keyID *uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT installed_by_user_id, installed_by_org_api_key_id FROM integration_installs WHERE id = $1
`, install.ID).Scan(&userID, &keyID); err != nil {
		t.Fatal(err)
	}
	if userID != nil || keyID == nil || *keyID != key.Record.ID {
		t.Fatalf("stored attribution user=%v key=%v", userID, keyID)
	}

	otherProject, err := store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(user.ID), Name: "Other installer project",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherInput := input
	otherInput.ProjectID = otherProject.ID
	assertUnauthorized(otherInput)
	otherInput = input
	otherInput.InstalledBy = identitystore.NewChannelConnectorPrincipal(
		"gateway", []channelconnector.Capability{{ConnectorKey: app.ConnectorKey, Provider: app.Provider}},
	)
	assertUnauthorized(otherInput)

	// Reinstallation deliberately updates attribution to the latest authorized
	// installer, while always clearing the other principal reference.
	userInput := input
	userInput.InstalledBy = identitystore.NewUserPrincipal(user.ID)
	updated, err := store.Integrations().UpsertIntegrationInstall(ctx, userInput)
	if err != nil || !reflect.DeepEqual(updated.InstalledBy, userInput.InstalledBy) {
		t.Fatalf("user reinstall = %+v, %v", updated.InstalledBy, err)
	}
	assertPrincipal(store.Integrations().UpsertIntegrationInstall(ctx, input))
	if _, err := store.Identity().RevokeOrgAPIKey(
		ctx, testOrgID, key.Record.ID, identitystore.PrincipalRecord{},
	); err != nil {
		t.Fatalf("revoke installer: %v", err)
	}
	assertUnauthorized(input)
	assertPrincipal(store.Integrations().GetIntegrationInstall(ctx, testProjectID, install.ID))
}

func TestIntegrationInstallerDatabasePrincipalConstraints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user, _, _, install := createChannelLifecycleFixture(t, ctx, store, "installer-constraints")
	otherOrg, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: user.ID, Name: "Other installer org", IdempotencyKey: "other-installer-org",
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID: otherOrg.Org.ID, CreatedByUserID: user.ID, Name: "Other org key", OrgRole: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		userID, keyID *uuid.UUID
		code          string
	}{
		{name: "missing", code: "23514"},
		{name: "both", userID: &user.ID, keyID: &key.Record.ID, code: "23514"},
		{name: "foreign org key", keyID: &key.Record.ID, code: "23503"},
	} {
		_, err := pool.Exec(ctx, `
UPDATE integration_installs SET installed_by_user_id = $2, installed_by_org_api_key_id = $3 WHERE id = $1
`, install.ID, tc.userID, tc.keyID)
		if !isPgCode(err, tc.code) {
			t.Fatalf("%s principal constraint error = %v, want %s", tc.name, err, tc.code)
		}
	}
	// Claiming the local organization does not change the real key's owner.
	forged := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: install.IntegrationAppID,
		InstalledBy: identitystore.NewOrgAPIKeyPrincipal(testOrgID, key.Record.ID), Provider: install.Provider,
		IntegrationKind: install.IntegrationKind, ConnectionMode: install.ConnectionMode, State: install.State,
		ProviderTenantID: install.ProviderTenantID, ProviderAccountRef: install.ProviderAccountRef,
	}
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, forged); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("forged key organization error = %v, want unauthorized", err)
	}
}

func TestIntegrationInstallerRechecksAPIKeyAfterRevocationWait(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user, _, app, _ := createChannelLifecycleFixture(t, ctx, store, "installer-revocation")
	key, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID: testOrgID, CreatedByUserID: user.ID, Name: "Revoked installer", OrgRole: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blockingTx.Exec(ctx, `SELECT id FROM org_api_keys WHERE id = $1 FOR UPDATE`, key.Record.ID); err != nil {
		t.Fatal(err)
	}
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
		InstalledBy: identitystore.NewOrgAPIKeyPrincipal(testOrgID, key.Record.ID), Provider: app.Provider,
		IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
		State:            integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: "revoked-key-tenant", ProviderAccountRef: "revoked-key-account",
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
		done <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "-- name: LockOrgAPIKeyForUpdate", blockingPID)
	// Commit the same principal and membership retirement as RevokeOrgAPIKey,
	// after the installation is waiting on that key's lifecycle lock.
	if _, err := blockingTx.Exec(ctx, `
UPDATE org_api_keys SET revoked_at = statement_timestamp() WHERE id = $1
`, key.Record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := blockingTx.Exec(ctx, `DELETE FROM org_memberships WHERE org_api_key_id = $1`, key.Record.ID); err != nil {
		t.Fatal(err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("install after revocation error = %v, want unauthorized", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM integration_installs WHERE integration_app_id = $1 AND provider_account_ref = $2
`, app.ID, input.ProviderAccountRef).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("installations committed after installer revocation = %d", count)
	}
}

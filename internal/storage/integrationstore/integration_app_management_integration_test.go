//go:build integration

package integrationstore_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAppManagementInventoryAndProjectEligibility(t *testing.T) {
	t.Parallel()
	f := newIntegrationAppManagementFixture(t)
	shared := f.app(t, uuid.Nil, uuid.Nil)
	owned := f.app(t, testProjectID, uuid.Nil)
	other := f.app(t, f.otherProjectID, uuid.Nil)
	disabled := f.app(t, uuid.Nil, uuid.Nil)
	state := integrationstore.IntegrationAppStateDisabled
	_, err := f.store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
		OrgID: testOrgID, ID: disabled.ID, State: &state,
	})
	require.NoError(t, err)
	deleted := f.app(t, testProjectID, uuid.Nil)
	_, err = f.store.pool.Exec(t.Context(), `UPDATE integration_apps
SET state='disabled', deleted_at=statement_timestamp() WHERE id=$1`, deleted.ID)
	require.NoError(t, err)
	foreignOrg, err := f.store.Organizations().CreateOrgForUser(t.Context(), orglifecycle.CreateOrgForUserInput{
		UserID: f.adminID, Name: "app-inventory-other", IdempotencyKey: "app-inventory-other",
	})
	require.NoError(t, err)
	foreign, err := f.store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
		OrgID: foreignOrg.Org.ID, Provider: testChannelProvider, ProviderAppRef: "foreign-app",
		ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	for _, test := range []struct {
		org, project uuid.UUID
		want         []uuid.UUID
	}{
		{testOrgID, uuid.Nil, []uuid.UUID{shared.ID, owned.ID, other.ID, disabled.ID}},
		{testOrgID, testProjectID, []uuid.UUID{shared.ID, owned.ID}},
		{testOrgID, f.otherProjectID, []uuid.UUID{shared.ID, other.ID}},
		{foreignOrg.Org.ID, uuid.Nil, []uuid.UUID{foreign.ID}},
	} {
		page, err := f.store.Integrations().ListIntegrationApps(t.Context(), integrationstore.ListIntegrationAppsInput{
			OrgID: test.org, EligibleProjectID: test.project, Limit: 100,
		})
		require.NoError(t, err)
		ids := make([]uuid.UUID, 0, len(page.Apps))
		for _, app := range page.Apps {
			ids = append(ids, app.ID)
			require.Equal(t, test.org, app.OrgID)
			if test.project != uuid.Nil {
				require.Equal(t, integrationstore.IntegrationAppStateActive, app.State)
				require.Contains(t, []uuid.UUID{uuid.Nil, test.project}, app.OwnerProjectID)
			}
			if app.ID == disabled.ID {
				require.Equal(t, integrationstore.IntegrationAppStateDisabled, app.State)
			}
		}
		require.ElementsMatch(t, test.want, ids)
		require.False(t, page.Next.Set)
	}
	_, err = f.store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
		OrgID: foreignOrg.Org.ID, ID: owned.ID, State: &state,
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
		OrgID: testOrgID, ID: deleted.ID, State: &state,
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestIntegrationAppManagementCursorUsesCreationTimeAndID(t *testing.T) {
	t.Parallel()
	f := newIntegrationAppManagementFixture(t)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	created := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	// Creation time is immutable. Seed legitimate historical app rows sharing
	// one timestamp so paging must use the ID tie-breaker, not clock resolution.
	_, err := f.store.pool.Exec(t.Context(), `INSERT INTO integration_apps
(id, org_id, provider, provider_app_ref, connector_key, state, created_at, updated_at)
SELECT id, $1, $2, id::text, $3, 'active', $4, $4 FROM unnest($5::uuid[]) AS app(id)`,
		testOrgID, testChannelProvider, testChannelConnector, created, ids)
	require.NoError(t, err)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(b[:], a[:]) })
	input := integrationstore.ListIntegrationAppsInput{OrgID: testOrgID, Limit: 2}
	var actual []uuid.UUID
	for pageNumber := range 3 {
		page, err := f.store.Integrations().ListIntegrationApps(t.Context(), input)
		require.NoError(t, err)
		for _, app := range page.Apps {
			require.Equal(t, created, app.CreatedAt.UTC())
			actual = append(actual, app.ID)
		}
		if pageNumber < 2 {
			require.Len(t, page.Apps, 2)
			require.True(t, page.Next.Set)
			require.Equal(t, page.Apps[1].ID, page.Next.ID)
			require.Equal(t, created, page.Next.CreatedAt.UTC())
		} else {
			require.Len(t, page.Apps, 1)
			require.False(t, page.Next.Set)
		}
		input.After = page.Next
	}
	require.Equal(t, ids, actual, "same-time rows cannot be skipped or returned twice")
}

func TestIntegrationAppManagementPatchPreservesOtherFields(t *testing.T) {
	t.Parallel()
	f := newIntegrationAppManagementFixture(t)
	original, _ := f.secret(t, testProjectID, "original")
	replacement, _ := f.secret(t, testProjectID, "replacement")
	app := f.app(t, testProjectID, original.ID)
	name, state, clearedSecret := "  renamed app  ", integrationstore.IntegrationAppStateDisabled, uuid.Nil
	for _, test := range []struct {
		input integrationstore.UpdateIntegrationAppInput
		apply func(*integrationstore.IntegrationAppRecord)
	}{
		{integrationstore.UpdateIntegrationAppInput{DisplayName: &name},
			func(app *integrationstore.IntegrationAppRecord) { app.DisplayName = "renamed app" }},
		{integrationstore.UpdateIntegrationAppInput{ProviderConfig: json.RawMessage(`{"setting":"new"}`)},
			func(app *integrationstore.IntegrationAppRecord) {
				app.ProviderConfig = json.RawMessage(`{"setting": "new"}`)
			}},
		{integrationstore.UpdateIntegrationAppInput{CredentialSecretID: &replacement.ID},
			func(app *integrationstore.IntegrationAppRecord) { app.CredentialSecretID = replacement.ID }},
		{integrationstore.UpdateIntegrationAppInput{State: &state},
			func(app *integrationstore.IntegrationAppRecord) { app.State = state }},
		{integrationstore.UpdateIntegrationAppInput{CredentialSecretID: &clearedSecret},
			func(app *integrationstore.IntegrationAppRecord) { app.CredentialSecretID = uuid.Nil }},
		{integrationstore.UpdateIntegrationAppInput{ProviderConfig: json.RawMessage(`{}`)},
			func(app *integrationstore.IntegrationAppRecord) { app.ProviderConfig = json.RawMessage(`{}`) }},
	} {
		test.input.OrgID, test.input.ID = testOrgID, app.ID
		updated, err := f.store.Integrations().UpdateIntegrationApp(t.Context(), test.input)
		require.NoError(t, err)
		require.True(t, updated.UpdatedAt.After(app.UpdatedAt))
		want := app
		test.apply(&want)
		want.ConfigurationRevision++
		want.UpdatedAt = updated.UpdatedAt
		require.Equal(t, want, updated, "only the supplied field and revision/timestamp may change")
		app = updated
	}
	noOp, err := f.store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
		OrgID: testOrgID, ID: app.ID,
	})
	require.NoError(t, err)
	require.Equal(t, app, noOp, "omitting configuration and credentials must preserve them")
}

func TestIntegrationAppManagementConcurrentPatchesMerge(t *testing.T) {
	t.Parallel()
	f := newIntegrationAppManagementFixture(t)
	app := f.app(t, testProjectID, uuid.Nil)
	ctx := t.Context()
	control := integrationdb.BeginTx(t, ctx, f.store.pool)
	_, err := control.Exec(ctx, `SELECT id FROM integration_apps WHERE id=$1 FOR UPDATE`, app.ID)
	require.NoError(t, err)
	name := "concurrent name"
	inputs := []integrationstore.UpdateIntegrationAppInput{
		{OrgID: testOrgID, ID: app.ID, DisplayName: &name},
		{OrgID: testOrgID, ID: app.ID, ProviderConfig: json.RawMessage(`{"concurrent":true}`)},
	}
	done := make([]<-chan integrationdb.AsyncResult[integrationstore.IntegrationAppRecord], len(inputs))
	for i := range inputs {
		done[i] = integrationdb.RunAsync(func() (integrationstore.IntegrationAppRecord, error) {
			return f.store.Integrations().UpdateIntegrationApp(ctx, inputs[i])
		})
	}
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "UpdateIntegrationApp", 2)
	require.NoError(t, control.Commit(ctx))
	for _, result := range done {
		integrationdb.AwaitSuccess(t, result, "independent app patch")
	}
	updated := f.get(t, app.ID)
	require.Equal(t, name, updated.DisplayName)
	require.JSONEq(t, `{"concurrent":true}`, string(updated.ProviderConfig))
	require.Equal(t, app.ConfigurationRevision+2, updated.ConfigurationRevision)
	require.Equal(t, app.ProviderMetadata, updated.ProviderMetadata)
	require.Equal(t, app.OwnerProjectID, updated.OwnerProjectID)
}

func TestIntegrationAppManagementRejectsWrongAndDeletedSecrets(t *testing.T) {
	t.Parallel()
	f := newIntegrationAppManagementFixture(t)
	orgSecret, _ := f.secret(t, uuid.Nil, "organization")
	projectSecret, _ := f.secret(t, testProjectID, "project")
	otherSecret, _ := f.secret(t, f.otherProjectID, "other-project")
	deleted, _ := f.secret(t, testProjectID, "deleted")
	_, err := f.store.Secrets().DeleteSecret(t.Context(), secretstore.DeleteSecretInput{
		OrgID: testOrgID, SecretID: deleted.ID, Actor: identitystore.NewUserPrincipal(f.adminID),
	})
	require.NoError(t, err)
	shared := f.app(t, uuid.Nil, orgSecret.ID)
	owned := f.app(t, testProjectID, projectSecret.ID)
	for _, test := range []struct {
		app    integrationstore.IntegrationAppRecord
		secret uuid.UUID
	}{
		{shared, projectSecret.ID}, {owned, orgSecret.ID}, {owned, otherSecret.ID}, {owned, deleted.ID}, {owned, uuid.New()},
	} {
		name := "must roll back"
		_, err := f.store.Integrations().UpdateIntegrationApp(t.Context(), integrationstore.UpdateIntegrationAppInput{
			OrgID: testOrgID, ID: test.app.ID, CredentialSecretID: &test.secret, DisplayName: &name,
		})
		require.ErrorIs(t, err, storeerr.ErrNotFound)
		require.Equal(t, test.app, f.get(t, test.app.ID))
	}
}

func TestIntegrationAppManagementSecretDeletionSerializesWithPatch(t *testing.T) {
	t.Parallel()
	for _, referenceWins := range []bool{false, true} {
		name := "deletion_wins"
		if referenceWins {
			name = "patch_wins"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationAppManagementFixture(t)
			ctx := t.Context()
			original, _ := f.secret(t, testProjectID, "original")
			replacement, version := f.secret(t, testProjectID, "replacement")
			app := f.app(t, testProjectID, original.ID)
			patch := func() (integrationstore.IntegrationAppRecord, error) {
				return f.store.Integrations().UpdateIntegrationApp(ctx, integrationstore.UpdateIntegrationAppInput{
					OrgID: testOrgID, ID: app.ID, CredentialSecretID: &replacement.ID,
				})
			}
			remove := func() error {
				_, err := f.store.Secrets().DeleteSecretOnceForIntegration(ctx, secretstore.DeleteSecretInput{
					OrgID: testOrgID, SecretID: replacement.ID, Actor: identitystore.NewUserPrincipal(f.adminID),
				})
				return err
			}
			control := integrationdb.BeginTx(t, ctx, f.store.pool)
			if referenceWins {
				_, err := control.Exec(ctx, `LOCK TABLE integration_apps IN SHARE MODE`)
				require.NoError(t, err)
				patched := integrationdb.RunAsync(patch)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "UpdateIntegrationApp", 1)
				deleted := integrationdb.RunAsyncError(remove)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockSecret", 1)
				require.NoError(t, control.Commit(ctx))
				updated := integrationdb.AwaitSuccess(t, patched, "app credential reference")
				require.Equal(t, replacement.ID, updated.CredentialSecretID)
				require.ErrorIs(t, integrationdb.Await(t, deleted, "referenced credential deletion"), storeerr.ErrConflict)
			} else {
				_, err := control.Exec(ctx, `SELECT id FROM secret_versions WHERE id=$1 FOR UPDATE`, version.ID)
				require.NoError(t, err)
				deleted := integrationdb.RunAsyncError(remove)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "DeleteSecretVersions", 1)
				patched := integrationdb.RunAsync(patch)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockSecretForReference", 1)
				require.NoError(t, control.Commit(ctx))
				require.NoError(t, integrationdb.Await(t, deleted, "unreferenced credential deletion"))
				result := integrationdb.Await(t, patched, "patch after credential deletion")
				require.ErrorIs(t, result.Err, storeerr.ErrNotFound)
				require.Equal(t, app, f.get(t, app.ID), "failed reassociation preserves the original credential and revision")
			}
		})
	}
}

func TestIntegrationAppManagementSecretRotationAndPatchLockOrder(t *testing.T) {
	t.Parallel()
	for _, setCredential := range []bool{false, true} {
		name := "metadata_only"
		if setCredential {
			name = "explicit_credential"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationAppManagementFixture(t)
			ctx := t.Context()
			credential, version := f.secret(t, testProjectID, "rotating")
			app := f.app(t, testProjectID, credential.ID)
			control := integrationdb.BeginTx(t, ctx, f.store.pool)
			_, err := control.Exec(ctx, `LOCK TABLE integration_apps IN SHARE MODE`)
			require.NoError(t, err)
			rotated := integrationdb.RunAsyncError(func() error {
				_, _, err := f.store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
					OrgID: testOrgID, SecretID: credential.ID, Material: secrets.GenericMaterial{Value: "rotated"},
					Actor: identitystore.NewUserPrincipal(f.adminID),
				})
				return err
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "SetSecretCurrentVersion", 1)
			newName := "renamed during rotation"
			input := integrationstore.UpdateIntegrationAppInput{OrgID: testOrgID, ID: app.ID, DisplayName: &newName}
			query := "UpdateIntegrationApp"
			if setCredential {
				input.CredentialSecretID = &credential.ID
				query = "LockSecretForReference"
			}
			patched := integrationdb.RunAsync(func() (integrationstore.IntegrationAppRecord, error) {
				return f.store.Integrations().UpdateIntegrationApp(ctx, input)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, query, 1)
			require.NoError(t, control.Commit(ctx))
			require.NoError(t, integrationdb.Await(t, rotated, "secret rotation"))
			integrationdb.AwaitSuccess(t, patched, "app patch during rotation")
			updated := f.get(t, app.ID)
			require.Equal(t, newName, updated.DisplayName)
			require.Equal(t, credential.ID, updated.CredentialSecretID)
			require.Equal(t, app.ConfigurationRevision+2, updated.ConfigurationRevision)
			current, err := f.store.Secrets().GetSecret(ctx, testOrgID, credential.ID)
			require.NoError(t, err)
			require.NotEqual(t, version.ID, current.CurrentVersionID)
		})
	}
}

func TestIntegrationAppManagementIdentityRemainsImmutable(t *testing.T) {
	t.Parallel()
	f := newIntegrationAppManagementFixture(t)
	app := f.app(t, testProjectID, uuid.Nil)
	for _, test := range []struct {
		column string
		value  any
	}{
		{"id", uuid.New()}, {"org_id", uuid.New()}, {"owner_project_id", f.otherProjectID},
		{"provider", "github"}, {"provider_app_ref", "different"}, {"connector_key", "different"},
		{"installation_credential_kind", string(secrets.KindIntegrationCredentials)},
		{"created_at", app.CreatedAt.Add(time.Hour)},
	} {
		_, err := f.store.pool.Exec(t.Context(), "UPDATE integration_apps SET "+test.column+"=$1 WHERE id=$2",
			test.value, app.ID)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, test.column)
		require.Equal(t, "25006", pgErr.Code, test.column)
		require.Equal(t, app, f.get(t, app.ID))
	}
}

type integrationAppManagementFixture struct {
	store                   *Store
	adminID, otherProjectID uuid.UUID
}

func newIntegrationAppManagementFixture(t *testing.T) integrationAppManagementFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "app-management@example.com")
	project, err := store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(admin.ID),
		Name: "App management other project", IdempotencyKey: "app-management-other",
	})
	require.NoError(t, err)
	return integrationAppManagementFixture{store: store, adminID: admin.ID, otherProjectID: project.ID}
}

func (f integrationAppManagementFixture) app(
	t *testing.T, ownerProjectID, credentialID uuid.UUID,
) integrationstore.IntegrationAppRecord {
	t.Helper()
	app, err := f.store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, OwnerProjectID: ownerProjectID, Provider: testChannelProvider,
		ProviderAppRef: uuid.NewString(), DisplayName: "Original app", ConnectorKey: testChannelConnector,
		CredentialSecretID: credentialID, State: integrationstore.IntegrationAppStateActive,
		ProviderConfig: json.RawMessage(`{"original":true}`), ProviderMetadata: json.RawMessage(`{"preserved":true}`),
	})
	require.NoError(t, err)
	return app
}

func (f integrationAppManagementFixture) secret(
	t *testing.T, projectID uuid.UUID, name string,
) (secretstore.SecretRecord, secretstore.SecretVersionRecord) {
	t.Helper()
	owner := secretstore.SecretOwnerProject
	if projectID == uuid.Nil {
		owner = secretstore.SecretOwnerOrg
	}
	secret, version, err := f.store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: owner, OwnerProjectID: projectID, Name: name,
		Material: secrets.GenericMaterial{Value: "local-test"}, Actor: identitystore.NewUserPrincipal(f.adminID),
	})
	require.NoError(t, err)
	return secret, version
}

func (f integrationAppManagementFixture) get(t *testing.T, id uuid.UUID) integrationstore.IntegrationAppRecord {
	t.Helper()
	app, err := f.store.Integrations().GetIntegrationApp(t.Context(), testOrgID, id)
	require.NoError(t, err)
	return app
}

//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func projectAppSetupFixture(
	t *testing.T,
) (inboxFixture, *secretstore.Store, integrationstore.ConfigureProjectAppInput) {
	t.Helper()
	f := newInboxFixture(t)
	f.exec(t, `INSERT INTO org_memberships(org_id,user_id,role,created_at) VALUES($1,$2,'owner',now())`, f.org, f.user)
	wrapper, err := secrets.NewLocalKeyWrapper("app-test", map[string][]byte{
		"app-test": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	secretStore := secretstore.New(f.pool, wrapper, identitystore.New(f.pool, wrapper, nil))
	credential, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
		Name: "app-fixture", Actor: identitystore.NewUserPrincipal(f.user),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-fixture", ClientID: "client", ClientSecret: "client-secret", SigningSecret: "signing-secret",
		},
	})
	require.NoError(t, err)
	app, err := f.store.CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "setup", DefinitionID: appdefinition.Slack,
	})
	require.NoError(t, err)
	return f, secretStore, integrationstore.ConfigureProjectAppInput{
		OrgID: f.org, ProjectID: f.project, AppID: app.ID, InstalledByUserID: f.user,
		Provider: integrationstore.IntegrationProviderSlack, ProviderTenantID: "T123", ProviderAccountRef: "router",
		CredentialSecretID: credential.ID, CredentialVersionID: version.ID,
		ExpectedSetupRevision: app.SetupRevision, OAuthFlowID: uuid.Must(uuid.NewV7()),
	}
}

func projectAppMetadata(app integrationstore.ProjectAppRecord) integrationstore.SaveProjectAppInput {
	return integrationstore.SaveProjectAppInput{
		OrgID: app.OrgID, ProjectID: app.ProjectID, Name: app.Name, DefinitionID: app.DefinitionID, Settings: app.Settings,
	}
}

func TestProjectAppSetupLifecycleAndOwnership(t *testing.T) {
	t.Parallel()
	f, secretStore, input := projectAppSetupFixture(t)
	created, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, input.AppID, created.ID)
	require.Equal(t, input.CredentialSecretID, created.CredentialSecretID)
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "the callback is pinned to the original setup revision")
	input.ExpectedSetupRevision = created.SetupRevision

	second := uuid.New()
	storagefixture.InsertProject(t, f.ctx, f.pool, f.org, second, "other", "other", time.Now())
	foreign := input
	foreign.ProjectID = second
	_, err = f.store.GetProjectApp(f.ctx, second, created.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.store.ConfigureProjectApp(f.ctx, foreign)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	other, err := f.store.CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: second, Name: created.Name, DefinitionID: created.DefinitionID,
	})
	require.NoError(t, err)
	_, err = secretStore.CreateSecretGrant(f.ctx, secretstore.CreateSecretGrantInput{
		OrgID:           f.org,
		SecretID:        input.CredentialSecretID,
		TargetProjectID: second,
		Actor:           identitystore.NewUserPrincipal(f.user),
	})
	require.NoError(t, err)
	foreign.AppID, foreign.ExpectedSetupRevision, foreign.OAuthFlowID = other.ID, other.SetupRevision, uuid.Must(
		uuid.NewV7(),
	)
	other, err = f.store.ConfigureProjectApp(f.ctx, foreign)
	require.NoError(t, err, "another project independently owns the same physical bot")
	resolved, err := f.store.ListProjectAppsByProviderIdentity(
		f.ctx,
		input.Provider,
		input.ProviderTenantID,
		input.ProviderAccountRef,
		uuid.Nil,
		100,
	)
	require.NoError(t, err)
	require.Len(t, resolved, 2)
	require.ElementsMatch(t, []uuid.UUID{created.ID, other.ID}, []uuid.UUID{resolved[0].ID, resolved[1].ID})
	changed := input
	changed.ProviderAccountRef = "different"
	_, err = f.store.ConfigureProjectApp(f.ctx, changed)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	applied, err := f.store.DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: f.project, AppID: created.ID, ExpectedSetupRevision: &created.SetupRevision,
	})
	require.NoError(t, err)
	require.True(t, applied)
	updated, err := f.store.GetProjectApp(f.ctx, f.project, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.CreatedAt, updated.CreatedAt)
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, updated.State)
	require.Equal(t, created.SetupRevision+1, updated.SetupRevision)
	unaffected, err := f.store.GetProjectApp(f.ctx, second, other.ID)
	require.NoError(t, err)
	require.Equal(t, other, unaffected)
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, created.ID))
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	replacement, err := f.store.CreateProjectApp(f.ctx, projectAppMetadata(created))
	require.NoError(t, err, "a deleted app name may be reused with a new identity")
	require.NotEqual(t, created.ID, replacement.ID)
	input.AppID, input.ExpectedSetupRevision, input.OAuthFlowID = replacement.ID, replacement.SetupRevision, uuid.Must(
		uuid.NewV7(),
	)
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
}

func TestProjectAppProviderConstraints(t *testing.T) {
	t.Parallel()
	f, _, input := projectAppSetupFixture(t)
	_, err := f.pool.Exec(f.ctx, `UPDATE project_apps SET provider='discord' WHERE id=$1`, input.AppID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "25006", pgErr.Code, "the database also prevents changing the saved app provider")
	_, err = f.pool.Exec(
		f.ctx,
		`INSERT INTO project_apps(org_id,project_id,name,definition_id,provider,state,created_at,updated_at)
        VALUES($1,$2,'invalid','omnara.slack','unknown','disconnected',now(),now())`,
		f.org,
		f.project,
	)
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code, "unknown providers cannot be persisted")
}

func TestProjectAppCredentialAuthorizationAndRotation(t *testing.T) {
	t.Parallel()
	f, secretStore, input := projectAppSetupFixture(t)
	other := uuid.New()
	storagefixture.InsertProject(t, f.ctx, f.pool, f.org, other, "credential-owner", "credential-owner", time.Now())
	actor := identitystore.NewUserPrincipal(f.user)
	material := secrets.GitHubAppCredentialsMaterial{
		AppID:         "123",
		PrivateKey:    "test-key-validated-by-caller",
		WebhookSecret: "test-webhook",
	}
	credential, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: other,
		Name: "github", Actor: actor, Material: material,
	})
	require.NoError(t, err)
	app, err := f.store.CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "github", DefinitionID: appdefinition.GitHub,
	})
	require.NoError(t, err)
	input.AppID, input.ExpectedSetupRevision = app.ID, app.SetupRevision
	input.Provider = integrationstore.IntegrationProviderGitHub
	input.ProviderTenantID, input.ProviderAccountRef = "123", "456"
	input.CredentialSecretID, input.CredentialVersionID, input.CredentialAppID = credential.ID, version.ID, 123
	input.OAuthFlowID = uuid.Nil
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	grant, err := secretStore.CreateSecretGrant(f.ctx, secretstore.CreateSecretGrantInput{
		OrgID: f.org, SecretID: credential.ID, TargetProjectID: f.project, Actor: actor,
	})
	require.NoError(t, err)
	saved, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	input.ExpectedSetupRevision = saved.SetupRevision
	_, newVersion, err := secretStore.CreateSecretVersion(f.ctx, secretstore.CreateSecretVersionInput{
		OrgID: f.org, SecretID: credential.ID, Material: material, Actor: actor,
	})
	require.NoError(t, err)
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "rotation invalidates the verified credential version")
	current, err := f.store.GetProjectApp(f.ctx, f.project, saved.ID)
	require.NoError(t, err)
	require.Equal(t, saved, current)
	input.CredentialVersionID = newVersion.ID
	saved, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	input.ExpectedSetupRevision = saved.SetupRevision
	_, err = secretStore.DeleteSecretGrant(f.ctx, secretstore.DeleteSecretGrantInput{
		OrgID: f.org, SecretID: credential.ID, GrantID: grant.ID, Actor: actor,
	})
	require.NoError(t, err)
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, saved.ID))
}

func TestProjectAppMetadataPreservesConcurrentProviderObservations(t *testing.T) {
	t.Parallel()
	f, _, input := projectAppSetupFixture(t)
	saved, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	require.NoError(
		t,
		dbsqlc.New(tx).
			LockProjectAppLifecycleExclusive(f.ctx, dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: saved.ID}),
	)
	_, err = tx.Exec(f.ctx, `UPDATE project_apps SET provider_identity='{"observed":"new"}',
        provider_metadata='{"revision":2}',provider_agent_display_name='New label',
        setup_revision=setup_revision+1 WHERE id=$1`, saved.ID)
	require.NoError(t, err)
	done := integrationdb.RunAsync(func() (integrationstore.ProjectAppRecord, error) {
		return f.store.UpdateProjectApp(f.ctx, saved.ID, projectAppMetadata(saved))
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockProjectAppLifecycleShared", 1)
	require.NoError(t, tx.Commit(f.ctx))
	outcome := integrationdb.Await(t, done, "metadata update")
	require.NoError(t, outcome.Err)
	current := outcome.Value
	require.JSONEq(t, `{"observed":"new"}`, string(current.ProviderIdentity))
	require.JSONEq(t, `{"revision":2}`, string(current.ProviderMetadata))
	require.Equal(t, "New label", current.ProviderAgentDisplayName)
	require.Equal(t, saved.SetupRevision+1, current.SetupRevision)
}

func TestProjectAppCredentialRebindRequiresVerifiedSetup(t *testing.T) {
	t.Parallel()
	f, secretStore, input := projectAppSetupFixture(t)
	saved, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	credential, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
		Name: "replacement", Actor: identitystore.NewUserPrincipal(f.user),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-replacement", ClientID: "client", ClientSecret: "client-secret", SigningSecret: "signing-secret",
		},
	})
	require.NoError(t, err)
	changed := input
	changed.CredentialSecretID, changed.CredentialVersionID = credential.ID, version.ID
	changed.ExpectedSetupRevision, changed.OAuthFlowID = saved.SetupRevision, uuid.Nil
	_, err = f.store.ConfigureProjectApp(f.ctx, changed)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	current, err := f.store.GetProjectApp(f.ctx, f.project, saved.ID)
	require.NoError(t, err)
	require.Equal(t, saved, current)
	changed.OAuthFlowID = uuid.Must(uuid.NewV7())
	rebound, err := f.store.ConfigureProjectApp(f.ctx, changed)
	require.NoError(t, err)
	require.Equal(t, credential.ID, rebound.CredentialSecretID)
	input.ExpectedSetupRevision, input.OAuthFlowID = saved.SetupRevision, uuid.Must(uuid.NewV7())
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "old setup cannot restore replaced credentials")
	// A metadata save queued before another credential change preserves it.
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	require.NoError(
		t,
		dbsqlc.New(tx).
			LockProjectAppLifecycleExclusive(f.ctx, dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: saved.ID}),
	)
	_, err = tx.Exec(
		f.ctx,
		`UPDATE project_apps SET credential_secret_id=$2,setup_revision=setup_revision+1 WHERE id=$1`,
		saved.ID,
		input.CredentialSecretID,
	)
	require.NoError(t, err)
	done := integrationdb.RunAsync(func() (integrationstore.ProjectAppRecord, error) {
		return f.store.UpdateProjectApp(f.ctx, saved.ID, projectAppMetadata(rebound))
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockProjectAppLifecycleShared", 1)
	require.NoError(t, tx.Commit(f.ctx))
	result := integrationdb.Await(t, done, "metadata save after credential change")
	require.NoError(t, result.Err)
	require.Equal(t, input.CredentialSecretID, result.Value.CredentialSecretID)
	require.Equal(t, rebound.SetupRevision+1, result.Value.SetupRevision)
}

func TestProjectAppOAuthReplayCannotRecreateDeletedApp(t *testing.T) {
	t.Parallel()
	f, _, input := projectAppSetupFixture(t)
	first, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	other, err := f.store.CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "other", DefinitionID: appdefinition.Slack,
	})
	require.NoError(t, err)
	replay := input
	replay.AppID, replay.ExpectedSetupRevision = other.ID, other.SetupRevision
	_, err = f.store.ConfigureProjectApp(f.ctx, replay)
	require.ErrorIs(t, err, storeerr.ErrIntegrationOAuthFlowConsumed)
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, first.ID))
	_, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.store.ConfigureProjectApp(f.ctx, replay)
	require.ErrorIs(t, err, storeerr.ErrIntegrationOAuthFlowConsumed, "deleted apps still prevent cross-app OAuth replay")
}

func TestProjectAppUpdatesCannotRecreateConcurrentDeletion(t *testing.T) {
	t.Parallel()
	for _, setup := range []bool{false, true} {
		name := "metadata"
		if setup {
			name = "setup"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, _, input := projectAppSetupFixture(t)
			saved, err := f.store.ConfigureProjectApp(f.ctx, input)
			require.NoError(t, err)
			input.ExpectedSetupRevision, input.OAuthFlowID = saved.SetupRevision, uuid.Must(uuid.NewV7())
			tx := integrationdb.BeginTx(t, f.ctx, f.pool)
			q := dbsqlc.New(tx)
			require.NoError(
				t,
				q.LockProjectAppLifecycleExclusive(f.ctx, dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: saved.ID}),
			)
			done := integrationdb.RunAsync(func() (integrationstore.ProjectAppRecord, error) {
				if setup {
					return f.store.ConfigureProjectApp(f.ctx, input)
				}
				return f.store.UpdateProjectApp(f.ctx, saved.ID, projectAppMetadata(saved))
			})
			query := "LockProjectAppLifecycleShared"
			if setup {
				query = "LockProjectAppLifecycleExclusive"
			}
			integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, query, 1)
			rows, err := q.DeleteProjectApp(f.ctx, dbsqlc.DeleteProjectAppParams{ProjectID: f.project, ID: saved.ID})
			require.NoError(t, err)
			require.EqualValues(t, 1, rows)
			require.NoError(t, tx.Commit(f.ctx))
			result := integrationdb.Await(t, done, "app update after deletion")
			require.ErrorIs(t, result.Err, storeerr.ErrNotFound)
			found, err := f.store.ListProjectAppsByProviderIdentity(
				f.ctx,
				input.Provider,
				input.ProviderTenantID,
				input.ProviderAccountRef,
				uuid.Nil,
				100,
			)
			require.NoError(t, err)
			require.Empty(t, found)
		})
	}
}

func TestProjectAppDisplayNameRefreshSurvivesMetadataEdit(t *testing.T) {
	t.Parallel()
	f, _, input := projectAppSetupFixture(t)
	input.ProviderAgentDisplayName = "Observed name"
	saved, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	input.ProviderAgentDisplayName = "Refreshed name"
	input.ProviderMetadata = json.RawMessage(`{"revision":2}`)
	input.ExpectedSetupRevision, input.OAuthFlowID = saved.SetupRevision, uuid.Must(uuid.NewV7())
	refreshed, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, "Refreshed name", refreshed.ProviderAgentDisplayName)
	edited, err := f.store.UpdateProjectApp(f.ctx, saved.ID, projectAppMetadata(saved))
	require.NoError(t, err)
	require.Equal(t, refreshed.ProviderAgentDisplayName, edited.ProviderAgentDisplayName)
	require.JSONEq(t, string(refreshed.ProviderMetadata), string(edited.ProviderMetadata))
	require.Equal(t, refreshed.SetupRevision, edited.SetupRevision)
}

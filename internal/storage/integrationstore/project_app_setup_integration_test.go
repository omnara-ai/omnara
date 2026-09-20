//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func slackSetupFixture(
	t *testing.T,
) (inboxFixture, integrationstore.ConfigureProjectAppInput, integrationstore.SaveProjectAppInput) {
	t.Helper()
	f, _, input := projectAppSetupFixture(t)
	var profileID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_profiles WHERE project_id=$1`, f.project).Scan(&profileID),
	)
	return f, input, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "setup", DefinitionID: appdefinition.Slack,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
			Slots: []integrationstore.AppLaunchSlot{{Key: "default", AgentProfileID: &profileID}},
		}},
	}
}

func TestSlackAppSetupFailureRetainsMetadataAndReconnectPreservesSettings(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	app, err := f.store.UpdateProjectApp(f.ctx, input.AppID, metadata)
	require.NoError(t, err)
	f.exec(t, `INSERT INTO org_resource_limit_overrides(org_id,max_active_project_apps_per_project) VALUES($1,0)`, f.org)
	extra := metadata
	extra.Name = "over-limit"
	_, err = f.store.CreateProjectApp(f.ctx, extra)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)

	// Verified setup updates the explicitly saved app; failed validation leaves
	// its metadata available for retry and does not consume the OAuth flow.
	invalid := input
	invalid.CredentialVersionID = uuid.New()
	_, err = f.store.ConfigureProjectApp(f.ctx, invalid)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	current, err := f.store.GetProjectApp(f.ctx, f.project, app.ID)
	require.NoError(t, err)
	require.Equal(t, app, current)
	consumed, err = f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	app, err = f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err, "setup does not create another app or consume creation quota")
	require.Equal(t, input.OAuthFlowID, app.LastOAuthFlowID)
	require.Equal(t, metadata.Settings, app.Settings)

	// Removing a launcher and disconnecting are separate operations. Reconnect
	// may reactivate the saved app but cannot restore the removed launcher.
	metadata.Settings.Launcher = nil
	changed, err := f.store.UpdateProjectApp(f.ctx, app.ID, metadata)
	require.NoError(t, err)
	require.Equal(t, app.SetupRevision, changed.SetupRevision)
	applied, err := f.store.DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: f.project, AppID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
	})
	require.NoError(t, err)
	require.True(t, applied)
	disconnected, err := f.store.GetProjectApp(f.ctx, f.project, app.ID)
	require.NoError(t, err)
	input.ExpectedSetupRevision, input.OAuthFlowID = disconnected.SetupRevision, uuid.Must(uuid.NewV7())
	reconnected, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, app.ID, reconnected.ID)
	require.Equal(t, app.Name, reconnected.Name)
	require.Equal(t, integrationstore.ProjectAppStateActive, reconnected.State)
	require.Equal(t, disconnected.SetupRevision+1, reconnected.SetupRevision)
	require.Nil(t, reconnected.Settings.Launcher)
	page, err := f.store.ListProjectApps(f.ctx, integrationstore.ListProjectAppsInput{ProjectID: f.project, Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Apps, 2) // Saved app plus the independent inbox fixture app.
}

func TestSlackAppInvalidLauncherEditLeavesSetupAndOAuthUnchanged(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	_, err := f.store.UpdateProjectApp(f.ctx, input.AppID, metadata)
	require.NoError(t, err)
	app, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	// Simulate a dependency made unavailable outside ordinary guarded deletion.
	profileID := *metadata.Settings.Launcher.Slots[0].AgentProfileID
	f.exec(t, `UPDATE agent_profiles SET deleted_at=now() WHERE id=$1`, profileID)
	pendingFlow := uuid.Must(uuid.NewV7())
	_, err = f.store.UpdateProjectApp(f.ctx, app.ID, metadata)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, pendingFlow)
	require.NoError(t, err)
	require.False(t, consumed)
	current, err := f.store.GetProjectApp(f.ctx, f.project, app.ID)
	require.NoError(t, err)
	require.Equal(t, app, current)
	// Credential setup is independent of the launcher's profile validation.
	input.ExpectedSetupRevision, input.OAuthFlowID = app.SetupRevision, pendingFlow
	input.ProviderAgentDisplayName = "Reverified bot"
	refreshed, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, pendingFlow, refreshed.LastOAuthFlowID)
	require.Equal(t, app.Settings, refreshed.Settings)
}

func TestSlackAppSetupRechecksConcurrentDisconnect(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	_, err := f.store.UpdateProjectApp(f.ctx, input.AppID, metadata)
	require.NoError(t, err)
	app, err := f.store.ConfigureProjectApp(f.ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	q := dbsqlc.New(tx)
	require.NoError(
		t,
		q.LockProjectAppLifecycleExclusive(f.ctx, dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: app.ID}),
	)
	input.ExpectedSetupRevision, input.OAuthFlowID = app.SetupRevision, uuid.Must(uuid.NewV7())
	done := integrationdb.RunAsync(func() (integrationstore.ProjectAppRecord, error) {
		return f.store.ConfigureProjectApp(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockProjectAppLifecycleExclusive", 1)
	rows, err := q.DisconnectProjectApp(f.ctx, dbsqlc.DisconnectProjectAppParams{
		ProjectID: f.project, ID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, tx.Commit(f.ctx))
	result := integrationdb.Await(t, done, "setup after disconnect")
	require.ErrorIs(t, result.Err, storeerr.ErrConflict)
	require.ErrorIs(t, result.Err, integrationstore.ErrProjectAppSetupChanged)
	require.EqualError(t, result.Err, "app setup changed; refresh the app and start setup again")
	current, err := f.store.GetProjectApp(f.ctx, f.project, app.ID)
	require.NoError(t, err)
	require.Equal(t, app.LastOAuthFlowID, current.LastOAuthFlowID)
	require.Equal(t, app.Settings, current.Settings)
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, current.State)
	require.Equal(t, app.SetupRevision+1, current.SetupRevision)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
}

func TestProjectAppLauncherMatchesVerifiedProviderAccount(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{
		integrationstore.IntegrationProviderSlack, integrationstore.IntegrationProviderGitHub,
	} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f, secretStore, setup := projectAppSetupFixture(t)
			var profileID uuid.UUID
			require.NoError(t, f.pool.QueryRow(f.ctx,
				`SELECT id FROM agent_profiles WHERE project_id=$1 LIMIT 1`, f.project).Scan(&profileID))
			scopeKind, validScope, invalidScope := "workspace", "T123", "T999"
			app, err := f.store.GetProjectApp(f.ctx, f.project, setup.AppID)
			require.NoError(t, err)
			if provider == integrationstore.IntegrationProviderGitHub {
				credential, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
					OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
					Name: "github-launcher", Actor: identitystore.NewUserPrincipal(f.user),
					Material: secrets.GitHubAppCredentialsMaterial{
						AppID: "123", PrivateKey: "test-key-validated-by-caller", WebhookSecret: "test-webhook",
					},
				})
				require.NoError(t, err)
				app, err = f.store.CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
					OrgID: f.org, ProjectID: f.project, Name: "github-launcher", DefinitionID: appdefinition.GitHub,
				})
				require.NoError(t, err)
				setup.AppID, setup.ExpectedSetupRevision = app.ID, app.SetupRevision
				setup.Provider, setup.ProviderTenantID, setup.ProviderAccountRef = provider, "123", "456"
				setup.CredentialSecretID, setup.CredentialVersionID, setup.CredentialAppID = credential.ID, version.ID, 123
				setup.OAuthFlowID = uuid.Nil
				scopeKind, validScope, invalidScope = "installation", "456", "999"
			}
			metadata := projectAppMetadata(app)
			metadata.Settings.Launcher = &integrationstore.AppLauncher{
				Trigger: "mention", ScopeKind: scopeKind, ScopeRef: invalidScope,
				Slots: []integrationstore.AppLaunchSlot{{Key: "default", AgentProfileID: &profileID}},
			}
			// A draft can save setup before credentials exist. Verification rejects an
			// incompatible account without changing metadata or consuming the attempt.
			draft, err := f.store.UpdateProjectApp(f.ctx, app.ID, metadata)
			require.NoError(t, err)
			_, err = f.store.ConfigureProjectApp(f.ctx, setup)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			require.ErrorContains(t, err, "must match")
			current, err := f.store.GetProjectApp(f.ctx, f.project, app.ID)
			require.NoError(t, err)
			require.Equal(t, draft, current)
			if setup.OAuthFlowID != uuid.Nil {
				consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, setup.OAuthFlowID)
				require.NoError(t, err)
				require.False(t, consumed)
			}
			metadata.Settings.Launcher.ScopeRef = validScope
			_, err = f.store.UpdateProjectApp(f.ctx, app.ID, metadata)
			require.NoError(t, err)
			connected, err := f.store.ConfigureProjectApp(f.ctx, setup)
			require.NoError(t, err)
			// Editing an established app checks the same invariant, including after
			// disconnect: its physical identity remains pinned for later reconnect.
			for _, disconnect := range []bool{false, true} {
				if disconnect {
					_, err = f.store.DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
						ProjectID: f.project, AppID: app.ID,
					})
					require.NoError(t, err)
					connected, err = f.store.GetProjectApp(f.ctx, f.project, app.ID)
					require.NoError(t, err)
				}
				metadata.Settings.Launcher.ScopeRef = invalidScope
				_, err = f.store.UpdateProjectApp(f.ctx, app.ID, metadata)
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
				current, err = f.store.GetProjectApp(f.ctx, f.project, app.ID)
				require.NoError(t, err)
				require.Equal(t, connected, current)
			}
		})
	}
}

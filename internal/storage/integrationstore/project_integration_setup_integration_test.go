//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
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
) (inboxFixture, integrationstore.ConfigureProjectIntegrationInput, integrationstore.SaveProjectIntegrationInput) {
	t.Helper()
	f, _, input := projectIntegrationSetupFixture(t)
	var profileID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_profiles WHERE project_id=$1`, f.project).Scan(&profileID),
	)
	return f, input, integrationstore.SaveProjectIntegrationInput{
		OrgID: f.org, ProjectID: f.project, Name: "setup", IntegrationType: integrationdefinition.SlackThread,
		Settings: integrationstore.ProjectIntegrationSettings{Launcher: &integrationstore.IntegrationLauncher{
			Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
			Slots: []integrationstore.IntegrationLaunchSlot{{Key: "default", AgentProfileID: &profileID}},
		}},
	}
}

func TestSlackIntegrationSetupFailureRetainsMetadataAndReconnectPreservesSettings(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	integration, err := f.store.UpdateProjectIntegration(f.ctx, input.IntegrationID, metadata)
	require.NoError(t, err)
	f.exec(
		t,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_project_integrations_per_project) VALUES($1,0)`,
		f.org,
	)
	extra := metadata
	extra.Name = "over-limit"
	_, err = f.store.CreateProjectIntegration(f.ctx, extra)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)

	invalid := input
	invalid.CredentialVersionID = uuid.New()
	_, err = f.store.ConfigureProjectIntegration(f.ctx, invalid)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	current, err := f.store.GetProjectIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration, current)
	consumed, err = f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	integration, err = f.store.ConfigureProjectIntegration(f.ctx, input)
	require.NoError(t, err, "setup does not create another integration or consume creation quota")
	require.Equal(t, input.OAuthFlowID, integration.LastOAuthFlowID)
	require.Equal(t, metadata.Settings, integration.Settings)

	metadata.Settings.Launcher = nil
	changed, err := f.store.UpdateProjectIntegration(f.ctx, integration.ID, metadata)
	require.NoError(t, err)
	require.Equal(t, integration.SetupRevision, changed.SetupRevision)
	applied, err := f.store.DisconnectProjectIntegration(f.ctx, integrationstore.DisconnectProjectIntegrationInput{
		ProjectID: f.project, IntegrationID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
	})
	require.NoError(t, err)
	require.True(t, applied)
	disconnected, err := f.store.GetProjectIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	input.ExpectedSetupRevision, input.OAuthFlowID = disconnected.SetupRevision, uuid.Must(uuid.NewV7())
	reconnected, err := f.store.ConfigureProjectIntegration(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, integration.ID, reconnected.ID)
	require.Equal(t, integration.Name, reconnected.Name)
	require.Equal(t, integrationstore.ProjectIntegrationStateActive, reconnected.State)
	require.Equal(t, disconnected.SetupRevision+1, reconnected.SetupRevision)
	require.Nil(t, reconnected.Settings.Launcher)
	page, err := f.store.ListProjectIntegrations(
		f.ctx,
		integrationstore.ListProjectIntegrationsInput{ProjectID: f.project, Limit: 100},
	)
	require.NoError(t, err)
	require.Len(t, page.Integrations, 2)
}

func TestSlackIntegrationInvalidLauncherEditLeavesSetupAndOAuthUnchanged(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	_, err := f.store.UpdateProjectIntegration(f.ctx, input.IntegrationID, metadata)
	require.NoError(t, err)
	integration, err := f.store.ConfigureProjectIntegration(f.ctx, input)
	require.NoError(t, err)
	profileID := *metadata.Settings.Launcher.Slots[0].AgentProfileID
	f.exec(t, `UPDATE agent_profiles SET deleted_at=now() WHERE id=$1`, profileID)
	pendingFlow := uuid.Must(uuid.NewV7())
	_, err = f.store.UpdateProjectIntegration(f.ctx, integration.ID, metadata)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, pendingFlow)
	require.NoError(t, err)
	require.False(t, consumed)
	current, err := f.store.GetProjectIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration, current)
	input.ExpectedSetupRevision, input.OAuthFlowID = integration.SetupRevision, pendingFlow
	input.ProviderAgentDisplayName = "Reverified bot"
	refreshed, err := f.store.ConfigureProjectIntegration(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, pendingFlow, refreshed.LastOAuthFlowID)
	require.Equal(t, integration.Settings, refreshed.Settings)
}

func TestSlackIntegrationSetupRechecksConcurrentDisconnect(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	_, err := f.store.UpdateProjectIntegration(f.ctx, input.IntegrationID, metadata)
	require.NoError(t, err)
	integration, err := f.store.ConfigureProjectIntegration(f.ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	q := dbsqlc.New(tx)
	require.NoError(
		t,
		q.LockProjectIntegrationLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectIntegrationLifecycleExclusiveParams{IntegrationID: integration.ID},
		),
	)
	input.ExpectedSetupRevision, input.OAuthFlowID = integration.SetupRevision, uuid.Must(uuid.NewV7())
	done := integrationdb.RunAsync(func() (integrationstore.ProjectIntegrationRecord, error) {
		return f.store.ConfigureProjectIntegration(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockProjectIntegrationLifecycleExclusive", 1)
	rows, err := q.DisconnectProjectIntegration(f.ctx, dbsqlc.DisconnectProjectIntegrationParams{
		ProjectID: f.project, ID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, tx.Commit(f.ctx))
	result := integrationdb.Await(t, done, "setup after disconnect")
	require.ErrorIs(t, result.Err, storeerr.ErrConflict)
	require.ErrorIs(t, result.Err, integrationstore.ErrProjectIntegrationSetupChanged)
	require.EqualError(t, result.Err, "integration setup changed; refresh the integration and start setup again")
	current, err := f.store.GetProjectIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration.LastOAuthFlowID, current.LastOAuthFlowID)
	require.Equal(t, integration.Settings, current.Settings)
	require.Equal(t, integrationstore.ProjectIntegrationStateDisconnected, current.State)
	require.Equal(t, integration.SetupRevision+1, current.SetupRevision)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
}

func TestProjectIntegrationLauncherMatchesVerifiedProviderAccount(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{
		integrationstore.IntegrationProviderSlack, integrationstore.IntegrationProviderGitHub,
	} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f, secretStore, setup := projectIntegrationSetupFixture(t)
			var profileID uuid.UUID
			require.NoError(t, f.pool.QueryRow(f.ctx,
				`SELECT id FROM agent_profiles WHERE project_id=$1 LIMIT 1`, f.project).Scan(&profileID))
			scopeKind, validScope, invalidScope := "workspace", "T123", "T999"
			integration, err := f.store.GetProjectIntegration(f.ctx, f.project, setup.IntegrationID)
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
				integration, err = f.store.CreateProjectIntegration(f.ctx, integrationstore.SaveProjectIntegrationInput{
					OrgID: f.org, ProjectID: f.project, Name: "github-launcher", IntegrationType: integrationdefinition.GitHubPR,
				})
				require.NoError(t, err)
				setup.IntegrationID, setup.ExpectedSetupRevision = integration.ID, integration.SetupRevision
				setup.Provider, setup.ProviderTenantID, setup.ProviderAccountRef = provider, "123", "456"
				setup.CredentialSecretID, setup.CredentialVersionID, setup.CredentialAppID = credential.ID, version.ID, 123
				setup.OAuthFlowID = uuid.Nil
				scopeKind, validScope, invalidScope = "installation", "456", "999"
			}
			metadata := projectIntegrationMetadata(integration)
			metadata.Settings.Launcher = &integrationstore.IntegrationLauncher{
				Trigger: "mention", ScopeKind: scopeKind, ScopeRef: invalidScope,
				Slots: []integrationstore.IntegrationLaunchSlot{{Key: "default", AgentProfileID: &profileID}},
			}
			draft, err := f.store.UpdateProjectIntegration(f.ctx, integration.ID, metadata)
			require.NoError(t, err)
			_, err = f.store.ConfigureProjectIntegration(f.ctx, setup)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			require.ErrorContains(t, err, "must match")
			current, err := f.store.GetProjectIntegration(f.ctx, f.project, integration.ID)
			require.NoError(t, err)
			require.Equal(t, draft, current)
			if setup.OAuthFlowID != uuid.Nil {
				consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, setup.OAuthFlowID)
				require.NoError(t, err)
				require.False(t, consumed)
			}
			metadata.Settings.Launcher.ScopeRef = validScope
			_, err = f.store.UpdateProjectIntegration(f.ctx, integration.ID, metadata)
			require.NoError(t, err)
			connected, err := f.store.ConfigureProjectIntegration(f.ctx, setup)
			require.NoError(t, err)
			for _, disconnect := range []bool{false, true} {
				if disconnect {
					_, err = f.store.DisconnectProjectIntegration(f.ctx, integrationstore.DisconnectProjectIntegrationInput{
						ProjectID: f.project, IntegrationID: integration.ID,
					})
					require.NoError(t, err)
					connected, err = f.store.GetProjectIntegration(f.ctx, f.project, integration.ID)
					require.NoError(t, err)
				}
				metadata.Settings.Launcher.ScopeRef = invalidScope
				_, err = f.store.UpdateProjectIntegration(f.ctx, integration.ID, metadata)
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
				current, err = f.store.GetProjectIntegration(f.ctx, f.project, integration.ID)
				require.NoError(t, err)
				require.Equal(t, connected, current)
			}
		})
	}
}

//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func slackSetupFixture(t *testing.T) (
	inboxFixture,
	integrationstore.SaveIntegrationConnectionInput,
	integrationstore.SaveProjectAppInput,
) {
	t.Helper()
	f, secretsStore, input := connectionFixture(t)
	f.store = integrationstore.New(f.pool, executionstore.IntegrationConnectionAccess{})
	credential, _, err := secretsStore.CreateSecret(
		f.ctx,
		secretstore.CreateSecretInput{
			OrgID:          f.org,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: f.project,
			Name:           "slack-setup",
			Actor:          identitystore.NewUserPrincipal(f.user),
			Material: secrets.SlackAppCredentialsMaterial{
				AccessToken:   "xoxb-setup",
				ClientID:      "setup-client",
				ClientSecret:  "setup-secret",
				SigningSecret: "setup-signature",
			},
		},
	)
	require.NoError(t, err)
	input.Provider = integrationstore.IntegrationProviderSlack
	input.ProviderTenantID, input.CredentialSecretID = "T123", credential.ID
	input.OAuthFlowID, err = uuid.NewV7()
	require.NoError(t, err)
	var configID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(
			f.ctx,
			`SELECT id FROM agent_configs WHERE project_id=$1 ORDER BY id LIMIT 1`,
			f.project,
		).Scan(&configID),
	)
	profile, err := executionstore.New(f.pool, executionstore.Config{}).CreateAgentProfile(
		f.ctx,
		executionstore.CreateAgentProfileInput{
			OrgID:           f.org,
			ProjectID:       f.project,
			Name:            "setup-profile",
			CurrentConfigID: configID,
		},
	)
	require.NoError(t, err)
	setup := integrationstore.SaveProjectAppInput{
		OrgID:        f.org,
		ProjectID:    f.project,
		DefinitionID: appdefinition.Slack,
		Enabled:      true,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger:   "mention",
			ScopeKind: "workspace",
			ScopeRef:  "T123",
			Slots:     []integrationstore.AppLaunchSlot{{Key: "default", AgentProfileID: &profile.ID}},
		}},
	}
	return f, input, setup
}

func TestSlackAppSetupRollsBackTogetherAndPreservesReconnect(t *testing.T) {
	t.Parallel()
	f, input, setup := slackSetupFixture(t)
	f.exec(
		t,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_project_apps_per_project) VALUES($1,0)`,
		f.org,
	)
	_, _, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	_, err = f.store.GetIntegrationConnectionByProviderAccount(
		f.ctx,
		input.Provider,
		input.ProviderTenantID,
		input.ProviderAccountRef,
	)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	f.exec(t, `UPDATE org_resource_limit_overrides SET max_active_project_apps_per_project=2 WHERE org_id=$1`, f.org)
	connection, app, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.NoError(t, err)
	require.Equal(t, input.OAuthFlowID, connection.LastOAuthFlowID)
	require.Equal(t, "slack-"+connection.ID.String(), app.Name)
	ref, err := publicid.Encode(publicid.KindIntegrationConnection, connection.ID)
	require.NoError(t, err)
	require.Equal(t, ref, app.Settings.Resource.Connection)
	// The user intentionally removes the launcher and disables this reusable app.
	configured := setup
	configured.Name = "Customized Slack app"
	configured.Settings = app.Settings
	configured.Settings.Launcher = nil
	configured.Enabled = false
	changed, err := f.store.UpdateProjectApp(f.ctx, app.ID, configured)
	require.NoError(t, err)
	input.OAuthFlowID, err = uuid.NewV7()
	require.NoError(t, err)
	// An ignored default must not overwrite the existing setup or validate a
	// now-unrelated requested profile. The persisted app itself is validated.
	absent := uuid.New()
	setup.Settings.Launcher.Slots = []integrationstore.AppLaunchSlot{{Key: "replacement", AgentProfileID: &absent}}
	reconnected, preserved, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.NoError(t, err)
	require.Equal(t, connection.ID, reconnected.ID)
	require.Equal(t, changed, preserved)
	page, err := f.store.ListProjectApps(f.ctx, integrationstore.ListProjectAppsInput{ProjectID: f.project, Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Apps, 1)
}

func TestSlackAppSetupInvalidProfileDoesNotConsumeOAuth(t *testing.T) {
	t.Parallel()
	f, input, setup := slackSetupFixture(t)
	profileID := *setup.Settings.Launcher.Slots[0].AgentProfileID
	connection, app, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.NoError(t, err)
	// Simulate a dependency made unavailable outside ordinary guarded deletion.
	f.exec(t, `UPDATE agent_profiles SET deleted_at=now() WHERE id=$1`, profileID)
	input.OAuthFlowID, err = uuid.NewV7()
	require.NoError(t, err)
	input.ProviderAgentDisplayName = "Should roll back"
	_, _, err = f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	current, err := f.store.GetIntegrationConnection(f.ctx, f.project, connection.ID)
	require.NoError(t, err)
	require.Equal(t, connection.LastOAuthFlowID, current.LastOAuthFlowID)
	require.Equal(t, connection.ProviderAgentDisplayName, current.ProviderAgentDisplayName)
	retained, err := f.store.GetProjectApp(f.ctx, f.project, app.ID)
	require.NoError(t, err)
	require.Equal(t, app, retained)
}

func TestSlackAppSetupConcurrentDisablePreservesOperatorEdit(t *testing.T) {
	t.Parallel()
	f, input, setup := slackSetupFixture(t)
	connection, app, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.NoError(t, err)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(
		f.ctx,
		`SELECT id FROM agent_profiles WHERE id=$1 FOR UPDATE`,
		*setup.Settings.Launcher.Slots[0].AgentProfileID,
	)
	require.NoError(t, err)
	input.OAuthFlowID, err = uuid.NewV7()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, _, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup); done <- err }()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockAgentProfile", 1)
	disable := setup
	disable.Name, disable.Settings, disable.Enabled = app.Name, app.Settings, false
	_, err = f.store.UpdateProjectApp(f.ctx, app.ID, disable)
	require.NoError(
		t,
		err,
		"disable must not wait behind a setup holding the connection gate while waiting on a profile",
	)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(t, <-done, storeerr.ErrConflict)
	current, err := f.store.GetIntegrationConnection(f.ctx, f.project, connection.ID)
	require.NoError(t, err)
	require.Equal(t, connection.LastOAuthFlowID, current.LastOAuthFlowID)
	retained, err := f.store.GetProjectApp(f.ctx, f.project, app.ID)
	require.NoError(t, err)
	require.False(t, retained.Enabled)
}

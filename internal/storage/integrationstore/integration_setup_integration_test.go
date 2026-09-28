//go:build integration

package integrationstore_test

import (
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func slackSetupFixture(
	t *testing.T,
) (inboxFixture, integrationstore.ConfigureIntegrationInput, integrationstore.SaveIntegrationInput) {
	t.Helper()
	f, _, input := integrationSetupFixture(t)
	var profileID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_profiles WHERE project_id=$1`, f.project).Scan(&profileID),
	)
	return f, input, integrationstore.SaveIntegrationInput{
		OrgID: f.org, ProjectID: f.project, Name: "setup", IntegrationKind: integrationdefinition.SlackThread,
		Settings: integrationtest.ChatSettings("", profileID),
	}
}

func TestSlackIntegrationSetupFailureRetainsMetadataAndReconnectPreservesSettings(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	integration, err := f.store.UpdateIntegration(f.ctx, input.IntegrationID, metadata)
	require.NoError(t, err)
	f.exec(
		t,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_integrations_per_project) VALUES($1,0)`,
		f.org,
	)
	extra := metadata
	extra.Name = "over-limit"
	_, err = f.store.CreateIntegration(f.ctx, extra)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)

	invalid := input
	invalid.CredentialVersionID = uuid.New()
	_, err = f.store.ConfigureIntegration(f.ctx, invalid)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	current, err := f.store.GetIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration, current)
	consumed, err = f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	integration, err = f.store.ConfigureIntegration(f.ctx, input)
	require.NoError(t, err, "setup does not create another integration or consume creation quota")
	require.Equal(t, input.OAuthFlowID, integration.LastOAuthFlowID)
	require.JSONEq(t, string(metadata.Settings), string(integration.Settings))

	metadata.Settings = integrationstore.IntegrationSettings(`{}`)
	changed, err := f.store.UpdateIntegration(f.ctx, integration.ID, metadata)
	require.NoError(t, err)
	require.Equal(t, integration.SetupRevision, changed.SetupRevision)
	applied, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
		ProjectID: f.project, IntegrationID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
	})
	require.NoError(t, err)
	require.True(t, applied)
	disconnected, err := f.store.GetIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	input.ExpectedSetupRevision, input.OAuthFlowID = disconnected.SetupRevision, uuid.Must(uuid.NewV7())
	reconnected, err := f.store.ConfigureIntegration(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, integration.ID, reconnected.ID)
	require.Equal(t, integration.Name, reconnected.Name)
	require.Equal(t, integrationstore.IntegrationStateActive, reconnected.State)
	require.Equal(t, disconnected.SetupRevision+1, reconnected.SetupRevision)
	require.JSONEq(t, `{}`, string(reconnected.Settings))
	page, err := f.store.ListIntegrations(
		f.ctx,
		integrationstore.ListIntegrationsInput{ProjectID: f.project, Limit: 100},
	)
	require.NoError(t, err)
	require.Len(t, page.Integrations, 2)
}

func TestSlackIntegrationInvalidLauncherEditLeavesSetupAndOAuthUnchanged(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	_, err := f.store.UpdateIntegration(f.ctx, input.IntegrationID, metadata)
	require.NoError(t, err)
	integration, err := f.store.ConfigureIntegration(f.ctx, input)
	require.NoError(t, err)
	metadata.Settings = integrationstore.IntegrationSettings(`{"launcher":{"profiles":[]}}`)
	pendingFlow := uuid.Must(uuid.NewV7())
	_, err = f.store.UpdateIntegration(f.ctx, integration.ID, metadata)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, pendingFlow)
	require.NoError(t, err)
	require.False(t, consumed)
	current, err := f.store.GetIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration, current)
	input.ExpectedSetupRevision, input.OAuthFlowID = integration.SetupRevision, pendingFlow
	input.ProviderAgentDisplayName = "Reverified bot"
	refreshed, err := f.store.ConfigureIntegration(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, pendingFlow, refreshed.LastOAuthFlowID)
	require.JSONEq(t, string(integration.Settings), string(refreshed.Settings))
}

func TestSlackIntegrationSetupRechecksConcurrentDisconnect(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	_, err := f.store.UpdateIntegration(f.ctx, input.IntegrationID, metadata)
	require.NoError(t, err)
	integration, err := f.store.ConfigureIntegration(f.ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	q := dbsqlc.New(tx)
	require.NoError(
		t,
		q.LockIntegrationLifecycleExclusive(
			f.ctx,
			dbsqlc.LockIntegrationLifecycleExclusiveParams{IntegrationID: integration.ID},
		),
	)
	input.ExpectedSetupRevision, input.OAuthFlowID = integration.SetupRevision, uuid.Must(uuid.NewV7())
	done := integrationdb.RunAsync(func() (integrationstore.IntegrationRecord, error) {
		return f.store.ConfigureIntegration(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationLifecycleExclusive", 1)
	rows, err := q.DisconnectIntegration(f.ctx, dbsqlc.DisconnectIntegrationParams{
		ProjectID: f.project, ID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
	require.NoError(t, tx.Commit(f.ctx))
	result := integrationdb.Await(t, done, "setup after disconnect")
	require.ErrorIs(t, result.Err, storeerr.ErrConflict)
	require.ErrorIs(t, result.Err, integrationstore.ErrIntegrationSetupChanged)
	require.EqualError(t, result.Err, "integration setup changed; refresh the integration and start setup again")
	current, err := f.store.GetIntegration(f.ctx, f.project, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration.LastOAuthFlowID, current.LastOAuthFlowID)
	require.Equal(t, integration.Settings, current.Settings)
	require.Equal(t, integrationstore.IntegrationStateDisconnected, current.State)
	require.Equal(t, integration.SetupRevision+1, current.SetupRevision)
	consumed, err := f.store.IntegrationOAuthFlowConsumed(f.ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
}

func TestIntegrationLauncherDoesNotDuplicateVerifiedAccount(t *testing.T) {
	t.Parallel()
	f, input, metadata := slackSetupFixture(t)
	saved, err := f.store.UpdateIntegration(f.ctx, input.IntegrationID, metadata)
	require.NoError(t, err)
	connected, err := f.store.ConfigureIntegration(f.ctx, input)
	require.NoError(t, err)
	require.JSONEq(t, string(saved.Settings), string(connected.Settings))
	require.NotContains(t, string(connected.Settings), "T123")
}

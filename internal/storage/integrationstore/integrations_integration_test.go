//go:build integration

package integrationstore_test

import (
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationReferencesAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	s := integrationstore.New(f.pool, executionstore.IntegrationAccess{})
	execution := executionstore.New(f.pool, executionstore.Config{})
	var configID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(
			f.ctx,
			`SELECT id FROM agent_configs WHERE project_id=$1 ORDER BY id LIMIT 1`,
			f.project,
		).Scan(&configID),
	)
	profile, err := execution.CreateAgentProfile(f.ctx, executionstore.CreateAgentProfileInput{
		OrgID: f.org, ProjectID: f.project, Name: "reviewer", CurrentConfigID: configID,
	})
	require.NoError(t, err)
	input := integrationstore.SaveIntegrationInput{
		OrgID: f.org, ProjectID: f.project, Name: "thread-bot", IntegrationKind: integrationdefinition.SlackThread,
		Settings: integrationtest.ChatSettings(profile.ID),
	}
	integration, err := s.CreateIntegration(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, integrationdefinition.SlackThread, integration.IntegrationKind)
	require.Equal(t, "integration:slack:"+integration.ID.String(), integrationstore.IdempotencyScope(integration),
		"released idempotency keys retain the transport namespace")
	require.Equal(t, integrationstore.IntegrationStateDisconnected, integration.State)
	require.EqualValues(t, 1, integration.SetupRevision)
	require.NoError(t, execution.DeleteAgentProfile(f.ctx, f.project, profile.ID))
	_, err = execution.GetAgentProfile(f.ctx, f.project, profile.ID)
	require.True(t, storeerr.IsNotFound(err), "%v", err)
	_, err = s.CreateIntegration(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.EqualError(t, err, `an integration named "thread-bot" already exists in this project; choose a different name`)
	_, err = s.GetIntegration(f.ctx, uuid.New(), integration.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	updated, err := s.UpdateIntegration(f.ctx, integration.ID, input)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationStateDisconnected, updated.State)
	require.Equal(t, integration.SetupRevision, updated.SetupRevision)
	_, err = execution.GetAgentProfile(f.ctx, f.project, profile.ID)
	require.True(t, storeerr.IsNotFound(err), "%v", err)
	require.NoError(t, s.DeleteIntegration(f.ctx, f.org, f.project, integration.ID))
	_, err = s.GetIntegration(f.ctx, f.project, integration.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	other, err := s.GetIntegration(f.ctx, f.project, f.integrationID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationStateActive, other.State)
}

func TestIntegrationIndependentCapabilitiesAndPagination(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	s := integrationstore.New(f.pool, executionstore.IntegrationAccess{})
	input := integrationstore.SaveIntegrationInput{
		OrgID:           f.org,
		ProjectID:       f.project,
		Name:            "slack-settings",
		IntegrationKind: integrationdefinition.SlackThread,
	}
	first, err := s.CreateIntegration(f.ctx, input)
	require.NoError(t, err)
	input.Name = "more-slack-settings"
	second, err := s.CreateIntegration(f.ctx, input)
	require.NoError(t, err)
	page, err := s.ListIntegrations(
		f.ctx,
		integrationstore.ListIntegrationsInput{ProjectID: f.project, Limit: 1},
	)
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Equal(t, second.ID, page.Integrations[0].ID)
	page, err = s.ListIntegrations(
		f.ctx,
		integrationstore.ListIntegrationsInput{ProjectID: f.project, Limit: 1, After: page.Next},
	)
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Equal(t, first.ID, page.Integrations[0].ID)
	page, err = s.ListIntegrations(
		f.ctx,
		integrationstore.ListIntegrationsInput{ProjectID: f.project, Limit: 1, After: page.Next},
	)
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, f.integrationID, page.Integrations[0].ID)
	_, err = f.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_integrations_per_project) VALUES($1,3)`,
		f.org,
	)
	require.NoError(t, err)
	input.Name = "over-limit"
	_, err = s.CreateIntegration(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.EqualError(t, err, "project integrations limit of 3 reached: resource conflict")
	page, err = s.ListIntegrations(
		f.ctx,
		integrationstore.ListIntegrationsInput{ProjectID: f.project, Limit: 100},
	)
	require.NoError(t, err)
	require.Len(t, page.Integrations, 3)
}

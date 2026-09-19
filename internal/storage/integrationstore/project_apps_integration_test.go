//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestProjectAppReferencesAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	s := integrationstore.New(f.pool, executionstore.IntegrationConnectionAccess{})
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
	connection, err := publicid.Encode(publicid.KindIntegrationConnection, f.connection)
	require.NoError(t, err)
	input := integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "Thread bot", DefinitionID: appdefinition.Slack, Enabled: true,
		Settings: integrationstore.ProjectAppSettings{
			Resource: agentconfig.AgentConfigAppResourceSource{Connection: connection},
			Launcher: &integrationstore.AppLauncher{Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}}},
		},
	}
	app, err := s.CreateProjectApp(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, appdefinition.Slack, app.Settings.Resource.Definition)
	require.ErrorIs(t, execution.DeleteAgentProfile(f.ctx, f.project, profile.ID), storeerr.ErrConflict)
	_, err = s.CreateProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	_, err = s.GetProjectApp(f.ctx, uuid.New(), app.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	// Stopping the launcher keeps reusable setup and prevents deleting its profile.
	input.Enabled = false
	updated, err := s.UpdateProjectApp(f.ctx, app.ID, input)
	require.NoError(t, err)
	require.False(t, updated.Enabled)
	require.ErrorIs(t, execution.DeleteAgentProfile(f.ctx, f.project, profile.ID), storeerr.ErrConflict)
	require.NoError(t, s.DeleteProjectApp(f.ctx, f.org, f.project, app.ID))
	require.NoError(t, execution.DeleteAgentProfile(f.ctx, f.project, profile.ID))
	_, err = s.GetProjectApp(f.ctx, f.project, app.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	// App deletion does not disconnect the provider account.
	_, err = s.GetIntegrationConnection(f.ctx, f.project, f.connection)
	require.NoError(t, err)
}

func TestProjectAppIndependentCapabilitiesAndPagination(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	s := integrationstore.New(f.pool, executionstore.IntegrationConnectionAccess{})
	input := integrationstore.SaveProjectAppInput{
		OrgID:        f.org,
		ProjectID:    f.project,
		Name:         "Slack settings",
		DefinitionID: appdefinition.Slack,
		Enabled:      true,
	}
	first, err := s.CreateProjectApp(f.ctx, input)
	require.NoError(t, err) // Reusable setup need not enable a launcher or listener.
	input.Name = "More Slack settings"
	second, err := s.CreateProjectApp(f.ctx, input)
	require.NoError(t, err)
	page, err := s.ListProjectApps(f.ctx, integrationstore.ListProjectAppsInput{ProjectID: f.project, Limit: 1})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Equal(t, second.ID, page.Apps[0].ID)
	page, err = s.ListProjectApps(
		f.ctx,
		integrationstore.ListProjectAppsInput{ProjectID: f.project, Limit: 1, After: page.Next},
	)
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, first.ID, page.Apps[0].ID)
	_, err = f.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_project_apps_per_project) VALUES($1,2)`,
		f.org,
	)
	require.NoError(t, err)
	input.Name = "Over limit"
	_, err = s.CreateProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	page, err = s.ListProjectApps(f.ctx, integrationstore.ListProjectAppsInput{ProjectID: f.project, Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Apps, 2) // The failed insert rolled back.
}

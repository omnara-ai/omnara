//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestProjectAppReferencesAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	s := integrationstore.New(f.pool, executionstore.AppAccess{})
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
	input := integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "thread-bot", AppType: appdefinition.SlackThread,
		Settings: integrationstore.ProjectAppSettings{
			Launcher: &integrationstore.AppLauncher{Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}}},
		},
	}
	app, err := s.CreateProjectApp(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, appdefinition.SlackThread, app.AppType)
	require.Equal(t, "integration:slack:"+app.ID.String(), integrationstore.IdempotencyScope(app),
		"released idempotency keys retain the transport namespace")
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, app.State)
	require.EqualValues(t, 1, app.SetupRevision)
	require.ErrorIs(t, execution.DeleteAgentProfile(f.ctx, f.project, profile.ID), storeerr.ErrConflict)
	_, err = s.CreateProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.EqualError(t, err, `an app named "thread-bot" already exists in this project; choose a different name`)
	_, err = s.GetProjectApp(f.ctx, uuid.New(), app.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	updated, err := s.UpdateProjectApp(f.ctx, app.ID, input)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, updated.State)
	require.Equal(t, app.SetupRevision, updated.SetupRevision)
	require.ErrorIs(t, execution.DeleteAgentProfile(f.ctx, f.project, profile.ID), storeerr.ErrConflict)
	require.NoError(t, s.DeleteProjectApp(f.ctx, f.org, f.project, app.ID))
	require.NoError(t, execution.DeleteAgentProfile(f.ctx, f.project, profile.ID))
	_, err = s.GetProjectApp(f.ctx, f.project, app.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	other, err := s.GetProjectApp(f.ctx, f.project, f.appID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ProjectAppStateActive, other.State)
}

func TestProjectAppIndependentCapabilitiesAndPagination(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	s := integrationstore.New(f.pool, executionstore.AppAccess{})
	input := integrationstore.SaveProjectAppInput{
		OrgID:     f.org,
		ProjectID: f.project,
		Name:      "slack-settings",
		AppType:   appdefinition.SlackThread,
	}
	first, err := s.CreateProjectApp(f.ctx, input)
	require.NoError(t, err)
	input.Name = "more-slack-settings"
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
	require.True(t, page.HasMore)
	require.Equal(t, first.ID, page.Apps[0].ID)
	page, err = s.ListProjectApps(
		f.ctx,
		integrationstore.ListProjectAppsInput{ProjectID: f.project, Limit: 1, After: page.Next},
	)
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, f.appID, page.Apps[0].ID)
	_, err = f.pool.Exec(
		f.ctx,
		`INSERT INTO org_resource_limit_overrides(org_id,max_active_project_apps_per_project) VALUES($1,3)`,
		f.org,
	)
	require.NoError(t, err)
	input.Name = "over-limit"
	_, err = s.CreateProjectApp(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.EqualError(t, err, "project apps limit of 3 reached: resource conflict")
	page, err = s.ListProjectApps(f.ctx, integrationstore.ListProjectAppsInput{ProjectID: f.project, Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Apps, 3)
}

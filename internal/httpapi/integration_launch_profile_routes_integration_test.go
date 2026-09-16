//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestPublicIntegrationLaunchProfileReadManageAndStableRoute(t *testing.T) {
	t.Parallel()
	f := newLaunchProfileHTTPFixture(t)
	viewer, role := createChannelHTTPKey(t, f.project, "viewer")
	read := requestJSONWithHeaders(t, f.handler, http.MethodGet, f.path, "", "", http.StatusOK, authHeaders(viewer))
	require.Equal(t, map[string]any{"agent_profile_id": nil}, read)
	body := workflowHTTPJSON(t, map[string]any{"agent_profile_id": f.profileID})
	for _, denied := range []string{"viewer", "operator"} {
		role.Role = denied
		_, err := f.project.Store.Identity().SetOrgAPIKeyProjectRole(t.Context(), role)
		require.NoError(t, err)
		requestJSONWithHeaders(t, f.handler, http.MethodPut, f.path, body, "", http.StatusForbidden, authHeaders(viewer))
	}
	role.Role = "developer"
	_, err := f.project.Store.Identity().SetOrgAPIKeyProjectRole(t.Context(), role)
	require.NoError(t, err)
	updated := requestJSONWithHeaders(t, f.handler, http.MethodPut, f.path, body, "", http.StatusOK, authHeaders(viewer))
	require.Equal(t, map[string]any{"agent_profile_id": f.profileID}, updated)
	original := f.route(t)
	require.Equal(t, "discord", original.DeploymentKey)
	require.Equal(t, "discord_conversation", original.BehaviorKey)
	require.Equal(t, integrationstore.IntegrationRouteStateActive, original.State)
	for _, selected := range []any{f.secondProfile(t), nil, f.profileID} {
		response := f.request(t, http.MethodPut, f.path, map[string]any{"agent_profile_id": selected}, http.StatusOK)
		require.Equal(t, map[string]any{"agent_profile_id": selected}, response)
		require.Equal(t, response, f.request(t, http.MethodGet, f.path, nil, http.StatusOK))
		current := f.route(t)
		require.Equal(t, original.ID, current.ID)
		require.Equal(t, original.CreatedAt, current.CreatedAt)
		require.Equal(t, original.Configuration, current.Configuration)
		require.Equal(t, original.State, current.State)
		require.Equal(t, original.BehaviorKey, current.BehaviorKey)
	}
	var agents int
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.project.ProjectUUID).Scan(&agents))
	require.Zero(t, agents, "profile selection only configures future launches")
	requestJSONWithHeaders(t, f.handler, http.MethodGet, f.path, "", "", http.StatusUnauthorized, nil)
}

func TestPublicIntegrationLaunchProfileRejectsMissingForeignAndDeletedProfile(t *testing.T) {
	t.Parallel()
	f := newLaunchProfileHTTPFixture(t)
	f.request(t, http.MethodPut, f.path, map[string]any{"agent_profile_id": f.profileID}, http.StatusOK)
	original := f.route(t)
	otherProject, err := f.project.Store.Identity().CreateProjectForPrincipal(t.Context(),
		identitystore.CreateProjectForPrincipalInput{
			OrgID: f.project.OrgUUID, Creator: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
			Name: "Other launch profile project", IdempotencyKey: "other-launch-profile-project",
		})
	require.NoError(t, err)
	otherProjectID := testPublicID(t, publicid.KindProject, otherProject.ID)
	grantDefaultPublicHTTPModelToProject(t, f.handler, f.project, otherProjectID, f.project.AdminToken)
	other := f.project
	other.ProjectID, other.ProjectUUID = otherProjectID, otherProject.ID
	other.ProjectPath = "/api/v1/orgs/" + other.OrgID + "/projects/" + otherProjectID
	foreignProfile := createSlackReadyHTTPProfile(t, f.handler, other, "foreign-launch", f.project.AdminToken)
	deletedProfile := f.secondProfile(t)
	require.NoError(t, f.project.Store.Execution().DeleteAgentProfile(t.Context(), f.project.ProjectUUID,
		mustPublicHTTPID(t, publicid.KindAgentProfile, deletedProfile)))
	for _, test := range []struct {
		body   any
		status int
	}{
		{nil, http.StatusBadRequest},
		{map[string]any{}, http.StatusBadRequest},
		{map[string]any{"agent_profile_id": ""}, http.StatusBadRequest},
		{map[string]any{"agent_profile_id": uuid.NewString()}, http.StatusBadRequest},
		{map[string]any{"agent_profile_id": testPublicID(t, publicid.KindAgentProfile, uuid.New())}, http.StatusNotFound},
		{map[string]any{"agent_profile_id": channelReceiptString(t, foreignProfile, "id")}, http.StatusNotFound},
		{map[string]any{"agent_profile_id": deletedProfile}, http.StatusNotFound},
		{map[string]any{"agent_profile_id": f.profileID, "behavior_key": "arbitrary"}, http.StatusBadRequest},
	} {
		f.request(t, http.MethodPut, f.path, test.body, test.status)
		require.Equal(t, original, f.route(t), "invalid profile selection must not rewrite the route")
	}
	foreignPath := other.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, f.install.ID) + "/launch-profile"
	f.request(t, http.MethodGet, foreignPath, nil, http.StatusNotFound)
	f.request(t, http.MethodPut, foreignPath, map[string]any{"agent_profile_id": nil}, http.StatusNotFound)
	require.Equal(t, original, f.route(t))
}

func TestPublicIntegrationLaunchProfileRejectsExternalConnection(t *testing.T) {
	t.Parallel()
	f := newLaunchProfileHTTPFixture(t)
	external, err := f.project.Store.Integrations().CreateExternalIntegrationInstall(t.Context(),
		integrationstore.CreateExternalIntegrationInstallInput{
			OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			InstalledBy: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
		})
	require.NoError(t, err)
	for _, id := range []uuid.UUID{external.ID, uuid.New()} {
		path := f.project.ProjectPath + "/integration-installs/" +
			testPublicID(t, publicid.KindIntegrationInstall, id) + "/launch-profile"
		f.request(t, http.MethodGet, path, nil, http.StatusNotFound)
		f.request(t, http.MethodPut, path, map[string]any{"agent_profile_id": f.profileID}, http.StatusNotFound)
	}
	var routes int
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT count(*) FROM integration_routes WHERE integration_install_id=$1`, external.ID).Scan(&routes))
	require.Zero(t, routes)
}

func TestPublicIntegrationLaunchProfileCanClearBeforeConfiguration(t *testing.T) {
	t.Parallel()
	f := newLaunchProfileHTTPFixture(t)
	for range 2 {
		response := f.request(t, http.MethodPut, f.path, map[string]any{"agent_profile_id": nil}, http.StatusOK)
		require.Equal(t, map[string]any{"agent_profile_id": nil}, response)
	}
	original := f.route(t)
	require.Equal(t, uuid.Nil, original.AgentProfileID)
	f.request(t, http.MethodPut, f.path, map[string]any{"agent_profile_id": f.profileID}, http.StatusOK)
	current := f.route(t)
	require.Equal(t, original.ID, current.ID)
	require.Equal(t, mustPublicHTTPID(t, publicid.KindAgentProfile, f.profileID), current.AgentProfileID)
}

type launchProfileHTTPFixture struct {
	handler                   http.Handler
	project                   publicHTTPProject
	install                   integrationstore.IntegrationInstallRecord
	profileID, configID, path string
}

func newLaunchProfileHTTPFixture(t *testing.T) launchProfileHTTPFixture {
	t.Helper()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "launch-profile")
	app, err := project.Store.Integrations().CreateIntegrationApp(t.Context(), integrationstore.CreateIntegrationAppInput{
		OrgID: project.OrgUUID, OwnerProjectID: project.ProjectUUID,
		Provider: "discord", ProviderAppRef: "31", ConnectorKey: channelconnector.BuiltInConnectorKey,
		State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: app.ID,
		InstalledBy:     identitystore.NewUserPrincipal(project.AdminUserUUID),
		IntegrationKind: integrationstore.IntegrationKindManaged, Provider: "discord", ConnectionMode: "gateway",
		ProviderTenantID: "32", ProviderAccountRef: "33", State: integrationstore.IntegrationInstallStateActive,
	}
	install, err := project.Store.Integrations().UpsertIntegrationInstall(t.Context(), input)
	require.NoError(t, err)
	profile := createSlackReadyHTTPProfile(t, handler, project, "future-launch", project.AdminToken)
	return launchProfileHTTPFixture{handler: handler, project: project, install: install,
		profileID: channelReceiptString(t, profile, "id"), configID: channelReceiptString(t, profile, "current_config_id"),
		path: project.ProjectPath + "/integration-installs/" +
			testPublicID(t, publicid.KindIntegrationInstall, install.ID) + "/launch-profile"}
}

func (f launchProfileHTTPFixture) request(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	var raw string
	if body != nil {
		raw = workflowHTTPJSON(t, body)
	}
	return requestJSONWithHeaders(t, f.handler, method, path, raw, "", status, authHeaders(f.project.AdminToken))
}

func (f launchProfileHTTPFixture) route(t *testing.T) integrationstore.IntegrationRouteRecord {
	t.Helper()
	route, err := f.project.Store.Integrations().GetIntegrationRouteByDeploymentKey(
		t.Context(), f.project.ProjectUUID, f.install.ID, "discord")
	require.NoError(t, err)
	return route
}

func (f launchProfileHTTPFixture) secondProfile(t *testing.T) string {
	t.Helper()
	profile := createPublicHTTPAgentProfile(t, f.handler, f.project, "second-launch-profile", "Second launch profile",
		f.configID, f.project.AdminToken, http.StatusCreated)
	return channelReceiptString(t, profile, "id")
}

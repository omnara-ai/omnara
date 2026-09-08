//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestGetOrgOverview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)

	project := bootstrapPublicHTTPProject(t, handler, "org-overview")
	overviewPath := "/api/v1/orgs/" + project.OrgID + "/overview"

	overview := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		overviewPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	projects := testutil.RequireType[[]any](t, overview["projects"])
	if len(projects) != 1 {
		t.Fatalf("projects = %+v, want the bootstrap project only", projects)
	}
	bootstrapRow := testutil.RequireType[map[string]any](t, projects[0])
	if bootstrapRow["id"] != project.ProjectID {
		t.Fatalf("project id = %v, want %s", bootstrapRow["id"], project.ProjectID)
	}
	if access := testutil.RequireType[map[string]any](t, bootstrapRow["access"]); access["can_manage"] != true {
		t.Fatalf("bootstrap project access = %+v, want can_manage", access)
	}
	if agents := testutil.RequireType[[]any](t, overview["recent_agents"]); len(agents) != 0 {
		t.Fatalf("fresh org recent_agents = %+v, want empty", agents)
	}
	if profiles := testutil.RequireType[[]any](t, overview["recent_agent_profiles"]); len(profiles) != 0 {
		t.Fatalf("fresh org recent_agent_profiles = %+v, want empty", profiles)
	}

	configSource := "instruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"org-overview",
		"yaml",
		configSource,
		project.AdminToken,
		http.StatusCreated,
	)
	configID := testutil.RequireType[string](t, config["id"])
	firstProfile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"org-overview-first",
		"First Profile",
		configID,
		project.AdminToken,
		http.StatusCreated,
	)
	firstProfileID := testutil.RequireType[string](t, firstProfile["id"])

	secondCreated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Overview Second"}`,
		"idem-org-overview-second-project",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secondProject := project
	secondProject.ProjectID = testutil.RequireType[string](t, secondCreated["id"])
	secondProject.ProjectUUID = mustPublicHTTPID(t, publicid.KindProject, secondProject.ProjectID)
	secondProject.ProjectPath = "/api/v1/orgs/" + project.OrgID + "/projects/" + secondProject.ProjectID
	grantDefaultPublicHTTPModelToProject(t, handler, project, secondProject.ProjectID, project.AdminToken)
	secondConfig := createPublicHTTPAgentConfig(
		t,
		handler,
		secondProject,
		"org-overview-second",
		"yaml",
		configSource,
		project.AdminToken,
		http.StatusCreated,
	)
	secondConfigID := testutil.RequireType[string](t, secondConfig["id"])
	secondProfile := createPublicHTTPAgentProfile(
		t,
		handler,
		secondProject,
		"org-overview-second",
		"Second Profile",
		secondConfigID,
		project.AdminToken,
		http.StatusCreated,
	)
	secondProfileID := testutil.RequireType[string](t, secondProfile["id"])
	launch := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		secondProject.ProjectPath+"/agents",
		`{"config":"`+secondConfigID+`"}`,
		"idem-org-overview-agent",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	launchedAgentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])

	overview = requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		overviewPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if projects := testutil.RequireType[[]any](t, overview["projects"]); len(projects) != 2 {
		t.Fatalf("projects = %+v, want both org projects", projects)
	}
	agents := testutil.RequireType[[]any](t, overview["recent_agents"])
	if len(agents) != 1 {
		t.Fatalf("recent_agents = %+v, want the launched agent", agents)
	}
	agentRow := testutil.RequireType[map[string]any](t, agents[0])
	if agentRow["id"] != launchedAgentID {
		t.Fatalf("recent agent id = %v, want %s", agentRow["id"], launchedAgentID)
	}
	if agentRow["project_id"] != secondProject.ProjectID {
		t.Fatalf("recent agent project_id = %v, want %s", agentRow["project_id"], secondProject.ProjectID)
	}
	if model := testutil.RequireType[map[string]any](
		t, agentRow["model"],
	); model["provider_config"] != "openai-prod" || model["name"] != "gpt-test" {
		t.Fatalf("recent agent model = %+v, want openai-prod/gpt-test", model)
	}
	profileRows := testutil.RequireType[[]any](t, overview["recent_agent_profiles"])
	if len(profileRows) != 2 {
		t.Fatalf("recent_agent_profiles = %+v, want both profiles", profileRows)
	}
	if got := testutil.RequireType[map[string]any](t, profileRows[0])["id"]; got != secondProfileID {
		t.Fatalf("recent profile order[0] = %v, want %s (newest first)", got, secondProfileID)
	}
	if got := testutil.RequireType[map[string]any](t, profileRows[1])["id"]; got != firstProfileID {
		t.Fatalf("recent profile order[1] = %v, want %s", got, firstProfileID)
	}
	if got := testutil.RequireType[map[string]any](
		t, testutil.RequireType[map[string]any](t, profileRows[0])["current_config"],
	)["id"]; got != secondConfigID {
		t.Fatalf("recent profile current_config id = %v, want %s", got, secondConfigID)
	}

	extraProfileIDs := make([]string, 0, 4)
	for _, seed := range []string{"extra-a", "extra-b", "extra-c", "extra-d"} {
		extra := createPublicHTTPAgentProfile(
			t,
			handler,
			project,
			"org-overview-"+seed,
			"Profile "+seed,
			configID,
			project.AdminToken,
			http.StatusCreated,
		)
		extraProfileIDs = append(extraProfileIDs, testutil.RequireType[string](t, extra["id"]))
	}
	overview = requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		overviewPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	profileRows = testutil.RequireType[[]any](t, overview["recent_agent_profiles"])
	if len(profileRows) != 5 {
		t.Fatalf("recent_agent_profiles returned %d rows, want cap of 5", len(profileRows))
	}
	gotProfileIDs := map[string]bool{}
	for _, raw := range profileRows {
		gotProfileIDs[testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, raw)["id"])] = true
	}
	if gotProfileIDs[firstProfileID] {
		t.Fatalf("oldest profile %s should fall outside the recents cap: %v", firstProfileID, gotProfileIDs)
	}
	if got := testutil.RequireType[map[string]any](t, profileRows[0])["id"]; got != extraProfileIDs[3] {
		t.Fatalf("recent profile order[0] = %v, want most recent %s", got, extraProfileIDs[3])
	}

	viewer, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "org-overview-viewer@example.com",
			DisplayName: "Overview Viewer",
		},
	)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: viewer.ID,
			Name:   "viewer",
		},
	)
	if err != nil {
		t.Fatalf("create viewer token: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: viewer.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add viewer org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     project.OrgUUID,
		ProjectID: secondProject.ProjectUUID,
		UserID:    viewer.ID,
		Role:      "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	viewerOverview := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		overviewPath,
		"",
		"",
		http.StatusOK,
		authHeaders(viewerPAT.Token),
	)
	viewerProjects := testutil.RequireType[[]any](t, viewerOverview["projects"])
	if len(viewerProjects) != 1 {
		t.Fatalf("viewer projects = %+v, want the second project only", viewerProjects)
	}
	viewerProject := testutil.RequireType[map[string]any](t, viewerProjects[0])
	if viewerProject["id"] != secondProject.ProjectID {
		t.Fatalf("viewer project id = %v, want %s", viewerProject["id"], secondProject.ProjectID)
	}
	if access := testutil.RequireType[map[string]any](
		t, viewerProject["access"],
	); access["can_manage"] != false || access["can_read"] != true {
		t.Fatalf("viewer project access = %+v, want read-only", access)
	}
	viewerAgents := testutil.RequireType[[]any](t, viewerOverview["recent_agents"])
	if len(viewerAgents) != 1 || testutil.RequireType[map[string]any](t, viewerAgents[0])["id"] != launchedAgentID {
		t.Fatalf("viewer recent_agents = %+v, want the second project's agent", viewerAgents)
	}
	viewerProfiles := testutil.RequireType[[]any](t, viewerOverview["recent_agent_profiles"])
	if len(viewerProfiles) != 1 || testutil.RequireType[map[string]any](t, viewerProfiles[0])["id"] != secondProfileID {
		t.Fatalf("viewer recent_agent_profiles = %+v, want the second project's profile", viewerProfiles)
	}

	otherOrg := bootstrapPublicHTTPProject(t, handler, "org-overview-other")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		overviewPath,
		"",
		"",
		http.StatusNotFound,
		authHeaders(otherOrg.AdminToken),
	)
}

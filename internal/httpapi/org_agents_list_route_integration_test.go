//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestListOrgAgentsAndProfiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)

	project := bootstrapPublicHTTPProject(t, handler, "org-agents")
	orgPath := "/api/v1/orgs/" + project.OrgID
	configSource := "instruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n"

	secondCreated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		orgPath+"/projects",
		`{"name":"Org Agents Second"}`,
		"idem-org-agents-second-project",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secondProject := project
	secondProject.ProjectID = testutil.RequireType[string](t, secondCreated["id"])
	secondProject.ProjectUUID = mustPublicHTTPID(t, publicid.KindProject, secondProject.ProjectID)
	secondProject.ProjectPath = orgPath + "/projects/" + secondProject.ProjectID
	grantDefaultPublicHTTPModelToProject(t, handler, project, secondProject.ProjectID, project.AdminToken)

	launchInProject := func(target publicHTTPProject, seed string) (string, string) {
		t.Helper()
		config := createPublicHTTPAgentConfig(
			t, handler, target, seed, "yaml", configSource, project.AdminToken, http.StatusCreated,
		)
		configID := testutil.RequireType[string](t, config["id"])
		profile := createPublicHTTPAgentProfile(
			t, handler, target, seed, "Profile "+seed, configID, project.AdminToken, http.StatusCreated,
		)
		profileID := testutil.RequireType[string](t, profile["id"])
		launch := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			target.ProjectPath+"/agents",
			`{"profile":"`+profileID+`","config":"`+configID+`"}`,
			"idem-"+seed+"-agent",
			http.StatusCreated,
			authHeaders(project.AdminToken),
		)
		agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])
		return profileID, agentID
	}
	firstProfileID, firstAgentID := launchInProject(project, "org-agents-first")
	secondProfileID, secondAgentID := launchInProject(secondProject, "org-agents-second")

	agents := listOrgRows(t, handler, orgPath+"/agents", project.AdminToken)
	assertOrgRows(t, "admin agents", agents, map[string]string{
		secondAgentID: secondProject.ProjectID,
		firstAgentID:  project.ProjectID,
	})
	if agents[0]["id"] != secondAgentID {
		t.Fatalf("agents[0] = %v, want newest agent %s", agents[0]["id"], secondAgentID)
	}
	profiles := listOrgRows(t, handler, orgPath+"/agent-profiles", project.AdminToken)
	assertOrgRows(t, "admin profiles", profiles, map[string]string{
		secondProfileID: secondProject.ProjectID,
		firstProfileID:  project.ProjectID,
	})

	firstPage := requestJSONWithHeaders(
		t, handler, http.MethodGet, orgPath+"/agents?limit=1", "", "", http.StatusOK,
		authHeaders(project.AdminToken),
	)
	firstPageRows := testutil.RequireType[[]any](t, firstPage["data"])
	cursor := testutil.RequireType[string](t, firstPage["next_cursor"])
	if len(firstPageRows) != 1 || testutil.RequireType[map[string]any](t, firstPageRows[0])["id"] != secondAgentID {
		t.Fatalf("first page = %+v, want the newest agent", firstPageRows)
	}
	secondPage := requestJSONWithHeaders(
		t, handler, http.MethodGet, orgPath+"/agents?limit=1&cursor="+url.QueryEscape(cursor), "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	secondPageRows := testutil.RequireType[[]any](t, secondPage["data"])
	if len(secondPageRows) != 1 || testutil.RequireType[map[string]any](t, secondPageRows[0])["id"] != firstAgentID {
		t.Fatalf("second page = %+v, want the older agent", secondPageRows)
	}
	if secondPage["next_cursor"] != nil {
		t.Fatalf("second page next_cursor = %v, want null", secondPage["next_cursor"])
	}
	requestJSONWithHeaders(
		t, handler, http.MethodGet, project.ProjectPath+"/agents?limit=1&cursor="+url.QueryEscape(cursor), "", "",
		http.StatusBadRequest, authHeaders(project.AdminToken),
	)

	viewer, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{Email: "org-agents-viewer@example.com", DisplayName: "Viewer"},
	)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: viewer.ID, Name: "viewer"},
	)
	if err != nil {
		t.Fatalf("create viewer token: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: project.OrgUUID, UserID: viewer.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add viewer org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: secondProject.ProjectUUID, UserID: viewer.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	assertOrgRows(t, "viewer agents", listOrgRows(t, handler, orgPath+"/agents", viewerPAT.Token),
		map[string]string{secondAgentID: secondProject.ProjectID})
	assertOrgRows(t, "viewer profiles", listOrgRows(t, handler, orgPath+"/agent-profiles", viewerPAT.Token),
		map[string]string{secondProfileID: secondProject.ProjectID})

	outsider, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{Email: "org-agents-outsider@example.com", DisplayName: "Outsider"},
	)
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	outsiderPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: outsider.ID, Name: "outsider"},
	)
	if err != nil {
		t.Fatalf("create outsider token: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: project.OrgUUID, UserID: outsider.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add outsider org membership: %v", err)
	}
	assertOrgRows(t, "member without projects", listOrgRows(t, handler, orgPath+"/agents", outsiderPAT.Token),
		map[string]string{})

	otherOrg := bootstrapPublicHTTPProject(t, handler, "org-agents-other")
	for _, path := range []string{orgPath + "/agents", orgPath + "/agent-profiles"} {
		requestJSONWithHeaders(
			t, handler, http.MethodGet, path, "", "", http.StatusNotFound, authHeaders(otherOrg.AdminToken),
		)
	}
}

func listOrgRows(t *testing.T, handler http.Handler, path string, token string) []map[string]any {
	t.Helper()
	listed := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, authHeaders(token))
	raw := testutil.RequireType[[]any](t, listed["data"])
	rows := make([]map[string]any, 0, len(raw))
	for _, row := range raw {
		rows = append(rows, testutil.RequireType[map[string]any](t, row))
	}
	return rows
}

func assertOrgRows(t *testing.T, label string, rows []map[string]any, wantProjects map[string]string) {
	t.Helper()
	if len(rows) != len(wantProjects) {
		t.Fatalf("%s = %+v, want ids %v", label, rows, wantProjects)
	}
	for _, row := range rows {
		id := testutil.RequireType[string](t, row["id"])
		want, ok := wantProjects[id]
		if !ok || row["project_id"] != want {
			t.Fatalf("%s row %s project_id = %v, want %q (expected %v)", label, id, row["project_id"], want, ok)
		}
	}
}

//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestProjectGetAndRename(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "project-rename")
	admin := authHeaders(project.AdminToken)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Taken"}`,
		"idem-taken-project",
		http.StatusCreated,
		admin,
	)

	before := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath, "", "", http.StatusOK, admin)
	if before["id"] != project.ProjectID {
		t.Fatalf("get project id = %v, want %s", before["id"], project.ProjectID)
	}

	renamed := requestJSONWithHeaders(
		t, handler, http.MethodPatch, project.ProjectPath, `{"name":"Renamed"}`, "", http.StatusOK, admin,
	)
	if renamed["name"] != "Renamed" || renamed["updated_at"] == before["updated_at"] {
		t.Fatalf("renamed project = %v, want new name and updated_at", renamed)
	}
	after := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath, "", "", http.StatusOK, admin)
	if after["name"] != "Renamed" {
		t.Fatalf("get after rename name = %v, want Renamed", after["name"])
	}
	requestJSONWithHeaders(
		t, handler, http.MethodPatch, project.ProjectPath, `{"name":"Taken"}`, "", http.StatusConflict, admin,
	)

	viewer, err := storagetest.CreateVerifiedUser(
		ctx, pool, storagetest.CreateVerifiedUserInput{Email: "project-viewer@example.com", DisplayName: "Viewer"},
	)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
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
		ProjectID: project.ProjectUUID,
		UserID:    viewer.ID,
		Role:      "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: viewer.ID, Name: "viewer"},
	)
	if err != nil {
		t.Fatalf("create viewer pat: %v", err)
	}
	viewerHeaders := authHeaders(viewerPAT.Token)
	requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath, "", "", http.StatusOK, viewerHeaders)
	requestJSONWithHeaders(
		t, handler, http.MethodPatch, project.ProjectPath, `{"name":"Viewer Rename"}`, "", http.StatusForbidden,
		viewerHeaders,
	)

	requestJSONWithHeaders(t, handler, http.MethodDelete, project.ProjectPath, "", "", http.StatusNoContent, admin)
	requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath, "", "", http.StatusNotFound, admin)
	requestJSONWithHeaders(
		t, handler, http.MethodPatch, project.ProjectPath, `{"name":"Gone"}`, "", http.StatusNotFound, admin,
	)
}

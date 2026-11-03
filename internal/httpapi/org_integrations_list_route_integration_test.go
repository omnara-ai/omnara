//go:build integration

package httpapi

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/stretchr/testify/require"
)

func TestListOrgIntegrations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)

	project := bootstrapPublicHTTPProject(t, handler, "org-integrations")
	secondProject := integrationHTTPSecondProject(t, handler, project)
	orgPath := "/api/v1/orgs/" + project.OrgID
	create := func(target publicHTTPProject, name string) string {
		t.Helper()
		created := requestJSONWithHeaders(t, handler, http.MethodPost, target.ProjectPath+"/integrations",
			integrationHTTPJSON(t, integrationHTTPBody(name, "slack_thread")), "", http.StatusCreated,
			authHeaders(project.AdminToken))
		return testutil.RequireType[string](t, created["id"])
	}
	firstID := create(project, "Support")
	secondID := create(secondProject, "Reviews")

	listed := listOrgRows(t, handler, orgPath+"/integrations", project.AdminToken)
	assertOrgRows(t, "admin integrations", listed, map[string]string{
		secondID: secondProject.ProjectID,
		firstID:  project.ProjectID,
	})
	require.Equal(t, secondID, listed[0]["id"], "newest integration first")
	assertOrgRows(t, "name filter",
		listOrgRows(t, handler, orgPath+"/integrations?name="+url.QueryEscape("supp*"), project.AdminToken),
		map[string]string{firstID: project.ProjectID})

	headers := authHeaders(project.AdminToken)
	firstPage := requestJSONWithHeaders(t, handler, http.MethodGet, orgPath+"/integrations?limit=1", "", "",
		http.StatusOK, headers)
	firstRows := testutil.RequireType[[]any](t, firstPage["data"])
	require.Len(t, firstRows, 1)
	require.Equal(t, secondID, testutil.RequireType[map[string]any](t, firstRows[0])["id"])
	cursor := testutil.RequireType[string](t, firstPage["next_cursor"])
	secondPage := requestJSONWithHeaders(t, handler, http.MethodGet,
		orgPath+"/integrations?limit=1&cursor="+url.QueryEscape(cursor), "", "", http.StatusOK, headers)
	secondRows := testutil.RequireType[[]any](t, secondPage["data"])
	require.Len(t, secondRows, 1)
	require.Equal(t, firstID, testutil.RequireType[map[string]any](t, secondRows[0])["id"])
	require.Nil(t, secondPage["next_cursor"])

	memberToken := func(email, projectRole string) string {
		t.Helper()
		user, err := storagetest.CreateVerifiedUser(ctx, pool,
			storagetest.CreateVerifiedUserInput{Email: email, DisplayName: email})
		require.NoError(t, err)
		pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx,
			identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "member"})
		require.NoError(t, err)
		_, err = store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
			OrgID: project.OrgUUID, UserID: user.ID, Role: "member",
		})
		require.NoError(t, err)
		if projectRole != "" {
			_, err = store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
				OrgID: project.OrgUUID, ProjectID: secondProject.ProjectUUID, UserID: user.ID, Role: projectRole,
			})
			require.NoError(t, err)
		}
		return pat.Token
	}
	assertOrgRows(t, "viewer integrations",
		listOrgRows(t, handler, orgPath+"/integrations", memberToken("org-integrations-viewer@example.com", "viewer")),
		map[string]string{secondID: secondProject.ProjectID})
	assertOrgRows(t, "member without projects",
		listOrgRows(t, handler, orgPath+"/integrations", memberToken("org-integrations-member@example.com", "")),
		map[string]string{})

	otherOrg := bootstrapPublicHTTPProject(t, handler, "org-integrations-other")
	requestJSONWithHeaders(t, handler, http.MethodGet, orgPath+"/integrations", "", "", http.StatusNotFound,
		authHeaders(otherOrg.AdminToken))
}

//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestGetOrgOverviewActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "overview-activity")
	activityPath := "/api/v1/orgs/" + project.OrgID + "/overview/activity"

	secondCreated := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Overview Activity Second"}`, "idem-overview-activity-second", http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secondProjectID := testutil.RequireType[string](t, secondCreated["id"])
	secondProjectUUID := mustPublicHTTPID(t, publicid.KindProject, secondProjectID)

	firstLaunch := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "overview-activity-first",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, firstLaunch.Agent,
		modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}, "",
	)
	spawnHTTPSubagentForTest(
		t, ctx, store, firstLaunch.Agent, firstLaunch.AgentConfig.ID, "overview-activity-child", "worker",
	)
	secondAgent := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, "overview-activity-second",
	).Agent
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, secondAgent,
		modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}, "",
	)

	get := func(token string, query url.Values) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(
			t, handler, http.MethodGet, activityPath+"?"+query.Encode(), "", "", http.StatusOK, authHeaders(token),
		)
	}
	since := func(value string) url.Values { return url.Values{"since": {value}} }

	assertOverviewActivity(t, get(project.AdminToken, since("2000-01-01T00:00:00Z")), 2, 2, 24)
	assertOverviewActivity(t, get(project.AdminToken, since("2100-01-01T00:00:00Z")), 0, 0, 0)
	assertOverviewActivity(
		t,
		get(project.AdminToken, url.Values{
			"since": {"2000-01-01T00:00:00Z"}, "until": {"2000-01-02T00:00:00Z"},
		}),
		0, 0, 0,
	)

	viewer, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "overview-activity-viewer@example.com", DisplayName: "Overview Activity Viewer",
	})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx, identitystore.CreatePersonalAccessTokenInput{UserID: viewer.ID, Name: "viewer"},
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
		OrgID: project.OrgUUID, ProjectID: secondProjectUUID, UserID: viewer.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	assertOverviewActivity(t, get(viewerPAT.Token, since("2000-01-01T00:00:00Z")), 1, 1, 12)

	for _, query := range []url.Values{
		{"until": {"2100-01-01T00:00:00Z"}},
		{"since": {"2100-01-01T00:00:00Z"}, "until": {"2000-01-01T00:00:00Z"}},
		{"since": {"not-a-time"}},
	} {
		requestJSONWithHeaders(
			t, handler, http.MethodGet, activityPath+"?"+query.Encode(), "", "", http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}

	otherOrg := bootstrapPublicHTTPProject(t, handler, "overview-activity-other")
	requestJSONWithHeaders(
		t, handler, http.MethodGet, activityPath+"?"+since("2000-01-01T00:00:00Z").Encode(), "", "",
		http.StatusNotFound, authHeaders(otherOrg.AdminToken),
	)
}

func assertOverviewActivity(
	t *testing.T,
	body map[string]any,
	agentsCreated, messagesSent, tokensUsed float64,
) {
	t.Helper()
	for field, want := range map[string]float64{
		"agents_created": agentsCreated,
		"messages_sent":  messagesSent,
		"tokens_used":    tokensUsed,
	} {
		if got := testutil.RequireType[float64](t, body[field]); got != want {
			t.Fatalf("%s = %v, want %v (body %+v)", field, got, want, body)
		}
	}
}

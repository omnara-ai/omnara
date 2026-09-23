//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestGetOrgOverviewUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "overview-usage")
	usagePath := "/api/v1/orgs/" + project.OrgID + "/overview/usage"

	secondCreated := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Overview Usage Second"}`, "idem-overview-usage-second", http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secondProjectID := testutil.RequireType[string](t, secondCreated["id"])
	secondProjectUUID := mustPublicHTTPID(t, publicid.KindProject, secondProjectID)

	firstAgent := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "overview-usage-first",
	).Agent
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, firstAgent,
		modelenvelope.Usage{InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 40, OutputTokens: 20},
		"0.0125",
	)
	secondAgent := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, "overview-usage-second",
	).Agent
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, secondAgent,
		modelenvelope.Usage{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 10},
		"",
	)
	firstAt := overviewUsageCallCreatedAt(t, ctx, pool, firstAgent.ID)
	secondAt := overviewUsageCallCreatedAt(t, ctx, pool, secondAgent.ID)
	latest := firstAt
	if secondAt.After(latest) {
		latest = secondAt
	}

	firstOnly := expectedUsage{
		modelCalls: 1, withCost: 1, cost: "0.0125", input: 100, uncached: 60, cacheRead: 40, output: 20,
	}
	secondOnly := expectedUsage{modelCalls: 1, cost: "0", input: 50, uncached: 50, output: 10}
	both := expectedUsage{
		modelCalls: 2, withCost: 1, cost: "0.0125", input: 150, uncached: 110, cacheRead: 40, output: 30,
	}
	nothing := expectedUsage{cost: "0"}

	get := func(token string, query url.Values) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(
			t, handler, http.MethodGet, usagePath+"?"+query.Encode(), "", "", http.StatusOK, authHeaders(token),
		)
	}
	window := func(since, until time.Time, extra ...string) url.Values {
		query := url.Values{"since": {since.Format(time.RFC3339Nano)}, "until": {until.Format(time.RFC3339Nano)}}
		for index := 0; index+1 < len(extra); index += 2 {
			query.Add(extra[index], extra[index+1])
		}
		return query
	}

	since := firstAt.Add(-48 * time.Hour)
	until := latest.Add(time.Second)
	daily := get(project.AdminToken, window(since, until))
	assertUsageTotals(t, "totals", overviewUsageObject(t, daily, "totals"), both)
	assertOverviewActiveAgents(t, daily, 2)
	assertUsageTotals(t, "previous_totals", overviewUsageObject(t, daily, "previous_totals"), nothing)
	modelGroups := testutil.RequireType[[]any](t, daily["groups"])
	if len(modelGroups) != 1 {
		t.Fatalf("model groups = %+v, want one model", modelGroups)
	}
	modelGroup := testutil.RequireType[map[string]any](t, modelGroups[0])
	modelID := testutil.RequireType[string](t, modelGroup["id"])
	if _, err := publicid.Decode(publicid.KindConfiguredModel, modelID); err != nil || modelGroup["name"] != "http-test" {
		t.Fatalf("model group = %+v (%v), want http-test", modelGroup, err)
	}
	assertUsageTotals(t, "groups[0]", testutil.RequireType[map[string]any](t, modelGroup["totals"]), both)
	dayStart := since.UTC().Truncate(24 * time.Hour)
	intervals := testutil.RequireType[[]any](t, daily["intervals"])
	if want := int(until.UTC().Truncate(24*time.Hour).Sub(dayStart)/(24*time.Hour)) + 1; len(intervals) != want {
		t.Fatalf("daily intervals = %d, want %d", len(intervals), want)
	}
	expectedByInterval := map[int]expectedUsage{}
	for _, call := range []struct {
		at    time.Time
		usage expectedUsage
	}{{firstAt, firstOnly}, {secondAt, secondOnly}} {
		index := int(call.at.UTC().Truncate(24*time.Hour).Sub(dayStart) / (24 * time.Hour))
		expectedByInterval[index] = addExpectedUsage(expectedByInterval[index], call.usage)
	}
	for index, raw := range intervals {
		interval := testutil.RequireType[map[string]any](t, raw)
		start := parseOverviewUsageTime(t, interval["start"])
		if want := dayStart.Add(time.Duration(index) * 24 * time.Hour); !start.Equal(want) {
			t.Fatalf("intervals[%d].start = %v, want %v", index, start, want)
		}
		want, used := expectedByInterval[index]
		if !used {
			want = nothing
		}
		assertUsageTotals(t, "intervals.totals", overviewUsageObject(t, interval, "totals"), want)
		intervalGroups := testutil.RequireType[[]any](t, interval["groups"])
		if used != (len(intervalGroups) == 1) {
			t.Fatalf("intervals[%d].groups = %+v, used = %v", index, intervalGroups, used)
		}
		if used {
			intervalGroup := testutil.RequireType[map[string]any](t, intervalGroups[0])
			if intervalGroup["id"] != modelID {
				t.Fatalf("intervals[%d].groups[0].id = %v, want %s", index, intervalGroup["id"], modelID)
			}
			assertUsageTotals(t, "intervals.groups[0]", overviewUsageObject(t, intervalGroup, "totals"), want)
		}
	}

	byProject := get(project.AdminToken, window(since, until, "group_by", "project"))
	projectGroups := testutil.RequireType[[]any](t, byProject["groups"])
	if len(projectGroups) != 2 {
		t.Fatalf("project groups = %+v, want both projects", projectGroups)
	}
	for index, want := range []struct {
		id    string
		usage expectedUsage
	}{{project.ProjectID, firstOnly}, {secondProjectID, secondOnly}} {
		group := testutil.RequireType[map[string]any](t, projectGroups[index])
		if group["id"] != want.id {
			t.Fatalf("project groups[%d].id = %v, want %s", index, group["id"], want.id)
		}
		assertUsageTotals(t, "project group", overviewUsageObject(t, group, "totals"), want.usage)
	}
	if name := testutil.RequireType[map[string]any](t, projectGroups[1])["name"]; name != "Overview Usage Second" {
		t.Fatalf("second project group name = %v", name)
	}
	limited := get(project.AdminToken, window(since, until, "group_by", "project", "limit", "1"))
	assertUsageTotals(t, "limited totals", overviewUsageObject(t, limited, "totals"), both)
	limitedGroups := testutil.RequireType[[]any](t, limited["groups"])
	if len(limitedGroups) != 1 || testutil.RequireType[map[string]any](t, limitedGroups[0])["id"] != project.ProjectID {
		t.Fatalf("limited groups = %+v, want the first project only", limitedGroups)
	}
	for _, raw := range testutil.RequireType[[]any](t, limited["intervals"]) {
		for _, group := range testutil.RequireType[[]any](t, testutil.RequireType[map[string]any](t, raw)["groups"]) {
			if id := testutil.RequireType[map[string]any](t, group)["id"]; id != project.ProjectID {
				t.Fatalf("limited interval group %v, want only %s", id, project.ProjectID)
			}
		}
	}

	hourly := get(project.AdminToken, window(firstAt.Add(-150*time.Minute), until, "interval", "hour"))
	assertUsageTotals(t, "hourly totals", overviewUsageObject(t, hourly, "totals"), both)
	hourStart := firstAt.Add(-150 * time.Minute).UTC().Truncate(time.Hour)
	for index, raw := range testutil.RequireType[[]any](t, hourly["intervals"]) {
		start := parseOverviewUsageTime(t, testutil.RequireType[map[string]any](t, raw)["start"])
		if want := hourStart.Add(time.Duration(index) * time.Hour); !start.Equal(want) {
			t.Fatalf("hourly intervals[%d].start = %v, want %v", index, start, want)
		}
	}

	losAngeles, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	local := get(project.AdminToken, window(since, until, "timezone", "America/Los_Angeles"))
	assertUsageTotals(t, "local totals", overviewUsageObject(t, local, "totals"), both)
	for index, raw := range testutil.RequireType[[]any](t, local["intervals"]) {
		start := parseOverviewUsageTime(t, testutil.RequireType[map[string]any](t, raw)["start"]).In(losAngeles)
		if start.Hour() != 0 || start.Minute() != 0 {
			t.Fatalf("local intervals[%d].start = %v, want Los Angeles midnight", index, start)
		}
	}

	afterCalls := latest.Add(time.Millisecond)
	comparison := get(project.AdminToken, window(afterCalls, afterCalls.Add(time.Hour)))
	assertUsageTotals(t, "comparison totals", overviewUsageObject(t, comparison, "totals"), nothing)
	assertOverviewActiveAgents(t, comparison, 0)
	assertUsageTotals(t, "comparison previous_totals", overviewUsageObject(t, comparison, "previous_totals"), both)
	if groups := testutil.RequireType[[]any](t, comparison["groups"]); len(groups) != 0 {
		t.Fatalf("comparison groups = %+v, want none", groups)
	}

	filtered := get(project.AdminToken, window(since, until, "project_ids", project.ProjectID))
	assertUsageTotals(t, "filtered totals", overviewUsageObject(t, filtered, "totals"), firstOnly)
	assertOverviewActiveAgents(t, filtered, 1)
	unknownProjectID := testPublicID(t, publicid.KindProject, httpTestID("overview-usage-unknown-project"))
	unknown := get(project.AdminToken, window(since, until, "project_ids", unknownProjectID))
	assertUsageTotals(t, "unknown project totals", overviewUsageObject(t, unknown, "totals"), nothing)

	viewer, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "overview-usage-viewer@example.com", DisplayName: "Overview Usage Viewer",
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
	viewerOnly := get(viewerPAT.Token, window(since, until))
	assertUsageTotals(t, "viewer without projects", overviewUsageObject(t, viewerOnly, "totals"), nothing)
	assertOverviewActiveAgents(t, viewerOnly, 0)
	assertUsageTotals(
		t, "viewer without projects previous", overviewUsageObject(t, viewerOnly, "previous_totals"), nothing,
	)
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: secondProjectUUID, UserID: viewer.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	viewerUsage := get(viewerPAT.Token, window(since, until, "group_by", "project"))
	assertUsageTotals(t, "viewer totals", overviewUsageObject(t, viewerUsage, "totals"), secondOnly)
	assertOverviewActiveAgents(t, viewerUsage, 1)
	viewerGroups := testutil.RequireType[[]any](t, viewerUsage["groups"])
	if len(viewerGroups) != 1 || testutil.RequireType[map[string]any](t, viewerGroups[0])["id"] != secondProjectID {
		t.Fatalf("viewer groups = %+v, want the second project only", viewerGroups)
	}
	viewerFiltered := get(viewerPAT.Token, window(since, until, "project_ids", project.ProjectID))
	assertUsageTotals(t, "viewer unreadable filter", overviewUsageObject(t, viewerFiltered, "totals"), nothing)

	for _, query := range []url.Values{
		window(until, since),
		window(since, until, "timezone", "Mars/Olympus_Mons"),
		window(since, until, "timezone", "Local"),
		window(since, since.Add(400*time.Hour), "interval", "hour"),
		window(since, until, "interval", "week"),
		window(since, until, "group_by", "agent"),
		window(since, until, "limit", "0"),
		window(since, until, "limit", "11"),
		window(since, until, "project_ids", "not-a-project"),
		{"until": {until.Format(time.RFC3339Nano)}},
	} {
		requestJSONWithHeaders(
			t, handler, http.MethodGet, usagePath+"?"+query.Encode(), "", "", http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}

	otherOrg := bootstrapPublicHTTPProject(t, handler, "overview-usage-other")
	requestJSONWithHeaders(
		t, handler, http.MethodGet, usagePath+"?"+window(since, until).Encode(), "", "", http.StatusNotFound,
		authHeaders(otherOrg.AdminToken),
	)
}

func TestGetOrgOverviewUsageByProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "overview-usage-profiles")
	usagePath := "/api/v1/orgs/" + project.OrgID + "/overview/usage"

	first := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "overview-profile-first",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, first.Agent,
		modelenvelope.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}, "",
	)
	second := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "overview-profile-second",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, second.Agent,
		modelenvelope.Usage{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 10}, "",
	)
	child := spawnHTTPSubagentForTest(
		t, ctx, store, first.Agent, first.AgentConfig.ID, "overview-profile-child", "worker",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, child,
		modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}, "",
	)
	if err := store.Execution().DeleteAgentProfile(ctx, project.ProjectUUID, second.Agent.AgentProfileID); err != nil {
		t.Fatalf("delete second profile: %v", err)
	}

	since := overviewUsageCallCreatedAt(t, ctx, pool, first.Agent.ID).Add(-time.Hour)
	until := since
	for _, agentID := range []uuid.UUID{first.Agent.ID, second.Agent.ID, child.ID} {
		if at := overviewUsageCallCreatedAt(t, ctx, pool, agentID); at.After(until) {
			until = at
		}
	}
	until = until.Add(time.Second)
	get := func(extra ...string) map[string]any {
		t.Helper()
		query := url.Values{
			"since": {since.Format(time.RFC3339Nano)}, "until": {until.Format(time.RFC3339Nano)}, "group_by": {"profile"},
		}
		for index := 0; index+1 < len(extra); index += 2 {
			query.Set(extra[index], extra[index+1])
		}
		return requestJSONWithHeaders(
			t, handler, http.MethodGet, usagePath+"?"+query.Encode(), "", "", http.StatusOK,
			authHeaders(project.AdminToken),
		)
	}

	firstOnly := expectedUsage{modelCalls: 1, cost: "0", input: 100, uncached: 100, output: 20}
	secondOnly := expectedUsage{modelCalls: 1, cost: "0", input: 50, uncached: 50, output: 10}
	childOnly := expectedUsage{modelCalls: 1, cost: "0", input: 10, uncached: 10, output: 2}
	all := addExpectedUsage(addExpectedUsage(firstOnly, secondOnly), childOnly)
	firstProfileID := testPublicID(t, publicid.KindAgentProfile, first.Agent.AgentProfileID)
	secondProfileID := testPublicID(t, publicid.KindAgentProfile, second.Agent.AgentProfileID)

	byProfile := get()
	assertUsageTotals(t, "profile totals", overviewUsageObject(t, byProfile, "totals"), all)
	groups := testutil.RequireType[[]any](t, byProfile["groups"])
	if len(groups) != 3 {
		t.Fatalf("profile groups = %+v, want two profiles and no profile", groups)
	}
	for index, want := range []struct {
		id, name string
		usage    expectedUsage
	}{
		{firstProfileID, "overview-profile-first", firstOnly},
		{secondProfileID, "overview-profile-second", secondOnly},
		{"", "", childOnly},
	} {
		group := testutil.RequireType[map[string]any](t, groups[index])
		id, hasID := group["id"]
		name, hasName := group["name"]
		if want.id == "" && (hasID || hasName) || want.id != "" && (id != want.id || name != want.name) {
			t.Fatalf("profile groups[%d] = %+v, want id %q name %q", index, group, want.id, want.name)
		}
		assertUsageTotals(t, "profile group", overviewUsageObject(t, group, "totals"), want.usage)
	}
	intervalGroupIDs := map[any]bool{}
	for _, raw := range testutil.RequireType[[]any](t, byProfile["intervals"]) {
		for _, rawGroup := range testutil.RequireType[[]any](t, testutil.RequireType[map[string]any](t, raw)["groups"]) {
			group := testutil.RequireType[map[string]any](t, rawGroup)
			intervalGroupIDs[group["id"]] = true
			if _, hasID := group["id"]; !hasID {
				assertUsageTotals(t, "no profile interval group", overviewUsageObject(t, group, "totals"), childOnly)
			}
		}
	}
	if len(intervalGroupIDs) != 3 || !intervalGroupIDs[firstProfileID] || !intervalGroupIDs[secondProfileID] ||
		!intervalGroupIDs[nil] {
		t.Fatalf("interval group ids = %v, want both profiles and no profile", intervalGroupIDs)
	}

	limited := get("limit", "2")
	assertUsageTotals(t, "limited profile totals", overviewUsageObject(t, limited, "totals"), all)
	if limitedGroups := testutil.RequireType[[]any](t, limited["groups"]); len(limitedGroups) != 2 {
		t.Fatalf("limited profile groups = %+v, want the two profiles", limitedGroups)
	}
	for _, raw := range testutil.RequireType[[]any](t, limited["intervals"]) {
		for _, rawGroup := range testutil.RequireType[[]any](t, testutil.RequireType[map[string]any](t, raw)["groups"]) {
			if _, hasID := testutil.RequireType[map[string]any](t, rawGroup)["id"]; !hasID {
				t.Fatalf("limited interval groups include no profile: %+v", raw)
			}
		}
	}
}

func overviewUsageCallCreatedAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID uuid.UUID) time.Time {
	t.Helper()
	var createdAt time.Time
	if err := pool.QueryRow(
		ctx, `SELECT created_at FROM model_call_contexts WHERE agent_id = $1`, agentID,
	).Scan(&createdAt); err != nil {
		t.Fatalf("load model call created_at: %v", err)
	}
	return createdAt
}

func assertOverviewActiveAgents(t *testing.T, body map[string]any, want float64) {
	t.Helper()
	if got := testutil.RequireType[float64](t, body["active_agents"]); got != want {
		t.Fatalf("active_agents = %v, want %v", got, want)
	}
}

func overviewUsageObject(t *testing.T, body map[string]any, field string) map[string]any {
	t.Helper()
	return testutil.RequireType[map[string]any](t, body[field])
}

func parseOverviewUsageTime(t *testing.T, raw any) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, testutil.RequireType[string](t, raw))
	if err != nil {
		t.Fatalf("parse interval start: %v", err)
	}
	return parsed
}

func addExpectedUsage(left, right expectedUsage) expectedUsage {
	cost := left.cost
	switch {
	case cost == "" || cost == "0":
		cost = right.cost
	case right.cost != "" && right.cost != "0":
		summed, _ := modelenvelope.SumProviderReportedCostUSD(cost, right.cost)
		cost = string(summed)
	}
	return expectedUsage{
		modelCalls: left.modelCalls + right.modelCalls,
		withCost:   left.withCost + right.withCost,
		cost:       cost,
		input:      left.input + right.input,
		uncached:   left.uncached + right.uncached,
		cacheRead:  left.cacheRead + right.cacheRead,
		cacheWrite: left.cacheWrite + right.cacheWrite,
		output:     left.output + right.output,
		reasoning:  left.reasoning + right.reasoning,
	}
}

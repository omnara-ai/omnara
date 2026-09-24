//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestGetOrgOverviewTodayAndUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "overview-usage")
	overviewPath := "/api/v1/orgs/" + project.OrgID + "/overview"

	secondCreated := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Overview Usage Second"}`, "idem-overview-usage-second", http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secondProjectID := testutil.RequireType[string](t, secondCreated["id"])
	secondProjectUUID := mustPublicHTTPID(t, publicid.KindProject, secondProjectID)

	first := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "overview-usage-first",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, first.Agent,
		modelenvelope.Usage{InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 40, OutputTokens: 20},
		"0.0125",
	)
	second := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, "overview-usage-second",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, second.Agent,
		modelenvelope.Usage{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 10}, "",
	)
	child := spawnHTTPSubagentForTest(
		t, ctx, store, first.Agent, first.AgentConfig.ID, "overview-usage-child", "worker",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, child,
		modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}, "",
	)
	if err := store.Execution().DeleteAgentProfile(ctx, secondProjectUUID, second.Agent.AgentProfileID); err != nil {
		t.Fatalf("delete second profile: %v", err)
	}

	firstOnly := expectedUsage{
		modelCalls: 1, withCost: 1, cost: "0.0125", input: 100, uncached: 60, cacheRead: 40, output: 20,
	}
	secondOnly := expectedUsage{modelCalls: 1, cost: "0", input: 50, uncached: 50, output: 10}
	childOnly := expectedUsage{modelCalls: 1, cost: "0", input: 10, uncached: 10, output: 2}
	all := addExpectedUsage(addExpectedUsage(firstOnly, secondOnly), childOnly)
	nothing := expectedUsage{cost: "0"}
	firstProfileID := testPublicID(t, publicid.KindAgentProfile, first.Agent.AgentProfileID)
	secondProfileID := testPublicID(t, publicid.KindAgentProfile, second.Agent.AgentProfileID)

	zone := overviewNoonTimezone(time.Now())
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	local := url.Values{"timezone": {zone}}
	get := func(token string, query url.Values, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(
			t, handler, http.MethodGet, overviewPath+"?"+query.Encode(), "", "", status, authHeaders(token),
		)
	}

	overview := get(project.AdminToken, local, http.StatusOK)
	assertOverviewToday(t, overview, 2, 2)
	usage := overviewUsageObject(t, overview, "usage")
	assertUsageTotals(t, "usage.totals", overviewUsageObject(t, usage, "totals"), all)
	assertOverviewActiveAgents(t, usage, 2)
	models := testutil.RequireType[[]any](t, usage["models"])
	if len(models) != 1 {
		t.Fatalf("models = %+v, want one model", models)
	}
	model := testutil.RequireType[map[string]any](t, models[0])
	modelID := testutil.RequireType[string](t, model["id"])
	if _, err := publicid.Decode(publicid.KindConfiguredModel, modelID); err != nil || model["name"] != "http-test" {
		t.Fatalf("model = %+v (%v), want http-test", model, err)
	}
	assertUsageTotals(t, "usage.models[0]", overviewUsageObject(t, model, "totals"), all)
	assertOverviewUsageProfiles(t, usage["profiles"], []overviewUsageProfile{
		{id: firstProfileID, name: "overview-usage-first", usage: firstOnly},
		{id: secondProfileID, name: "overview-usage-second", usage: secondOnly},
		{usage: childOnly},
	})
	days := testutil.RequireType[[]any](t, usage["days"])
	if len(days) != orgOverviewUsageDays {
		t.Fatalf("days = %d, want %d", len(days), orgOverviewUsageDays)
	}
	var previous time.Time
	for index, raw := range days {
		day := testutil.RequireType[map[string]any](t, raw)
		start := parseOverviewUsageTime(t, day["start"]).In(location)
		if start.Hour() != 0 || start.Minute() != 0 || index > 0 && !start.Equal(previous.Add(24*time.Hour)) {
			t.Fatalf("days[%d].start = %v, want the local midnight after %v", index, start, previous)
		}
		previous = start
		if index == len(days)-1 {
			continue
		}
		assertUsageTotals(t, "earlier day", overviewUsageObject(t, day, "totals"), nothing)
		dayModels := testutil.RequireType[[]any](t, day["models"])
		if dayProfiles := testutil.RequireType[[]any](t, day["profiles"]); len(dayModels) != 0 || len(dayProfiles) != 0 {
			t.Fatalf("days[%d] = %+v, want no usage", index, day)
		}
	}
	today := testutil.RequireType[map[string]any](t, days[len(days)-1])
	if now := time.Now(); now.Before(previous) || !now.Before(previous.Add(24*time.Hour)) {
		t.Fatalf("last day starts %v, want the day containing %v", previous, now)
	}
	assertUsageTotals(t, "today", overviewUsageObject(t, today, "totals"), all)
	todayModels := testutil.RequireType[[]any](t, today["models"])
	if len(todayModels) != 1 {
		t.Fatalf("today models = %+v, want one model", todayModels)
	}
	todayModel := testutil.RequireType[map[string]any](t, todayModels[0])
	if todayModel["id"] != modelID || todayModel["tokens"] != expectedTokens(all) {
		t.Fatalf("today model = %+v, want %s with %v tokens", todayModel, modelID, expectedTokens(all))
	}
	assertOverviewUsageDayProfiles(t, today["profiles"], []overviewUsageProfile{
		{id: firstProfileID, usage: firstOnly},
		{id: secondProfileID, usage: secondOnly},
		{usage: childOnly},
	})

	utc := get(project.AdminToken, url.Values{}, http.StatusOK)
	for index, raw := range testutil.RequireType[[]any](t, overviewUsageObject(t, utc, "usage")["days"]) {
		start := parseOverviewUsageTime(t, testutil.RequireType[map[string]any](t, raw)["start"]).UTC()
		if start.Hour() != 0 || start.Minute() != 0 {
			t.Fatalf("default days[%d].start = %v, want UTC midnight", index, start)
		}
	}

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
	unscoped := get(viewerPAT.Token, local, http.StatusOK)
	assertOverviewToday(t, unscoped, 0, 0)
	unscopedUsage := overviewUsageObject(t, unscoped, "usage")
	assertUsageTotals(t, "viewer without projects", overviewUsageObject(t, unscopedUsage, "totals"), nothing)
	assertOverviewActiveAgents(t, unscopedUsage, 0)
	if len(testutil.RequireType[[]any](t, unscopedUsage["models"])) != 0 ||
		len(testutil.RequireType[[]any](t, unscopedUsage["profiles"])) != 0 ||
		len(testutil.RequireType[[]any](t, unscopedUsage["days"])) != orgOverviewUsageDays {
		t.Fatalf("viewer without projects usage = %+v", unscopedUsage)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: secondProjectUUID, UserID: viewer.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	scoped := get(viewerPAT.Token, local, http.StatusOK)
	assertOverviewToday(t, scoped, 1, 1)
	scopedUsage := overviewUsageObject(t, scoped, "usage")
	assertUsageTotals(t, "viewer usage", overviewUsageObject(t, scopedUsage, "totals"), secondOnly)
	assertOverviewActiveAgents(t, scopedUsage, 1)
	assertOverviewUsageProfiles(t, scopedUsage["profiles"], []overviewUsageProfile{
		{id: secondProfileID, name: "overview-usage-second", usage: secondOnly},
	})

	for _, invalid := range []string{"Mars/Olympus_Mons", "Local", ""} {
		get(project.AdminToken, url.Values{"timezone": {invalid}}, http.StatusBadRequest)
	}
	otherOrg := bootstrapPublicHTTPProject(t, handler, "overview-usage-other")
	get(otherOrg.AdminToken, local, http.StatusNotFound)
}

func overviewNoonTimezone(now time.Time) string {
	offset := 12 - now.UTC().Hour()
	switch {
	case offset > 0:
		return fmt.Sprintf("Etc/GMT-%d", offset)
	case offset < 0:
		return fmt.Sprintf("Etc/GMT+%d", -offset)
	default:
		return "Etc/GMT"
	}
}

type overviewUsageProfile struct {
	id, name string
	usage    expectedUsage
}

func assertOverviewUsageProfiles(t *testing.T, raw any, want []overviewUsageProfile) {
	t.Helper()
	profiles := testutil.RequireType[[]any](t, raw)
	if len(profiles) != len(want) {
		t.Fatalf("profiles = %+v, want %d", profiles, len(want))
	}
	for index, expected := range want {
		profile := testutil.RequireType[map[string]any](t, profiles[index])
		id, hasID := profile["id"]
		name, hasName := profile["name"]
		if expected.id == "" && (hasID || hasName) ||
			expected.id != "" && (id != expected.id || expected.name != "" && name != expected.name) {
			t.Fatalf("profiles[%d] = %+v, want id %q name %q", index, profile, expected.id, expected.name)
		}
		assertUsageTotals(t, "profile", overviewUsageObject(t, profile, "totals"), expected.usage)
	}
}

func assertOverviewUsageDayProfiles(t *testing.T, raw any, want []overviewUsageProfile) {
	t.Helper()
	profiles := testutil.RequireType[[]any](t, raw)
	if len(profiles) != len(want) {
		t.Fatalf("day profiles = %+v, want %d", profiles, len(want))
	}
	for index, expected := range want {
		profile := testutil.RequireType[map[string]any](t, profiles[index])
		id, hasID := profile["id"]
		if expected.id == "" && hasID || expected.id != "" && id != expected.id ||
			profile["tokens"] != expectedTokens(expected.usage) {
			t.Fatalf("day profiles[%d] = %+v, want id %q with %v tokens",
				index, profile, expected.id, expectedTokens(expected.usage))
		}
	}
}

func expectedTokens(usage expectedUsage) float64 {
	return float64(usage.input + usage.output)
}

func assertOverviewToday(t *testing.T, overview map[string]any, agentsCreated, messagesSent float64) {
	t.Helper()
	today := overviewUsageObject(t, overview, "today")
	for field, want := range map[string]float64{"agents_created": agentsCreated, "messages_sent": messagesSent} {
		if got := testutil.RequireType[float64](t, today[field]); got != want {
			t.Fatalf("today.%s = %v, want %v (today %+v)", field, got, want, today)
		}
	}
}

func assertOverviewActiveAgents(t *testing.T, usage map[string]any, want float64) {
	t.Helper()
	if got := testutil.RequireType[float64](t, usage["active_agents"]); got != want {
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
		t.Fatalf("parse day start: %v", err)
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

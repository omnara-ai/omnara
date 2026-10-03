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

func TestGetOrgUsageTimeseries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "usage-series")
	firstProjectID := testPublicID(t, publicid.KindProject, project.ProjectUUID)

	secondCreated := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Usage Series Second"}`, "idem-usage-series-second", http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secondProjectID := testutil.RequireType[string](t, secondCreated["id"])
	secondProjectUUID := mustPublicHTTPID(t, publicid.KindProject, secondProjectID)

	first := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "usage-series-first",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, first.Agent,
		modelenvelope.Usage{InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 40, OutputTokens: 20},
		"0.0125",
	)
	second := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, "usage-series-second",
	)
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, secondProjectUUID, project.AdminUserUUID, second.Agent,
		modelenvelope.Usage{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 10}, "",
	)
	child := spawnHTTPSubagentForTest(
		t, ctx, store, first.Agent, first.AgentConfig.ID, "usage-series-child", "worker",
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
	now := time.Now().In(location)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	since := todayStart.AddDate(0, 0, -29)
	window := func(extra url.Values) url.Values {
		query := url.Values{"timezone": {zone}, "since": {since.Format(time.RFC3339)}}
		for key, values := range extra {
			query[key] = values
		}
		return query
	}
	getIn := func(orgID, token string, query url.Values, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(
			t, handler, http.MethodGet, orgUsageTimeseriesPath(orgID, query), "", "", status, authHeaders(token),
		)
	}
	get := func(token string, query url.Values, status int) map[string]any {
		t.Helper()
		return getIn(project.OrgID, token, query, status)
	}

	byModel := get(project.AdminToken, window(url.Values{"group_by": {"model"}}), http.StatusOK)
	if byModel["metric"] != "sum_tokens" || byModel["interval"] != "day" || byModel["timezone"] != zone {
		t.Fatalf("timeseries = %+v, want daily sum_tokens in %s", byModel, zone)
	}
	assertUsageTotals(t, "totals", overviewUsageObject(t, byModel, "totals"), all)
	assertOverviewActiveAgents(t, byModel, 2)
	bucketStarts := testutil.RequireType[[]any](t, byModel["bucket_starts"])
	if len(bucketStarts) != 30 {
		t.Fatalf("bucket_starts = %d, want 30", len(bucketStarts))
	}
	for index, raw := range bucketStarts {
		start := parseOverviewUsageTime(t, raw).In(location)
		if want := since.AddDate(0, 0, index); !start.Equal(want) {
			t.Fatalf("bucket_starts[%d] = %v, want %v", index, start, want)
		}
	}
	models := testutil.RequireType[[]any](t, byModel["series"])
	if len(models) != 1 {
		t.Fatalf("model series = %+v, want one model", models)
	}
	model := testutil.RequireType[map[string]any](t, models[0])
	if _, err := publicid.Decode(publicid.KindConfiguredModel, testutil.RequireType[string](t, model["id"])); err != nil ||
		model["kind"] != "model" || model["name"] != "http-test" {
		t.Fatalf("model series = %+v (%v), want http-test", model, err)
	}
	assertUsageSeriesValues(t, model, 30, expectedTokens(all))

	byProfile := get(project.AdminToken, window(url.Values{"group_by": {"profile"}}), http.StatusOK)
	assertUsageSeries(t, byProfile["series"], []usageSeriesExpectation{
		{kind: "profile", id: firstProfileID, name: "usage-series-first", total: expectedTokens(firstOnly)},
		{kind: "profile", id: secondProfileID, name: "usage-series-second", total: expectedTokens(secondOnly)},
		{kind: "no_profile", total: expectedTokens(childOnly)},
	})
	limited := get(project.AdminToken, window(url.Values{"group_by": {"profile"}, "group_limit": {"1"}}), http.StatusOK)
	assertUsageSeries(t, limited["series"], []usageSeriesExpectation{
		{kind: "profile", id: firstProfileID, name: "usage-series-first", total: expectedTokens(firstOnly)},
		{kind: "other", total: expectedTokens(secondOnly) + expectedTokens(childOnly)},
	})
	cost := get(project.AdminToken, window(url.Values{"metric": {"sum_cost"}, "group_by": {"profile"}}), http.StatusOK)
	assertUsageSeries(t, cost["series"], []usageSeriesExpectation{
		{kind: "profile", id: firstProfileID, name: "usage-series-first", total: 0.0125},
	})

	scopedToFirst := get(project.AdminToken, window(url.Values{"project_ids": {firstProjectID}}), http.StatusOK)
	assertUsageTotals(t, "first project", overviewUsageObject(t, scopedToFirst, "totals"),
		addExpectedUsage(firstOnly, childOnly))
	assertUsageSeries(t, scopedToFirst["series"], []usageSeriesExpectation{
		{kind: "all", total: expectedTokens(firstOnly) + expectedTokens(childOnly)},
	})
	profileOnly := get(project.AdminToken, window(url.Values{"agent_profile_ids": {firstProfileID}}), http.StatusOK)
	assertUsageTotals(t, "profile", overviewUsageObject(t, profileOnly, "totals"), firstOnly)
	withSubagents := get(project.AdminToken, window(url.Values{
		"agent_profile_ids": {firstProfileID}, "include_subagents": {"true"},
	}), http.StatusOK)
	assertUsageTotals(t, "profile with subagents", overviewUsageObject(t, withSubagents, "totals"),
		addExpectedUsage(firstOnly, childOnly))
	allTime := get(project.AdminToken, url.Values{"timezone": {zone}}, http.StatusOK)
	allTimeStarts := testutil.RequireType[[]any](t, allTime["bucket_starts"])
	if allTime["interval"] != "day" || len(allTimeStarts) != 1 ||
		!parseOverviewUsageTime(t, allTimeStarts[0]).Equal(todayStart) {
		t.Fatalf("all-time timeseries = %+v, want one bucket starting %v", allTime, todayStart)
	}
	assertUsageTotals(t, "all time", overviewUsageObject(t, allTime, "totals"), all)
	yearly := get(project.AdminToken, url.Values{
		"since": {todayStart.AddDate(-1, 0, 0).Format(time.RFC3339)},
	}, http.StatusOK)
	if yearly["interval"] != "week" || yearly["timezone"] != "UTC" {
		t.Fatalf("yearly timeseries interval %v timezone %v, want weekly UTC", yearly["interval"], yearly["timezone"])
	}
	for index, raw := range testutil.RequireType[[]any](t, yearly["bucket_starts"]) {
		start := parseOverviewUsageTime(t, raw).UTC()
		if start.Weekday() != time.Monday || start.Hour() != 0 {
			t.Fatalf("weekly bucket_starts[%d] = %v, want a UTC Monday midnight", index, start)
		}
	}
	hourStart := now.Truncate(time.Hour)
	hourly := get(project.AdminToken, url.Values{
		"timezone": {zone},
		"since":    {hourStart.Add(-23 * time.Hour).Format(time.RFC3339)},
		"until":    {hourStart.Add(time.Hour).Format(time.RFC3339)},
		"interval": {"hour"},
	}, http.StatusOK)
	hourlyStarts := testutil.RequireType[[]any](t, hourly["bucket_starts"])
	if hourly["interval"] != "hour" || len(hourlyStarts) != 24 ||
		!parseOverviewUsageTime(t, hourlyStarts[23]).Equal(hourStart) {
		t.Fatalf("hourly timeseries = %+v, want 24 buckets ending at %v", hourly, hourStart)
	}
	assertUsageTotals(t, "last 24 hours", overviewUsageObject(t, hourly, "totals"), all)

	viewer, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "usage-series-viewer@example.com", DisplayName: "Usage Series Viewer",
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
	unscoped := get(viewerPAT.Token, window(nil), http.StatusOK)
	assertUsageTotals(t, "viewer without projects", overviewUsageObject(t, unscoped, "totals"), nothing)
	assertOverviewActiveAgents(t, unscoped, 0)
	assertUsageSeries(t, unscoped["series"], []usageSeriesExpectation{{kind: "all"}})
	get(viewerPAT.Token, window(url.Values{"project_ids": {secondProjectID}}), http.StatusNotFound)
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: secondProjectUUID, UserID: viewer.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	scoped := get(viewerPAT.Token, window(url.Values{"group_by": {"profile"}}), http.StatusOK)
	assertUsageTotals(t, "viewer usage", overviewUsageObject(t, scoped, "totals"), secondOnly)
	assertOverviewActiveAgents(t, scoped, 1)
	assertUsageSeries(t, scoped["series"], []usageSeriesExpectation{
		{kind: "profile", id: secondProfileID, name: "usage-series-second", total: expectedTokens(secondOnly)},
	})

	for _, query := range []url.Values{
		{"timezone": {"Mars/Olympus_Mons"}},
		{"metric": {"sum_everything"}},
		{"interval": {"minute"}},
		{"since": {since.Format(time.RFC3339)}, "interval": {"hour"}},
		{"since": {now.Format(time.RFC3339)}, "until": {now.Add(-time.Hour).Format(time.RFC3339)}},
		// until defaults to now, so a future since is a bad request too.
		{"since": {now.Add(24 * time.Hour).Format(time.RFC3339)}},
		{"since": {now.AddDate(-2, 0, 0).Format(time.RFC3339)}, "interval": {"day"}},
		{"project_ids": {"not-a-project"}},
	} {
		get(project.AdminToken, query, http.StatusBadRequest)
	}
	otherOrg := bootstrapPublicHTTPProject(t, handler, "usage-series-other")
	get(otherOrg.AdminToken, window(nil), http.StatusNotFound)
	otherUsage := getIn(otherOrg.OrgID, otherOrg.AdminToken, window(nil), http.StatusOK)
	assertUsageTotals(t, "other org", overviewUsageObject(t, otherUsage, "totals"), nothing)
}

func TestGetOrgUsageTimeseriesRequiresMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "usage-series-outsider")
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "usage-series-orgless@example.com", DisplayName: "Orgless",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx, identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "orgless"},
	)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	requestJSONWithHeaders(
		t, handler, http.MethodGet, orgUsageTimeseriesPath(project.OrgID, url.Values{}), "", "",
		http.StatusNotFound, authHeaders(token.Token),
	)
}

func orgUsageTimeseriesPath(orgID string, query url.Values) string {
	return "/api/v1/orgs/" + orgID + "/usage/timeseries?" + query.Encode()
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

type usageSeriesExpectation struct {
	kind, id, name string
	total          float64
}

func assertUsageSeries(t *testing.T, raw any, want []usageSeriesExpectation) {
	t.Helper()
	series := testutil.RequireType[[]any](t, raw)
	if len(series) != len(want) {
		t.Fatalf("series = %+v, want %d", series, len(want))
	}
	for index, expected := range want {
		item := testutil.RequireType[map[string]any](t, series[index])
		id, hasID := item["id"]
		name, hasName := item["name"]
		if item["kind"] != expected.kind ||
			expected.id == "" && (hasID || hasName) ||
			expected.id != "" && (id != expected.id || name != expected.name) {
			t.Fatalf("series[%d] = %+v, want %+v", index, item, expected)
		}
		values := testutil.RequireType[[]any](t, item["values"])
		assertUsageSeriesValues(t, item, len(values), expected.total)
	}
}

// assertUsageSeriesValues checks a series has buckets values that are zero
// except the last, which holds all of total.
func assertUsageSeriesValues(t *testing.T, item map[string]any, buckets int, total float64) {
	t.Helper()
	values := testutil.RequireType[[]any](t, item["values"])
	if len(values) != buckets || item["total"] != total {
		t.Fatalf("series %+v, want %d buckets totalling %v", item, buckets, total)
	}
	for index, raw := range values {
		want := 0.0
		if index == len(values)-1 {
			want = total
		}
		if raw != want {
			t.Fatalf("series values[%d] = %v, want %v (series %+v)", index, raw, want, item)
		}
	}
}

func expectedTokens(usage expectedUsage) float64 {
	return float64(usage.input + usage.output)
}

func assertOverviewActiveAgents(t *testing.T, timeseries map[string]any, want float64) {
	t.Helper()
	if got := testutil.RequireType[float64](t, timeseries["active_agents"]); got != want {
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
		t.Fatalf("parse bucket start: %v", err)
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

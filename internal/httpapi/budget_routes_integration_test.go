//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestBudgetRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "budget-routes")
	admin := authHeaders(project.AdminToken)
	endpoint := "/api/v1/orgs/" + project.OrgID + "/budgets"
	projectScope := map[string]any{"type": "project", "project_id": project.ProjectID}
	projectQuery := "?scope_type=project&project_id=" + project.ProjectID
	budgetBody := func(scope map[string]any, interval, amount string) string {
		return integrationHTTPJSON(t, map[string]any{"scope": scope, "interval": interval, "amount_usd": amount})
	}
	listIDs := func(query string) []string {
		listed := requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+query, "", "", http.StatusOK, admin)
		var ids []string
		for _, raw := range testutil.RequireType[[]any](t, listed["data"]) {
			budget := testutil.RequireType[map[string]any](t, raw)
			require.Contains(t, budget, "usage")
			ids = append(ids, testutil.RequireType[string](t, budget["id"]))
		}
		return ids
	}

	var projectBudgetID string
	for _, scope := range []map[string]any{{"type": "org"}, projectScope} {
		query := "?scope_type=org"
		if scope["type"] == "project" {
			query = projectQuery
		}
		created := requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
			budgetBody(scope, "month", "500.50"), "", http.StatusCreated, admin)
		require.Equal(t, scope, created["scope"])
		require.Equal(t, project.OrgID, created["org_id"])
		require.Equal(t, "month", created["interval"])
		require.Equal(t, "500.5", created["amount_usd"])
		require.NotContains(t, created, "usage")
		requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
			budgetBody(scope, "month", "1"), "", http.StatusConflict, admin)
		id := testutil.RequireType[string](t, created["id"])
		path := endpoint + "/" + id

		got := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, admin)
		require.Equal(t, map[string]any{"amount_usd": "0"}, got["usage"])
		delete(got, "usage")
		require.Equal(t, created, got)

		for _, invalid := range []string{
			`{}`, `{"amount_usd":null}`, `{"amount_usd":"0"}`, `{"amount_usd":"0.000"}`, `{"amount_usd":"-1"}`,
			`{"amount_usd":"abc"}`, `{"amount_usd":500}`, `{"amount_usd":"1.0000000000000001"}`,
			`{"amount_usd":"1","interval":"week"}`,
		} {
			requestJSONWithHeaders(t, handler, http.MethodPatch, path, invalid, "", http.StatusBadRequest, admin)
		}
		updated := requestJSONWithHeaders(t, handler, http.MethodPatch, path,
			`{"amount_usd":"10"}`, "", http.StatusOK, admin)
		require.Equal(t, "10", updated["amount_usd"])
		require.Equal(t, scope, updated["scope"])
		require.Equal(t, "month", updated["interval"])

		weekly := requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
			budgetBody(scope, "week", "1"), "", http.StatusCreated, admin)
		require.Equal(t, []string{id, testutil.RequireType[string](t, weekly["id"])}, listIDs(query))

		requestJSONWithHeaders(t, handler, http.MethodDelete, path, "", "", http.StatusNoContent, admin)
		requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusNotFound, admin)
		requestJSONWithHeaders(t, handler, http.MethodPatch, path, `{"amount_usd":"1"}`, "", http.StatusNotFound, admin)
		requestJSONWithHeaders(t, handler, http.MethodDelete, path, "", "", http.StatusNotFound, admin)
		recreated := requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
			budgetBody(scope, "month", "1"), "", http.StatusCreated, admin)
		if scope["type"] == "project" {
			projectBudgetID = testutil.RequireType[string](t, recreated["id"])
		}
	}
	require.Len(t, listIDs("?scope_type=org"), 2)
	page := requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+"?limit=3", "", "", http.StatusOK, admin)
	require.Len(t, testutil.RequireType[[]any](t, page["data"]), 3)
	cursor := testutil.RequireType[string](t, page["next_cursor"])
	page = requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+"?limit=3&cursor="+url.QueryEscape(cursor),
		"", "", http.StatusOK, admin)
	require.Len(t, testutil.RequireType[[]any](t, page["data"]), 1)
	require.Nil(t, page["next_cursor"])

	for _, scope := range []map[string]any{
		{}, {"type": "agent"}, {"type": "org", "project_id": project.ProjectID}, {"type": "project"},
		{"type": "project", "project_id": "invalid"},
	} {
		requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
			budgetBody(scope, "day", "1"), "", http.StatusBadRequest, admin)
	}
	for _, body := range []string{
		budgetBody(map[string]any{"type": "org"}, "year", "1"),
		budgetBody(map[string]any{"type": "org"}, "day", "0"),
		`{"scope":{"type":"org"},"interval":"day"}`,
	} {
		requestJSONWithHeaders(t, handler, http.MethodPost, endpoint, body, "", http.StatusBadRequest, admin)
	}
	for _, query := range []string{
		"?project_id=" + project.ProjectID, "?scope_type=org&project_id=" + project.ProjectID,
		"?scope_type=project", "?scope_type=project&project_id=invalid", "?scope_type=agent",
	} {
		requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+query, "", "", http.StatusBadRequest, admin)
	}

	other := bootstrapPublicHTTPProject(t, handler, "budget-routes-other")
	for _, projectID := range []string{other.ProjectID, testPublicID(t, publicid.KindProject, uuid.New())} {
		requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+"?scope_type=project&project_id="+projectID,
			"", "", http.StatusNotFound, admin)
		requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
			budgetBody(map[string]any{"type": "project", "project_id": projectID}, "day", "1"),
			"", http.StatusNotFound, admin)
	}
	patchBody := func(method string) string {
		if method == http.MethodPatch {
			return `{"amount_usd":"1"}`
		}
		return ""
	}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		requestJSONWithHeaders(t, handler, method, "/api/v1/orgs/"+other.OrgID+"/budgets/"+projectBudgetID,
			patchBody(method), "", http.StatusNotFound, authHeaders(other.AdminToken))
	}

	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "budget-project-admin@example.com", DisplayName: "Project Admin",
	})
	require.NoError(t, err)
	_, err = store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: project.OrgUUID, UserID: user.ID, Role: "member",
	})
	require.NoError(t, err)
	_, err = store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: user.ID, Role: "admin",
	})
	require.NoError(t, err)
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "project admin"})
	require.NoError(t, err)
	member := authHeaders(pat.Token)
	requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+projectQuery, "", "", http.StatusForbidden, member)
	requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
		budgetBody(projectScope, "day", "1"), "", http.StatusForbidden, member)
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		requestJSONWithHeaders(t, handler, method, endpoint+"/"+projectBudgetID,
			patchBody(method), "", http.StatusForbidden, member)
	}

	requestJSONWithHeaders(t, handler, http.MethodDelete, project.ProjectPath, "", "", http.StatusNoContent, admin)
	requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+"/"+projectBudgetID, "", "", http.StatusNotFound, admin)
	requestJSONWithHeaders(t, handler, http.MethodGet, endpoint+projectQuery, "", "", http.StatusNotFound, admin)
	requestJSONWithHeaders(t, handler, http.MethodPost, endpoint,
		budgetBody(projectScope, "day", "1"), "", http.StatusNotFound, admin)
	require.Len(t, listIDs(""), 2)

	_, err = store.Organizations().DeleteOrganization(ctx, project.OrgUUID,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: project.AdminUserUUID})
	require.NoError(t, err)
	var live int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM budgets WHERE org_id = $1 AND deleted_at IS NULL`, project.OrgUUID).Scan(&live))
	require.Zero(t, live)
}

func TestBudgetConcurrentCreatesConflict(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "budget-concurrent")
	body := `{"scope":{"type":"org"},"interval":"day","amount_usd":"1"}`
	codes := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/budgets",
				strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+project.AdminToken)
			codes <- performRequest(handler, req).Code
		})
	}
	close(start)
	wg.Wait()
	close(codes)
	var got []int
	for code := range codes {
		got = append(got, code)
	}
	require.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, got)
}

func TestUsageReportsEffectiveCost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "usage-effective-costs")
	usage := modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5}
	for index, cost := range []modelenvelope.ProviderReportedCostUSD{"0.25", "0.1", ""} {
		agent := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID,
			"usage-effective-costs-"+strconv.Itoa(index))
		recordHTTPModelUsageForAgent(
			t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, agent.Agent, usage, cost,
		)
	}
	_, err := pool.Exec(ctx, `ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
UPDATE model_call_contexts
SET effective_cost_usd = CASE provider_reported_cost_usd WHEN 0.25 THEN 2 WHEN 0.1 THEN NULL ELSE 0.5 END,
    effective_cost_kind = CASE provider_reported_cost_usd
        WHEN 0.25 THEN 'custom' WHEN 0.1 THEN NULL ELSE 'estimated' END
WHERE project_id = $1 AND input_tokens_total IS NOT NULL`, project.ProjectUUID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ALTER TABLE model_call_contexts ENABLE TRIGGER model_call_contexts_transition_guard`)
	require.NoError(t, err)

	admin := authHeaders(project.AdminToken)
	report := requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/usage", "", "", http.StatusOK, admin)
	totals := testutil.RequireType[map[string]any](t, report["totals"])
	require.Equal(t, float64(3), totals["model_calls"])
	require.Equal(t, map[string]any{
		"provider_reported_usd":           "0.35",
		"model_calls_with_reported_cost":  float64(2),
		"effective_usd":                   "2.6",
		"model_calls_with_effective_cost": float64(3),
	}, totals["cost"])

	series := requestJSONWithHeaders(t, handler, http.MethodGet,
		orgUsageTimeseriesPath(project.OrgID, url.Values{"metric": {"sum_cost"}}), "", "", http.StatusOK, admin)
	var total float64
	for _, raw := range testutil.RequireType[[]any](t, series["series"]) {
		total += testutil.RequireType[float64](t, testutil.RequireType[map[string]any](t, raw)["total"])
	}
	require.InDelta(t, 2.6, total, 1e-12)
	require.Equal(t, totals["cost"], testutil.RequireType[map[string]any](t, series["totals"])["cost"])
}

func TestBudgetsApplyAcrossProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	first := bootstrapPublicHTTPProject(t, handler, "budget-projects")
	admin := authHeaders(first.AdminToken)
	created := requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/orgs/"+first.OrgID+"/projects",
		`{"name":"Budget Second"}`, "idem-budget-second", http.StatusCreated, admin)
	secondID := testutil.RequireType[string](t, created["id"])
	secondUUID := mustPublicHTTPID(t, publicid.KindProject, secondID)
	usage := modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5}
	agents := 0
	newAgent := func(projectID uuid.UUID) executionstore.AgentRecord {
		agents++
		return createHTTPRuntimeAgent(t, ctx, store, first.OrgUUID, projectID, first.AdminUserUUID,
			"budget-agent-"+strconv.Itoa(agents)).Agent
	}
	spend := func(projectID uuid.UUID, cost modelenvelope.ProviderReportedCostUSD) {
		recordHTTPModelUsageForAgent(t, ctx, store, first.OrgUUID, projectID, first.AdminUserUUID,
			newAgent(projectID), usage, cost)
	}
	denial := func(projectID uuid.UUID) string {
		_, claim := claimHTTPModelCallForAgent(t, ctx, store, first.OrgUUID, projectID, first.AdminUserUUID,
			newAgent(projectID))
		if claim.Claimed {
			return ""
		}
		require.Equal(t, storeerr.BudgetReachedCode, claim.Context.ErrorCode)
		return claim.Context.ErrorMessage
	}
	setBudget := func(scope map[string]any, amount string) map[string]any {
		created := requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/orgs/"+first.OrgID+"/budgets",
			integrationHTTPJSON(t, map[string]any{"scope": scope, "interval": "month", "amount_usd": amount}),
			"", http.StatusCreated, admin)
		return requestJSONWithHeaders(t, handler, http.MethodGet,
			"/api/v1/orgs/"+first.OrgID+"/budgets/"+testutil.RequireType[string](t, created["id"]),
			"", "", http.StatusOK, admin)
	}
	projectReached := storeerr.BudgetReachedMessage("project", "month")
	orgReached := storeerr.BudgetReachedMessage("org", "month")

	spend(first.ProjectUUID, "3")
	spend(secondUUID, "2")
	firstBudget := setBudget(map[string]any{"type": "project", "project_id": first.ProjectID}, "3")
	require.Equal(t, map[string]any{"amount_usd": "3"}, firstBudget["usage"])
	require.Equal(t, projectReached, denial(first.ProjectUUID))
	setBudget(map[string]any{"type": "project", "project_id": secondID}, "3")
	require.Empty(t, denial(secondUUID), "another project's spend must not use up this project's budget")

	orgBudget := setBudget(map[string]any{"type": "org"}, "5")
	require.Equal(t, map[string]any{"amount_usd": "5"}, orgBudget["usage"])
	require.Equal(t, orgReached, denial(secondUUID))

	requestJSONWithHeaders(t, handler, http.MethodDelete, first.ProjectPath, "", "", http.StatusNoContent, admin)
	require.Equal(t, orgReached, denial(secondUUID), "a deleted project's spend still counts toward the org budget")
}

func TestBudgetsForProfilesAndAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "budget-profile-agent")
	admin := authHeaders(project.AdminToken)
	budgetsPath := "/api/v1/orgs/" + project.OrgID + "/budgets"
	agent := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID,
		"budget-scoped").Agent
	usage := modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5}
	recordHTTPModelUsageForAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID,
		agent, usage, "2")
	encode := func(kind publicid.Kind, id uuid.UUID) string {
		value, err := publicid.Encode(kind, id)
		require.NoError(t, err)
		return value
	}
	profileID, agentID := encode(publicid.KindAgentProfile, agent.AgentProfileID), encode(publicid.KindAgent, agent.ID)
	create := func(scope map[string]any, interval string, status int) map[string]any {
		return requestJSONWithHeaders(t, handler, http.MethodPost, budgetsPath,
			integrationHTTPJSON(t, map[string]any{"scope": scope, "interval": interval, "amount_usd": "2"}),
			"", status, admin)
	}
	get := func(id any, status int) map[string]any {
		return requestJSONWithHeaders(t, handler, http.MethodGet,
			budgetsPath+"/"+testutil.RequireType[string](t, id), "", "", status, admin)
	}
	list := func(query string) []any {
		page := requestJSONWithHeaders(t, handler, http.MethodGet, budgetsPath+"?"+query, "", "", http.StatusOK, admin)
		return testutil.RequireType[[]any](t, page["data"])
	}

	profileScope := map[string]any{"type": "profile", "project_id": project.ProjectID, "agent_profile_id": profileID}
	agentScope := map[string]any{"type": "agent", "project_id": project.ProjectID, "agent_id": agentID}
	profileBudget := create(profileScope, "month", http.StatusCreated)
	require.Equal(t, profileScope, profileBudget["scope"])
	agentBudget := create(agentScope, "lifetime", http.StatusCreated)
	require.Equal(t, agentScope, agentBudget["scope"])
	create(agentScope, "lifetime", http.StatusConflict)
	require.Equal(t, map[string]any{"amount_usd": "2"}, get(profileBudget["id"], http.StatusOK)["usage"])
	require.Equal(t, map[string]any{"amount_usd": "2"}, get(agentBudget["id"], http.StatusOK)["usage"])

	require.Len(t, list("scope_type=profile&project_id="+project.ProjectID+"&agent_profile_id="+profileID), 1)
	require.Len(t, list("scope_type=agent&project_id="+project.ProjectID+"&agent_id="+agentID), 1)
	require.Empty(t, list("scope_type=project&project_id="+project.ProjectID))
	requestJSONWithHeaders(t, handler, http.MethodGet, budgetsPath+"?agent_id="+agentID, "", "",
		http.StatusBadRequest, admin)

	sibling, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: project.ProjectUUID, ProfileID: agent.AgentProfileID, AgentConfigID: agent.CurrentConfigID,
		LaunchedBy: httpUserPrincipal(project.AdminUserUUID), IdempotencyKey: "budget-sibling",
	})
	require.NoError(t, err)
	_, claim := claimHTTPModelCallForAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, sibling.Agent)
	require.False(t, claim.Claimed)
	require.Equal(t, storeerr.BudgetReachedMessage("profile", "month"), claim.Context.ErrorMessage,
		"another agent from the profile hits the profile budget, not the first agent's own budget")

	create(map[string]any{
		"type": "profile", "project_id": project.ProjectID,
		"agent_profile_id": encode(publicid.KindAgentProfile, uuid.New()),
	}, "month", http.StatusNotFound)
	child := uuid.New()
	_, err = pool.Exec(ctx, `
INSERT INTO agents(id, org_id, project_id, state, current_config_id, parent_agent_id, root_agent_id, subagent_key,
                   created_at, updated_at)
SELECT $1, org_id, project_id, state, current_config_id, id, id, 'child', now(), now() FROM agents WHERE id = $2`,
		child, agent.ID)
	require.NoError(t, err)
	create(map[string]any{
		"type": "agent", "project_id": project.ProjectID, "agent_id": encode(publicid.KindAgent, child),
	}, "month", http.StatusBadRequest)

	profilePath := project.ProjectPath + "/agent-profiles/" + profileID
	requestJSONWithHeaders(t, handler, http.MethodDelete, profilePath, "", "", http.StatusNoContent, admin)
	get(profileBudget["id"], http.StatusNotFound)
	get(agentBudget["id"], http.StatusOK)
}

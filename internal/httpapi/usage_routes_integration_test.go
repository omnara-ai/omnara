//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestUsageRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "usage-routes")

	parentLaunch := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "usage-parent",
	)
	parent := parentLaunch.Agent
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, parent,
		modelenvelope.Usage{
			InputTokens: 100, UncachedInputTokens: 60, CacheReadTokens: 30, CacheWriteTokens: 10,
			OutputTokens: 20, ReasoningTokens: 5,
		},
		"0.0125",
	)
	child := spawnHTTPSubagentForTest(t, ctx, store, parent, parentLaunch.AgentConfig.ID, "usage-child", "worker")
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, child,
		modelenvelope.Usage{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 10},
		"",
	)
	grandchild := spawnHTTPSubagentForTest(t, ctx, store, child, parentLaunch.AgentConfig.ID, "usage-grandchild", "helper")
	recordHTTPModelUsageForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, grandchild,
		modelenvelope.Usage{InputTokens: 7, UncachedInputTokens: 7, OutputTokens: 3},
		"0.0005",
	)

	parentOnly := expectedUsage{
		modelCalls: 1, withCost: 1, cost: "0.0125",
		input: 100, uncached: 60, cacheRead: 30, cacheWrite: 10, output: 20, reasoning: 5,
	}
	wholeTree := expectedUsage{
		modelCalls: 3, withCost: 2, cost: "0.013",
		input: 157, uncached: 117, cacheRead: 30, cacheWrite: 10, output: 33, reasoning: 5,
	}
	parentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, parent.ID)
	profilePath := project.ProjectPath + "/agent-profiles/" +
		testPublicID(t, publicid.KindAgentProfile, parent.AgentProfileID)

	get := func(path string) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(
			t, handler, http.MethodGet, path, "", "", http.StatusOK, authHeaders(project.AdminToken),
		)
	}
	assertUsageReport(t, get(parentPath+"/usage"), parentOnly)
	assertUsageReport(t, get(parentPath+"/usage?include_subagents=false"), parentOnly)
	assertUsageReport(t, get(parentPath+"/usage?include_subagents=true"), wholeTree)
	assertUsageReport(t, get(profilePath+"/usage"), parentOnly)
	assertUsageReport(t, get(profilePath+"/usage?include_subagents=false"), parentOnly)
	assertUsageReport(t, get(profilePath+"/usage?include_subagents=true"), wholeTree)
	assertUsageReport(t, get(project.ProjectPath+"/usage"), wholeTree)
	orgUsagePath := "/api/v1/orgs/" + project.OrgID + "/usage"
	assertUsageReport(t, get(orgUsagePath), wholeTree)

	nothing := expectedUsage{cost: "0"}
	window := "since=2000-01-01T00:00:00Z&until=2100-01-01T00:00:00Z"

	// Profile lists carry each profile's stats on request, matching its usage
	// report with subagents.
	profileID := testPublicID(t, publicid.KindAgentProfile, parent.AgentProfileID)
	profileStats := func(path string) map[string]any {
		t.Helper()
		for _, raw := range testutil.RequireType[[]any](t, get(path)["data"]) {
			row := testutil.RequireType[map[string]any](t, raw)
			if row["id"] != profileID {
				continue
			}
			stats, ok := row["stats"]
			if !ok {
				return nil
			}
			return testutil.RequireType[map[string]any](t, stats)
		}
		t.Fatalf("%s did not list profile %s", path, profileID)
		return nil
	}
	if stats := profileStats(project.ProjectPath + "/agent-profiles"); stats != nil {
		t.Fatalf("stats without include_stats = %+v, want none", stats)
	}
	for _, path := range []string{
		project.ProjectPath + "/agent-profiles?include_stats=true",
		"/api/v1/orgs/" + project.OrgID + "/agent-profiles?include_stats=true&stats_since=2000-01-01T00:00:00Z",
	} {
		stats := profileStats(path)
		if stats["agent_count"] != float64(1) {
			t.Fatalf("%s agent_count = %v, want 1", path, stats["agent_count"])
		}
		assertUsageTotals(t, path, testutil.RequireType[map[string]any](t, stats["usage"]), wholeTree)
	}
	future := profileStats(project.ProjectPath + "/agent-profiles?include_stats=true&stats_since=2100-01-01T00:00:00Z")
	assertUsageTotals(t, "future stats", testutil.RequireType[map[string]any](t, future["usage"]), nothing)

	// Agent lists carry each agent's usage with its subagents on request.
	agentUsage := func(path string) map[string]map[string]any {
		t.Helper()
		usage := map[string]map[string]any{}
		for _, raw := range testutil.RequireType[[]any](t, get(path)["data"]) {
			row := testutil.RequireType[map[string]any](t, raw)
			if totals, ok := row["usage"]; ok {
				usage[testutil.RequireType[string](t, row["id"])] = testutil.RequireType[map[string]any](t, totals)
			}
		}
		return usage
	}
	if usage := agentUsage(project.ProjectPath + "/agents?include_subagents=true"); len(usage) != 0 {
		t.Fatalf("usage without include_usage = %+v, want none", usage)
	}
	parentID := testPublicID(t, publicid.KindAgent, parent.ID)
	childID := testPublicID(t, publicid.KindAgent, child.ID)
	childTree := expectedUsage{
		modelCalls: 2, withCost: 1, cost: "0.0005", input: 57, uncached: 57, output: 13,
	}
	for _, path := range []string{
		project.ProjectPath + "/agents?include_subagents=true&include_usage=true",
		"/api/v1/orgs/" + project.OrgID + "/agents?include_subagents=true&include_usage=true",
	} {
		usage := agentUsage(path)
		assertUsageTotals(t, path+" parent", usage[parentID], wholeTree)
		assertUsageTotals(t, path+" child", usage[childID], childTree)
	}
	for _, scope := range []struct {
		path string
		want expectedUsage
	}{
		{parentPath + "/usage", parentOnly},
		{profilePath + "/usage", parentOnly},
		{project.ProjectPath + "/usage", wholeTree},
		{orgUsagePath, wholeTree},
	} {
		path := scope.path
		assertUsageReport(t, get(path+"?"+window), scope.want)
		assertUsageReport(t, get(path+"?since=2100-01-01T00:00:00Z"), nothing)
		assertUsageReport(t, get(path+"?until=2000-01-01T00:00:00Z"), nothing)
		requestJSONWithHeaders(
			t, handler, http.MethodGet, path+"?since=2100-01-01T00:00:00Z&until=2000-01-01T00:00:00Z", "", "",
			http.StatusBadRequest, authHeaders(project.AdminToken),
		)
	}

	fresh := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Usage Empty"}`, "idem-usage-empty-project", http.StatusCreated, authHeaders(project.AdminToken),
	)
	emptyProjectID := testutil.RequireType[string](t, fresh["id"])
	emptyPath := "/api/v1/orgs/" + project.OrgID + "/projects/" + emptyProjectID + "/usage"
	assertUsageReport(t, get(emptyPath), nothing)

	assertUsageReport(t, get(orgUsagePath+"?include_project_ids="+project.ProjectID), wholeTree)
	assertUsageReport(t, get(orgUsagePath+"?include_project_ids="+emptyProjectID), nothing)
	assertUsageReport(
		t, get(orgUsagePath+"?include_project_ids="+project.ProjectID+"&include_project_ids="+emptyProjectID), wholeTree,
	)
	assertUsageReport(t, get(orgUsagePath+"?exclude_project_ids="+project.ProjectID), nothing)
	assertUsageReport(t, get(orgUsagePath+"?exclude_project_ids="+emptyProjectID), wholeTree)
	for _, query := range []string{
		"include_project_ids=" + project.ProjectID + "&exclude_project_ids=" + emptyProjectID,
		"include_project_ids=not-a-project",
		"exclude_project_ids=not-a-project",
	} {
		requestJSONWithHeaders(
			t, handler, http.MethodGet, orgUsagePath+"?"+query, "", "",
			http.StatusBadRequest, authHeaders(project.AdminToken),
		)
	}

	// Members see org usage only from the projects they can read.
	memberToken := func(email string, projectID uuid.UUID) string {
		t.Helper()
		user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
			Email: email, DisplayName: email,
		})
		if err != nil {
			t.Fatalf("create %s: %v", email, err)
		}
		if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
			OrgID: project.OrgUUID, UserID: user.ID, Role: "member",
		}); err != nil {
			t.Fatalf("add %s org membership: %v", email, err)
		}
		if projectID != uuid.Nil {
			if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
				OrgID: project.OrgUUID, ProjectID: projectID, UserID: user.ID, Role: "viewer",
			}); err != nil {
				t.Fatalf("add %s project membership: %v", email, err)
			}
		}
		pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
			ctx, identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: email},
		)
		if err != nil {
			t.Fatalf("create %s token: %v", email, err)
		}
		return pat.Token
	}
	getAs := func(token, path string, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", status, authHeaders(token))
	}
	viewer := memberToken("usage-viewer@example.com", project.ProjectUUID)
	assertUsageReport(t, getAs(viewer, orgUsagePath, http.StatusOK), wholeTree)
	emptyViewer := memberToken("usage-empty-viewer@example.com", mustPublicHTTPID(t, publicid.KindProject, emptyProjectID))
	assertUsageReport(t, getAs(emptyViewer, orgUsagePath, http.StatusOK), nothing)
	getAs(emptyViewer, orgUsagePath+"?include_project_ids="+project.ProjectID, http.StatusNotFound)
	noProjects := memberToken("usage-no-projects@example.com", uuid.Nil)
	assertUsageReport(t, getAs(noProjects, orgUsagePath, http.StatusOK), nothing)

	missingProfilePath := project.ProjectPath + "/agent-profiles/" +
		testPublicID(t, publicid.KindAgentProfile, httpTestID("usage-missing-profile")) + "/usage"
	requestJSONWithHeaders(
		t, handler, http.MethodGet, missingProfilePath, "", "", http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	// A deleted project's past usage still counts for org admins, who see the
	// whole org, but no longer for members who could read it.
	if _, err := store.Organizations().DeleteProject(
		ctx, project.OrgUUID, project.ProjectUUID, httpUserPrincipal(project.AdminUserUUID),
	); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	assertUsageReport(t, get(orgUsagePath), wholeTree)
	assertUsageReport(t, get(orgUsagePath+"?exclude_project_ids="+emptyProjectID), wholeTree)
	assertUsageReport(t, getAs(viewer, orgUsagePath, http.StatusOK), nothing)
}

type expectedUsage struct {
	modelCalls, withCost                                      int
	cost                                                      string
	input, uncached, cacheRead, cacheWrite, output, reasoning int
}

func assertUsageReport(t *testing.T, report map[string]any, want expectedUsage) {
	t.Helper()
	totals := testutil.RequireType[map[string]any](t, report["totals"])
	assertUsageTotals(t, "totals", totals, want)
	byModel := testutil.RequireType[[]any](t, report["by_model"])
	if want.modelCalls == 0 {
		if len(byModel) != 0 {
			t.Fatalf("by_model = %+v, want empty", byModel)
		}
		return
	}
	if len(byModel) != 1 {
		t.Fatalf("by_model = %+v, want one model", byModel)
	}
	row := testutil.RequireType[map[string]any](t, byModel[0])
	assertUsageTotals(t, "by_model[0]", row, want)
	modelInfo := testutil.RequireType[map[string]any](t, row["model"])
	if modelInfo["name"] != "http-test" || modelInfo["provider_model_slug"] != "http-test" ||
		modelInfo["model_provider_config_name"] != "openai-prod" {
		t.Fatalf("by_model[0].model = %+v", modelInfo)
	}
	configuredModelID := testutil.RequireType[string](t, modelInfo["configured_model_id"])
	if _, err := publicid.Decode(publicid.KindConfiguredModel, configuredModelID); err != nil {
		t.Fatalf("configured_model_id: %v", err)
	}
	providerConfigID := testutil.RequireType[string](t, modelInfo["model_provider_config_id"])
	if _, err := publicid.Decode(publicid.KindModelProviderConfig, providerConfigID); err != nil {
		t.Fatalf("model_provider_config_id: %v", err)
	}
}

func assertUsageTotals(t *testing.T, label string, row map[string]any, want expectedUsage) {
	t.Helper()
	if calls := testutil.RequireType[float64](t, row["model_calls"]); int(calls) != want.modelCalls {
		t.Fatalf("%s.model_calls = %v, want %d", label, calls, want.modelCalls)
	}
	tokens := testutil.RequireType[map[string]any](t, row["tokens"])
	wantTokens := map[string]int{
		"input_tokens_total":       want.input,
		"uncached_input_tokens":    want.uncached,
		"cache_read_input_tokens":  want.cacheRead,
		"cache_write_input_tokens": want.cacheWrite,
		"output_tokens_total":      want.output,
		"reasoning_output_tokens":  want.reasoning,
	}
	for field, wantValue := range wantTokens {
		if got := testutil.RequireType[float64](t, tokens[field]); int(got) != wantValue {
			t.Fatalf("%s.tokens.%s = %v, want %d", label, field, got, wantValue)
		}
	}
	cost := testutil.RequireType[map[string]any](t, row["cost"])
	if cost["provider_reported_usd"] != want.cost {
		t.Fatalf("%s.cost.provider_reported_usd = %v, want %s", label, cost["provider_reported_usd"], want.cost)
	}
	withCost := testutil.RequireType[float64](t, cost["model_calls_with_reported_cost"])
	if int(withCost) != want.withCost {
		t.Fatalf("%s.cost.model_calls_with_reported_cost = %v, want %d", label, withCost, want.withCost)
	}
}

func recordHTTPModelUsageForAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, userID uuid.UUID,
	agent executionstore.AgentRecord,
	usage modelenvelope.Usage,
	cost modelenvelope.ProviderReportedCostUSD,
) {
	t.Helper()
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      projectID,
			AgentID:        agent.ID,
			Actor:          httpOmnaraActorParams(t, orgID, userID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"tally me"}]`),
			IdempotencyKey: "usage-msg-" + agent.ID.String(),
		},
	)
	if err != nil {
		t.Fatalf("create usage input: %v", err)
	}
	var claim executionstore.ClaimedAgentWork
	for attempt := 0; ; attempt++ {
		var found bool
		claim, found, err = store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
		if err != nil {
			t.Fatalf("claim usage input: %v", err)
		}
		if found && claim.Kind == executionstore.AgentWorkModel && len(claim.Model.AdmittedInputTurn.Inputs) == 1 &&
			claim.Model.AdmittedInputTurn.Inputs[0].ID == input.ID {
			break
		}
		if !found || attempt >= 4 {
			t.Fatalf("claim usage input found=%v kind=%v want input %s", found, claim.Kind, input.ID)
		}
	}
	runtime := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx, projectID, agent.ID, admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	modelCall := claimNormalModelCallForHTTPTest(
		t, ctx, store, projectID, agent.ID, runtime, []uuid.UUID{input.ID},
		snapshot.AgentConfig.ID, admitted.Events[0].Sequence,
	)
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"http-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:                      "resp_usage_" + agent.ID.String(),
			StopReason:              model.StopReasonEndTurn,
			Content:                 []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "done"}},
			Usage:                   usage,
			ProviderReportedCostUSD: cost,
		},
	)
	if err != nil {
		t.Fatalf("build usage provider response: %v", err)
	}
	if _, err := store.Execution().RecordModelOutputAndCompleteContext(
		ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID:          projectID,
			AgentID:            agent.ID,
			RuntimeLockID:      runtime.ID,
			ModelCallContextID: modelCall.Context.ID,
			ProviderResponse:   providerResponse,
		},
	); err != nil {
		t.Fatalf("record usage model output: %v", err)
	}
}

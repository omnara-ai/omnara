//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil"
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
	assertUsageReport(t, get(project.ProjectPath+"/usage"), wholeTree)
	assertUsageReport(t, get("/api/v1/orgs/"+project.OrgID+"/usage"), wholeTree)

	fresh := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Usage Empty"}`, "idem-usage-empty-project", http.StatusCreated, authHeaders(project.AdminToken),
	)
	emptyPath := "/api/v1/orgs/" + project.OrgID + "/projects/" + testutil.RequireType[string](t, fresh["id"]) + "/usage"
	assertUsageReport(t, get(emptyPath), expectedUsage{cost: "0"})

	missingProfilePath := project.ProjectPath + "/agent-profiles/" +
		testPublicID(t, publicid.KindAgentProfile, httpTestID("usage-missing-profile")) + "/usage"
	requestJSONWithHeaders(
		t, handler, http.MethodGet, missingProfilePath, "", "", http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
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
	orgID, projectID, userID storage.ID,
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
		t, ctx, store, projectID, agent.ID, runtime, []storage.ID{input.ID},
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

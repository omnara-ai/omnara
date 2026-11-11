//go:build integration

package kernel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprice"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type fixedCostEstimator struct {
	mu     sync.Mutex
	cost   modelenvelope.ProviderReportedCostUSD
	inputs []modelprice.Input
}

func (e *fixedCostEstimator) Estimate(_ context.Context, input modelprice.Input) modelenvelope.ProviderReportedCostUSD {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inputs = append(e.inputs, input)
	return e.cost
}

func budgetResponse(id string, cost modelenvelope.ProviderReportedCostUSD) model.Response {
	return model.Response{
		ID:                      id,
		ServedProviderModelSlug: "budget-model-served",
		Content:                 []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: id}},
		StopReason:              model.StopReasonEndTurn,
		ProviderReportedCostUSD: cost,
		Usage:                   model.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5},
	}
}

type budgetTurn struct {
	turnID    uuid.UUID
	notices   int
	responded int
}

func runBudgetTurn(
	t *testing.T,
	ctx context.Context,
	f kernelFixture,
	agentID, userID uuid.UUID,
	text string,
	at time.Time,
	response model.Response,
	afterRespond func(model.Response),
) budgetTurn {
	t.Helper()
	turn := f.admitContentInputTurn(t, ctx, agentID, userID, text, at)
	client := &sequenceKernelModel{
		providerModelSlug: "budget-model", responses: []model.Response{response}, afterRespond: afterRespond,
	}
	result := budgetTurn{turnID: turn.TurnID}
	executor := AgentExecutor{
		Store:         f.Store,
		ModelResolver: liveTestModelResolver(f.Store, client),
		ToolExecutor:  tools.Executor{Store: f.Store},
		Now:           func() time.Time { return at.Add(time.Second) },
		OnModelFailure: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			result.notices++
			return nil
		},
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, turn))
	require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, kernelTestProjectID, agentID, turn.RuntimeLockID))
	result.responded = client.respondedCount()
	return result
}

func modelCallErrorForKernelTest(
	t *testing.T,
	ctx context.Context,
	f kernelFixture,
	turnID uuid.UUID,
) (string, string) {
	t.Helper()
	var code, message string
	require.NoError(t, f.Pool.QueryRow(ctx, `
SELECT coalesce(error_code, ''), coalesce(error_message, '')
FROM model_call_contexts
WHERE project_id = $1 AND turn_id = $2
ORDER BY created_at DESC, attempt_number DESC
LIMIT 1`, kernelTestProjectID, turnID).Scan(&code, &message))
	return code, message
}

func moveStartedModelCallForKernelTest(t *testing.T, ctx context.Context, f kernelFixture, createdAt string) {
	t.Helper()
	_, err := f.Pool.Exec(ctx, `ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard`)
	require.NoError(t, err)
	_, err = f.Pool.Exec(ctx, `UPDATE model_call_contexts SET created_at = `+createdAt+`
WHERE project_id = $1 AND state = 'started'`, kernelTestProjectID)
	require.NoError(t, err)
	_, err = f.Pool.Exec(ctx, `ALTER TABLE model_call_contexts ENABLE TRIGGER model_call_contexts_transition_guard`)
	require.NoError(t, err)
}

type kernelTestBudget struct {
	project  bool
	interval string
	amount   string
}

func createBudgetForKernelTest(t *testing.T, ctx context.Context, f kernelFixture, budget kernelTestBudget) {
	t.Helper()
	input := identitystore.CreateBudgetInput{OrgID: kernelTestOrgID, Interval: budget.interval, AmountUSD: budget.amount}
	if budget.project {
		projectID := kernelTestProjectID
		input.ProjectID = &projectID
	}
	_, err := f.Store.Identity().CreateBudget(ctx, input)
	require.NoError(t, err)
}

func TestAgentExecutorBudgetDenialNotifiesWithoutProviderRequest(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	first := runBudgetTurn(t, ctx, f, agentID, userID, "spend", f.Now.Add(time.Second),
		budgetResponse("resp_spend", "2"), nil)
	require.Equal(t, 1, first.responded)
	require.Zero(t, first.notices)

	createBudgetForKernelTest(t, ctx, f, kernelTestBudget{project: true, interval: "month", amount: "2"})
	denied := runBudgetTurn(t, ctx, f, agentID, userID, "denied", f.Now.Add(time.Minute),
		budgetResponse("resp_denied", ""), nil)
	require.Zero(t, denied.responded, "a call over budget must not reach the provider")
	require.Equal(t, 1, denied.notices)
	assertDurableModelErrorForKernelTest(t, ctx, f, agentID, denied.turnID,
		string(modelprotocol.ErrorKindRuntime), storeerr.BudgetReachedCode)
}

func TestAgentExecutorBudgetScopes(t *testing.T) {
	t.Parallel()
	projectMonthly := storeerr.BudgetReachedMessage(true, "month")
	orgMonthly := storeerr.BudgetReachedMessage(false, "month")
	tests := []struct {
		name        string
		spend       modelenvelope.ProviderReportedCostUSD
		estimate    modelenvelope.ProviderReportedCostUSD
		createdAt   string
		newYork     bool
		budgets     []kernelTestBudget
		deleteFirst bool
		wantMessage string
	}{
		{name: "under budget runs", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "month", amount: "2.01"}, {interval: "month", amount: "100"},
		}},
		{name: "project budget reached", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "month", amount: "2"},
		}, wantMessage: projectMonthly},
		{name: "org budget reached", spend: "2", budgets: []kernelTestBudget{
			{interval: "month", amount: "1.5"},
		}, wantMessage: orgMonthly},
		{name: "tighter project budget wins", spend: "2", budgets: []kernelTestBudget{
			{interval: "month", amount: "100"}, {project: true, interval: "month", amount: "1"},
		}, wantMessage: projectMonthly},
		{name: "org budget is named before project", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "month", amount: "1"}, {interval: "month", amount: "1"},
		}, wantMessage: orgMonthly},
		{name: "longest interval is named first", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "day", amount: "1"}, {project: true, interval: "lifetime", amount: "1"},
			{project: true, interval: "week", amount: "1"},
		}, wantMessage: storeerr.BudgetReachedMessage(true, "lifetime")},
		{name: "daily budget reached", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "day", amount: "2"},
		}, wantMessage: storeerr.BudgetReachedMessage(true, "day")},
		{name: "weekly budget reached", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "week", amount: "2"},
		}, wantMessage: storeerr.BudgetReachedMessage(true, "week")},
		{name: "estimated cost counts", estimate: "2", budgets: []kernelTestBudget{
			{project: true, interval: "month", amount: "2"},
		}, wantMessage: projectMonthly},
		{
			name: "spend from yesterday does not count toward the day", spend: "2",
			createdAt: `date_trunc('day', statement_timestamp(), 'UTC') - interval '1 microsecond'`,
			budgets:   []kernelTestBudget{{project: true, interval: "day", amount: "2"}},
		},
		{
			name: "days follow UTC whatever the database time zone", spend: "2", newYork: true,
			createdAt:   `date_trunc('day', statement_timestamp(), 'UTC') + interval '1 microsecond'`,
			budgets:     []kernelTestBudget{{project: true, interval: "day", amount: "2"}},
			wantMessage: storeerr.BudgetReachedMessage(true, "day"),
		},
		{
			name: "spend from before Monday does not count toward the week", spend: "2",
			createdAt: `date_trunc('week', statement_timestamp(), 'UTC') - interval '1 microsecond'`,
			budgets:   []kernelTestBudget{{project: true, interval: "week", amount: "2"}},
		},
		{
			name: "weeks start Monday in UTC whatever the database time zone", spend: "2", newYork: true,
			createdAt:   `date_trunc('week', statement_timestamp(), 'UTC') + interval '1 microsecond'`,
			budgets:     []kernelTestBudget{{project: true, interval: "week", amount: "2"}},
			wantMessage: storeerr.BudgetReachedMessage(true, "week"),
		},
		{
			name: "spend from earlier months does not count toward the month", spend: "2",
			createdAt: `date_trunc('month', statement_timestamp(), 'UTC') - interval '1 microsecond'`,
			budgets:   []kernelTestBudget{{project: true, interval: "month", amount: "2"}},
		},
		{
			name: "spend from earlier months counts toward lifetime", spend: "2",
			createdAt:   `date_trunc('month', statement_timestamp(), 'UTC') - interval '1 microsecond'`,
			budgets:     []kernelTestBudget{{project: true, interval: "lifetime", amount: "2"}},
			wantMessage: storeerr.BudgetReachedMessage(true, "lifetime"),
		},
		{
			name: "months follow UTC whatever the database time zone", spend: "2", newYork: true,
			createdAt:   `date_trunc('month', statement_timestamp(), 'UTC') + interval '1 microsecond'`,
			budgets:     []kernelTestBudget{{project: true, interval: "month", amount: "2"}},
			wantMessage: projectMonthly,
		},
		{
			name: "deleted project spend still counts toward the org", spend: "2", deleteFirst: true,
			budgets: []kernelTestBudget{{interval: "month", amount: "2"}}, wantMessage: orgMonthly,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			estimator := &fixedCostEstimator{cost: test.estimate}
			f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(estimator))
			if test.newYork {
				var database string
				require.NoError(t, f.Pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database))
				_, err := f.Pool.Exec(ctx, `ALTER DATABASE "`+database+`" SET timezone = 'America/New_York'`)
				require.NoError(t, err)
				f.Pool.Reset()
				var zone string
				require.NoError(t, f.Pool.QueryRow(ctx, `SHOW timezone`).Scan(&zone))
				require.Equal(t, "America/New_York", zone)
			}
			agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
			var move func(model.Response)
			if test.createdAt != "" {
				move = func(model.Response) { moveStartedModelCallForKernelTest(t, ctx, f, test.createdAt) }
			}
			runBudgetTurn(t, ctx, f, agentID, userID, "spend", f.Now.Add(time.Second),
				budgetResponse("resp_spend", test.spend), move)
			for _, budget := range test.budgets {
				createBudgetForKernelTest(t, ctx, f, budget)
			}
			if test.deleteFirst {
				_, err := f.Pool.Exec(ctx, `UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1`,
					kernelTestProjectID)
				require.NoError(t, err)
			}
			next := runBudgetTurn(t, ctx, f, agentID, userID, "next", f.Now.Add(time.Minute),
				budgetResponse("resp_next", "0"), nil)
			code, message := modelCallErrorForKernelTest(t, ctx, f, next.turnID)
			if test.wantMessage == "" {
				require.Equal(t, 1, next.responded)
				require.Empty(t, code)
				return
			}
			require.Zero(t, next.responded)
			require.Equal(t, storeerr.BudgetReachedCode, code)
			require.Equal(t, test.wantMessage, message)
		})
	}
}

func TestAgentExecutorRecordsEstimatesOnlyWithoutReportedCost(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	estimator := &fixedCostEstimator{cost: "0.25"}
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(estimator))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	runBudgetTurn(t, ctx, f, agentID, userID, "estimated", f.Now.Add(time.Second),
		budgetResponse("resp_estimated", ""), nil)
	runBudgetTurn(t, ctx, f, agentID, userID, "reported", f.Now.Add(time.Minute),
		budgetResponse("resp_reported", "1"), nil)

	rows, err := f.Pool.Query(ctx, `
SELECT provider_response_id, coalesce(provider_reported_cost_usd::text, ''), coalesce(estimated_cost_usd::text, '')
FROM model_call_contexts WHERE project_id = $1 ORDER BY created_at`, kernelTestProjectID)
	require.NoError(t, err)
	defer rows.Close()
	type costs struct{ response, reported, estimated string }
	var got []costs
	for rows.Next() {
		var row costs
		require.NoError(t, rows.Scan(&row.response, &row.reported, &row.estimated))
		got = append(got, row)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []costs{
		{response: "resp_estimated", estimated: "0.250000000000000"},
		{response: "resp_reported", reported: "1.000000000000000"},
	}, got)

	require.Len(t, estimator.inputs, 1)
	input := estimator.inputs[0]
	require.Equal(t, "budget-model-served", input.ServedModel)
	require.Equal(t, "budget-model", input.ConfiguredModel)
	require.Equal(t, "https://api.openai.com/v1", input.BaseURL)
	require.False(t, input.CreatedAt.IsZero())
	require.Equal(t, 10, input.Usage.InputTokens)
}

func servedModelsForKernelTest(t *testing.T, estimator *fixedCostEstimator) []string {
	t.Helper()
	estimator.mu.Lock()
	defer estimator.mu.Unlock()
	served := make([]string, 0, len(estimator.inputs))
	for _, input := range estimator.inputs {
		require.Equal(t, "https://api.openai.com/v1", input.BaseURL, input.ServedModel)
		served = append(served, input.ServedModel)
	}
	return served
}

func TestAgentExecutorEstimatesFromTheResolvedProviderURL(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	estimator := &fixedCostEstimator{cost: "0.25"}
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(estimator))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	runBudgetTurn(t, ctx, f, agentID, userID, "estimated", f.Now.Add(time.Second),
		budgetResponse("resp_estimated", ""), func(model.Response) {
			_, err := f.Pool.Exec(ctx, `UPDATE model_provider_configs SET base_url = 'https://mirror.example.com/v1'
WHERE org_id = $1`, kernelTestOrgID)
			require.NoError(t, err)
		})
	require.Equal(t, []string{"budget-model-served"}, servedModelsForKernelTest(t, estimator))
}

func TestAgentExecutorPassesServedModelOnRecoverableFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	estimator := &fixedCostEstimator{cost: "0.1"}
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(estimator))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	turn := f.admitContentInputTurn(t, ctx, agentID, userID, "retry", f.Now.Add(time.Millisecond))
	client := &sequenceKernelModel{
		providerModelSlug: "budget-model",
		errs:              []error{model.ProviderError{Kind: model.ErrorKindTransient, Source: "test", Message: "retry"}},
		errorResponses: []model.Response{{
			ID: "resp_retry", ServedProviderModelSlug: "served-retry",
			Usage: model.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5},
		}},
		responses: []model.Response{budgetResponse("resp_done", "")},
	}
	executor := AgentExecutor{
		Store:           f.Store,
		ModelResolver:   liveTestModelResolver(f.Store, client),
		ToolExecutor:    tools.Executor{Store: f.Store},
		Now:             func() time.Time { return f.Now.Add(time.Second) },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, turn))
	next := continueTurnOnNewLeaseForKernelTest(t, ctx, f, turn, f.Now.Add(time.Minute))
	require.NoError(t, executor.ExecuteModelWork(ctx, next))
	require.Equal(t, []string{"served-retry", "budget-model-served"}, servedModelsForKernelTest(t, estimator))
}

func TestAgentExecutorPassesServedModelThroughCompaction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	estimator := &fixedCostEstimator{cost: "0.1"}
	fixture, agentID, userID := seedProgressiveHistory(t, ctx, []progressiveSeedTurn{
		{input: "SEED_ONE " + strings.Repeat("durable seed detail ", 120), output: "first seed accepted"},
		{input: "SEED_TWO " + strings.Repeat("second durable seed detail ", 120), output: "second seed accepted"},
	}, storage.WithModelCostEstimator(estimator))
	usage := model.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5}
	summary := func(id, served, text string, stop model.StopReason) model.Response {
		return model.Response{
			ID: id, ServedProviderModelSlug: served, Usage: usage, StopReason: stop,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: text}},
		}
	}
	client := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) {
				return 500
			}
			if bundle.ContextCheckpoint != nil &&
				(strings.Contains(bundle.ContextCheckpoint.Summary, "FINAL_SERVED_CHECKPOINT") ||
					strings.Contains(bundle.ContextCheckpoint.Summary, "[Earlier conversation compacted.]") ||
					strings.Contains(bundle.ContextCheckpoint.Summary, "[Additional closed history compacted.]")) {
				return 500
			}
			return 200_000
		},
		capabilities: model.Capabilities{ContextWindowTokens: 128000, MaxOutputTokens: new(256)},
		responses: []model.Response{
			summary("resp_truncated", "served-truncated", "truncated summary", model.StopReasonMaxTokens),
			summary("resp_intermediate", "served-intermediate",
				"INTERMEDIATE_SERVED_CHECKPOINT Preserve the earlier seed request.", model.StopReasonEndTurn),
			summary("resp_final", "served-final",
				"FINAL_SERVED_CHECKPOINT Continue with the current request.", model.StopReasonEndTurn),
			summary("resp_continuation", "served-continuation", "continued", model.StopReasonEndTurn),
		},
	}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID,
		"CURRENT_SERVED_REQUEST "+strings.Repeat("current request detail ", 80), fixture.Now.Add(5*time.Second))
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, client),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(6 * time.Second) },
	}
	for lease := range 3 {
		if lease > 0 {
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
				fixture.Now.Add(time.Duration(6+lease)*time.Second))
		}
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
	}
	require.Equal(t, 4, client.respondedCount())
	require.Equal(t,
		[]string{"served-truncated", "served-intermediate", "served-final", "served-continuation"},
		servedModelsForKernelTest(t, estimator))
}

func TestAgentExecutorBudgetStopsCompactionAfterCrossingCall(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now, "run_command")
	runBudgetTurn(t, ctx, f, agentID, userID, "seed history before overflow", f.Now.Add(time.Second),
		budgetResponse("resp_seed", "0"), nil)
	createBudgetForKernelTest(t, ctx, f, kernelTestBudget{project: true, interval: "month", amount: "1"})

	overflow := budgetResponse("resp_context_window", "1")
	overflow.StopReason = model.StopReasonContextWindow
	client := &sequenceKernelModel{
		providerModelSlug: "budget-model",
		capabilities:      model.Capabilities{ContextWindowTokens: 128000, MaxOutputTokens: new(128)},
		responses:         []model.Response{overflow, budgetResponse("resp_summary", "0")},
	}
	turn := f.admitContentInputTurn(t, ctx, agentID, userID, "trigger compaction", f.Now.Add(time.Minute))
	notices := 0
	executor := AgentExecutor{
		Store:         f.Store,
		ModelResolver: liveTestModelResolver(f.Store, client),
		ToolExecutor:  tools.Executor{Store: f.Store},
		Now:           func() time.Time { return f.Now.Add(2 * time.Minute) },
		OnModelFailure: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			notices++
			return nil
		},
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, turn))
	require.Equal(t, 1, notices)
	require.Equal(t, 1, client.respondedCount(), "the compaction call must not reach the provider")
	var compactParents, deniedCompactions int
	require.NoError(t, f.Pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE operation_kind = 'normal' AND state = 'failed' AND recovery_kind = 'compact'),
       count(*) FILTER (WHERE operation_kind = 'compaction' AND state = 'failed' AND error_code = $3)
FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2`,
		kernelTestProjectID, agentID, storeerr.BudgetReachedCode).Scan(&compactParents, &deniedCompactions))
	require.Equal(t, 1, compactParents)
	require.Equal(t, 1, deniedCompactions)
}

func projectDailyModelCostForKernelTest(t *testing.T, ctx context.Context, f kernelFixture) (string, string) {
	t.Helper()
	var reported, estimated string
	require.NoError(t, f.Pool.QueryRow(ctx, `
SELECT coalesce(sum(provider_reported_cost_usd), 0)::text, coalesce(sum(estimated_cost_usd), 0)::text
FROM project_daily_model_costs WHERE org_id = $1`, kernelTestOrgID).
		Scan(&reported, &estimated))
	return reported, estimated
}

func TestProjectDailyModelCostsAddEachFinishedCallOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	estimator := &fixedCostEstimator{cost: "0.5"}
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(estimator))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	runBudgetTurn(t, ctx, f, agentID, userID, "reported", f.Now.Add(time.Second),
		budgetResponse("resp_reported", "2"), nil)
	runBudgetTurn(t, ctx, f, agentID, userID, "estimated", f.Now.Add(2*time.Second),
		budgetResponse("resp_estimated", ""), nil)
	estimator.mu.Lock()
	estimator.cost = ""
	estimator.mu.Unlock()
	runBudgetTurn(t, ctx, f, agentID, userID, "unpriced", f.Now.Add(3*time.Second),
		budgetResponse("resp_unpriced", ""), nil)

	reported, estimated := projectDailyModelCostForKernelTest(t, ctx, f)
	require.Equal(t, "2.000000000000000", reported)
	require.Equal(t, "0.500000000000000", estimated)
	var rawReported, rawEstimated string
	require.NoError(t, f.Pool.QueryRow(ctx, `
SELECT coalesce(sum(provider_reported_cost_usd), 0)::text, coalesce(sum(estimated_cost_usd), 0)::text
FROM model_call_contexts WHERE project_id = $1`, kernelTestProjectID).Scan(&rawReported, &rawEstimated))
	require.Equal(t, rawReported, reported)
	require.Equal(t, rawEstimated, estimated)
	require.Equal(t, [2]int{1, 0}, projectDailyModelCostRowsForKernelTest(t, ctx, f))
}

func TestProjectDailyModelCostsMergeInBatches(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	_, err := f.Pool.Exec(ctx, `
INSERT INTO project_daily_model_costs(
	org_id, day, project_id, model_call_context_id, provider_reported_cost_usd, estimated_cost_usd
)
SELECT $1, (statement_timestamp() AT TIME ZONE 'UTC')::date, $2, gen_random_uuid(), 1, 0
FROM generate_series(1, 1500)`, kernelTestOrgID, uuid.New())
	require.NoError(t, err)
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	runBudgetTurn(t, ctx, f, agentID, userID, "first", f.Now.Add(time.Second), budgetResponse("resp_first", "1"), nil)
	require.Equal(t, 501, projectDailyModelCostRowsForKernelTest(t, ctx, f)[1])
	createBudgetForKernelTest(t, ctx, f, kernelTestBudget{project: true, interval: "day", amount: "1"})
	denied := runBudgetTurn(t, ctx, f, agentID, userID, "denied", f.Now.Add(time.Minute),
		budgetResponse("resp_denied", "1"), nil)
	require.Zero(t, denied.responded)
	require.Zero(t, projectDailyModelCostRowsForKernelTest(t, ctx, f)[1], "a denied call must still drain pending costs")
	reported, _ := projectDailyModelCostForKernelTest(t, ctx, f)
	require.Equal(t, "1501.000000000000000", reported)
}

func projectDailyModelCostRowsForKernelTest(t *testing.T, ctx context.Context, f kernelFixture) [2]int {
	t.Helper()
	var rows [2]int
	require.NoError(t, f.Pool.QueryRow(ctx, `
SELECT count(*), count(model_call_context_id) FROM project_daily_model_costs WHERE org_id = $1`,
		kernelTestOrgID).Scan(&rows[0], &rows[1]))
	return rows
}

func TestProjectDailyModelCostsRejectInvalidRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	for name, row := range map[string][]any{
		"NaN reported cost":  {kernelTestProjectID, "NaN", "0"},
		"NaN estimated cost": {kernelTestProjectID, "0", "NaN"},
		"negative cost":      {kernelTestProjectID, "-1", "0"},
	} {
		_, err := f.Pool.Exec(ctx, `
INSERT INTO project_daily_model_costs(org_id, day, project_id, provider_reported_cost_usd, estimated_cost_usd)
VALUES ($1, current_date, $2, $3::numeric, $4::numeric)`, append([]any{kernelTestOrgID}, row...)...)
		require.Error(t, err, name)
	}
}

func TestProjectDailyModelCostsSumConcurrentFinishes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	_, err := f.Pool.Exec(ctx, `
INSERT INTO project_daily_model_costs(org_id, day, project_id, provider_reported_cost_usd, estimated_cost_usd)
VALUES ($1, (statement_timestamp() AT TIME ZONE 'UTC')::date, $2, 0, 0)`, kernelTestOrgID, kernelTestProjectID)
	require.NoError(t, err)
	day, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = day.Rollback(ctx) }()
	_, err = day.Exec(ctx, `SELECT 1 FROM project_daily_model_costs WHERE project_id = $1 FOR UPDATE`, kernelTestProjectID)
	require.NoError(t, err)
	merging, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = merging.Rollback(ctx) }()
	_, err = merging.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('project_daily_model_costs:' || $1::text, 0))`, kernelTestOrgID)
	require.NoError(t, err)
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	const agents = 6
	type run struct {
		turn     ModelWorkExecution
		executor AgentExecutor
	}
	runs := make([]run, 0, agents)
	for index := range agents {
		at := f.Now.Add(time.Duration(index+1) * time.Second)
		agentID, userID := f.createNamedAgentWithModelOptions(
			t, ctx, fmt.Sprintf("Budget %d", index), "openai/budget-model", at, kernelConfiguredModelOptions{},
		)
		client := &sequenceKernelModel{
			providerModelSlug: "budget-model", responses: []model.Response{budgetResponse("resp_concurrent", "1")},
		}
		runs = append(runs, run{
			turn: f.admitContentInputTurn(t, ctx, agentID, userID, "concurrent", at),
			executor: AgentExecutor{
				Store:         f.Store,
				ModelResolver: liveTestModelResolver(f.Store, client),
				ToolExecutor:  tools.Executor{Store: f.Store},
				Now:           func() time.Time { return at.Add(time.Second) },
			},
		})
	}
	errs := make(chan error, agents)
	var wg sync.WaitGroup
	for _, current := range runs {
		wg.Go(func() { errs <- current.executor.ExecuteModelWork(runCtx, current.turn) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "a finishing call must not wait on the day's merged row")
	}
	require.Equal(t, [2]int{1 + agents, agents}, projectDailyModelCostRowsForKernelTest(t, ctx, f))
	require.NoError(t, day.Rollback(ctx))
	at := f.Now.Add(time.Minute)
	agentID, userID := f.createNamedAgentWithModelOptions(
		t, ctx, "Budget merge", "openai/budget-model", at, kernelConfiguredModelOptions{},
	)
	runBudgetTurn(t, ctx, f, agentID, userID, "skipped", at, budgetResponse("resp_skipped", "1"), nil)
	require.Equal(t, [2]int{2 + agents, 1 + agents}, projectDailyModelCostRowsForKernelTest(t, ctx, f),
		"a merge must skip while another merge holds the org")
	require.NoError(t, merging.Rollback(ctx))
	_, err = f.Pool.Exec(ctx, `
INSERT INTO project_daily_model_costs(
	org_id, day, project_id, model_call_context_id, provider_reported_cost_usd, estimated_cost_usd
) VALUES ($1, (statement_timestamp() AT TIME ZONE 'UTC')::date - 1, $2, $3, 1, 0)`,
		kernelTestOrgID, kernelTestProjectID, uuid.New())
	require.NoError(t, err)
	runBudgetTurn(t, ctx, f, agentID, userID, "merge", at.Add(time.Second), budgetResponse("resp_merge", "1"), nil)
	require.Equal(t, [2]int{2, 0}, projectDailyModelCostRowsForKernelTest(t, ctx, f),
		"a merge must fold the project's leftovers from earlier days into their own day")
	reported, _ := projectDailyModelCostForKernelTest(t, ctx, f)
	require.Equal(t, "9.000000000000000", reported)
}

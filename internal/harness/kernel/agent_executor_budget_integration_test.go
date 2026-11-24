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
	mu       sync.Mutex
	cost     modelenvelope.ProviderReportedCostUSD
	unpriced bool
	inputs   []modelprice.Input
}

func (e *fixedCostEstimator) Prices(modelprice.Input) bool {
	return !e.unpriced
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

var budgetTestPricing = modelprice.Pricing{Input: "100000", Output: "200000"}

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
	profile  bool
	agent    bool
	interval string
	amount   string
}

func createBudgetForKernelTest(
	t *testing.T, ctx context.Context, f kernelFixture, agentID uuid.UUID, budget kernelTestBudget,
) uuid.UUID {
	t.Helper()
	input := identitystore.CreateBudgetInput{OrgID: kernelTestOrgID, Interval: budget.interval, AmountUSD: budget.amount}
	if budget.project || budget.profile || budget.agent {
		projectID := kernelTestProjectID
		input.ProjectID = &projectID
	}
	if budget.profile {
		var profileID uuid.UUID
		require.NoError(t, f.Pool.QueryRow(ctx, `SELECT agent_profile_id FROM agents WHERE id = $1`, agentID).
			Scan(&profileID))
		input.AgentProfileID = &profileID
	}
	if budget.agent {
		input.AgentID = &agentID
	}
	record, err := f.Store.Identity().CreateBudget(ctx, input)
	require.NoError(t, err)
	return record.ID
}

func TestAgentExecutorBudgetDenialNotifiesWithoutProviderRequest(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(&fixedCostEstimator{}))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	first := runBudgetTurn(t, ctx, f, agentID, userID, "spend", f.Now.Add(time.Second),
		budgetResponse("resp_spend", "2"), nil)
	require.Equal(t, 1, first.responded)
	require.Zero(t, first.notices)

	createBudgetForKernelTest(t, ctx, f, agentID, kernelTestBudget{project: true, interval: "month", amount: "2"})
	denied := runBudgetTurn(t, ctx, f, agentID, userID, "denied", f.Now.Add(time.Minute),
		budgetResponse("resp_denied", ""), nil)
	require.Zero(t, denied.responded, "a call over budget must not reach the provider")
	require.Equal(t, 1, denied.notices)
	assertDurableModelErrorForKernelTest(t, ctx, f, agentID, denied.turnID,
		string(modelprotocol.ErrorKindRuntime), storeerr.BudgetReachedCode)
}

func TestAgentExecutorBudgetStopsTheRetryOfAFailedCallThatReachedIt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(&fixedCostEstimator{}))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	createBudgetForKernelTest(t, ctx, f, agentID, kernelTestBudget{project: true, interval: "month", amount: "2"})
	turn := f.admitContentInputTurn(t, ctx, agentID, userID, "fail", f.Now.Add(time.Second))
	client := &sequenceKernelModel{
		providerModelSlug: "budget-model",
		errs:              []error{model.ProviderError{Kind: model.ErrorKindTransient, Source: "test", Message: "retry"}},
		errorResponses: []model.Response{{
			ID: "resp_failed", ServedProviderModelSlug: "budget-model-served", ProviderReportedCostUSD: "2",
			Usage: model.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5},
		}},
		responses: []model.Response{budgetResponse("resp_retry", "")},
	}
	notices := 0
	executor := AgentExecutor{
		Store:           f.Store,
		ModelResolver:   liveTestModelResolver(f.Store, client),
		ToolExecutor:    tools.Executor{Store: f.Store},
		Now:             func() time.Time { return f.Now.Add(2 * time.Second) },
		ModelRetryDelay: immediateKernelModelRetryDelay,
		OnModelFailure: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			notices++
			return nil
		},
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, turn))
	require.NoError(t, executor.ExecuteModelWork(ctx,
		continueTurnOnNewLeaseForKernelTest(t, ctx, f, turn, f.Now.Add(time.Minute))))

	require.Equal(t, 1, client.respondedCount(), "the retry must not reach the provider")
	require.Equal(t, 1, notices)
	assertDurableModelErrorForKernelTest(t, ctx, f, agentID, turn.TurnID,
		string(modelprotocol.ErrorKindRuntime), storeerr.BudgetReachedCode)
	require.Equal(t, "2.000000000000000", dailyModelCostForKernelTest(t, ctx, f), "the failed call counts")
}

func TestAgentExecutorBudgetLetsAStoppedAgentRunAgain(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(&fixedCostEstimator{}))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	turn := func(text string, minute int, cost modelenvelope.ProviderReportedCostUSD) int {
		return runBudgetTurn(t, ctx, f, agentID, userID, text, f.Now.Add(time.Duration(minute)*time.Minute),
			budgetResponse("resp_"+text, cost), nil).responded
	}
	require.Equal(t, 1, turn("spend", 1, "2"))
	budgetID := createBudgetForKernelTest(t, ctx, f, agentID,
		kernelTestBudget{project: true, interval: "month", amount: "2"})
	require.Zero(t, turn("stopped", 2, ""))

	_, err := f.Store.Identity().UpdateBudgetAmount(ctx, kernelTestOrgID, budgetID, "5")
	require.NoError(t, err)
	require.Equal(t, 1, turn("raised", 3, "3"), "a new message runs once the budget is raised")
	require.Zero(t, turn("stopped_again", 4, ""))

	require.NoError(t, f.Store.Identity().DeleteBudget(ctx, kernelTestOrgID, budgetID))
	require.Equal(t, 1, turn("removed", 5, ""), "a new message runs once the budget is removed")
}

func TestAgentExecutorBudgetScopes(t *testing.T) {
	t.Parallel()
	projectMonthly := storeerr.BudgetReachedMessage("project", "month")
	orgMonthly := storeerr.BudgetReachedMessage("org", "month")
	tests := []struct {
		name        string
		spend       modelenvelope.ProviderReportedCostUSD
		estimate    modelenvelope.ProviderReportedCostUSD
		pricing     bool
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
		}, wantMessage: storeerr.BudgetReachedMessage("project", "lifetime")},
		{name: "daily budget reached", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "day", amount: "2"},
		}, wantMessage: storeerr.BudgetReachedMessage("project", "day")},
		{name: "weekly budget reached", spend: "2", budgets: []kernelTestBudget{
			{project: true, interval: "week", amount: "2"},
		}, wantMessage: storeerr.BudgetReachedMessage("project", "week")},
		{name: "profile budget reached", spend: "2", budgets: []kernelTestBudget{
			{profile: true, interval: "month", amount: "2"},
		}, wantMessage: storeerr.BudgetReachedMessage("profile", "month")},
		{name: "agent budget reached", spend: "2", budgets: []kernelTestBudget{
			{agent: true, interval: "month", amount: "2"},
		}, wantMessage: storeerr.BudgetReachedMessage("agent", "month")},
		{name: "project budget is named before profile and agent", spend: "2", budgets: []kernelTestBudget{
			{agent: true, interval: "month", amount: "1"}, {profile: true, interval: "month", amount: "1"},
			{project: true, interval: "month", amount: "1"},
		}, wantMessage: projectMonthly},
		{name: "profile budget is named before agent", spend: "2", budgets: []kernelTestBudget{
			{agent: true, interval: "month", amount: "1"}, {profile: true, interval: "month", amount: "1"},
		}, wantMessage: storeerr.BudgetReachedMessage("profile", "month")},
		{name: "estimated cost counts", estimate: "2", budgets: []kernelTestBudget{
			{project: true, interval: "month", amount: "2"},
		}, wantMessage: projectMonthly},
		{name: "custom price counts instead of the reported cost", spend: "0", pricing: true, budgets: []kernelTestBudget{
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
			wantMessage: storeerr.BudgetReachedMessage("project", "day"),
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
			wantMessage: storeerr.BudgetReachedMessage("project", "week"),
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
			wantMessage: storeerr.BudgetReachedMessage("project", "lifetime"),
		},
		{
			name: "agent spend from earlier months does not count toward the month", spend: "2",
			createdAt: `date_trunc('month', statement_timestamp(), 'UTC') - interval '1 microsecond'`,
			budgets:   []kernelTestBudget{{agent: true, interval: "month", amount: "2"}},
		},
		{
			name: "agent spend from earlier months counts toward lifetime", spend: "2",
			createdAt:   `date_trunc('month', statement_timestamp(), 'UTC') - interval '1 microsecond'`,
			budgets:     []kernelTestBudget{{agent: true, interval: "lifetime", amount: "2"}},
			wantMessage: storeerr.BudgetReachedMessage("agent", "lifetime"),
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
			var options kernelConfiguredModelOptions
			if test.pricing {
				pricing := budgetTestPricing
				options.Pricing = &pricing
			}
			agentID, userID := f.createAgentWithModelOptions(t, ctx, "openai/budget-model", f.Now, options)
			var move func(model.Response)
			if test.createdAt != "" {
				move = func(model.Response) { moveStartedModelCallForKernelTest(t, ctx, f, test.createdAt) }
			}
			runBudgetTurn(t, ctx, f, agentID, userID, "spend", f.Now.Add(time.Second),
				budgetResponse("resp_spend", test.spend), move)
			for _, budget := range test.budgets {
				createBudgetForKernelTest(t, ctx, f, agentID, budget)
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

func TestAgentExecutorProfileAndAgentBudgetsOnlyStopTheirOwnAgents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(&fixedCostEstimator{}))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	otherID, _ := f.createNamedAgentWithModelOptions(t, ctx, "Other", "openai/budget-model", f.Now.Add(time.Hour),
		kernelConfiguredModelOptions{})
	runBudgetTurn(t, ctx, f, agentID, userID, "spend", f.Now.Add(time.Second), budgetResponse("resp_spend", "2"), nil)
	createBudgetForKernelTest(t, ctx, f, agentID, kernelTestBudget{agent: true, interval: "month", amount: "2"})
	createBudgetForKernelTest(t, ctx, f, agentID, kernelTestBudget{profile: true, interval: "month", amount: "2"})
	createBudgetForKernelTest(t, ctx, f, otherID, kernelTestBudget{agent: true, interval: "month", amount: "2"})
	createBudgetForKernelTest(t, ctx, f, otherID, kernelTestBudget{profile: true, interval: "month", amount: "2"})
	other := runBudgetTurn(t, ctx, f, otherID, userID, "other", f.Now.Add(2*time.Hour),
		budgetResponse("resp_other", "0"), nil)
	require.Equal(t, 1, other.responded, "another agent's budgets count only its own spend")
	denied := runBudgetTurn(t, ctx, f, agentID, userID, "denied", f.Now.Add(3*time.Hour),
		budgetResponse("resp_denied", "0"), nil)
	require.Zero(t, denied.responded)
}

func TestAgentExecutorBudgetRefusesUnpricedModels(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(&fixedCostEstimator{unpriced: true}))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	pricing := budgetTestPricing
	pricedAgentID, _ := f.createNamedAgentWithModelOptions(t, ctx, "Priced", "priced/budget-model",
		f.Now, kernelConfiguredModelOptions{Pricing: &pricing})
	createBudgetForKernelTest(t, ctx, f, agentID, kernelTestBudget{project: true, interval: "month", amount: "100"})

	denied := runBudgetTurn(t, ctx, f, agentID, userID, "unpriced", f.Now.Add(time.Second),
		budgetResponse("resp_unpriced", ""), nil)
	require.Zero(t, denied.responded, "an unpriced model must not run while a budget applies")
	assertDurableModelErrorForKernelTest(t, ctx, f, agentID, denied.turnID,
		string(modelprotocol.ErrorKindRuntime), storeerr.BudgetModelUnpricedCode)
	priced := runBudgetTurn(t, ctx, f, pricedAgentID, userID, "priced", f.Now.Add(time.Minute),
		budgetResponse("resp_priced", ""), nil)
	require.Equal(t, 1, priced.responded, "a model with its own prices keeps running")
}

func TestAgentExecutorBudgetLetsModelsPricedAtZeroRun(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(&fixedCostEstimator{}))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	free := modelprice.Pricing{Input: "0", Output: "0"}
	freeAgentID, _ := f.createNamedAgentWithModelOptions(t, ctx, "Free", "priced/budget-model",
		f.Now, kernelConfiguredModelOptions{Pricing: &free})
	runBudgetTurn(t, ctx, f, agentID, userID, "spend", f.Now.Add(time.Second), budgetResponse("resp_spend", "2"), nil)
	createBudgetForKernelTest(t, ctx, f, agentID, kernelTestBudget{project: true, interval: "month", amount: "2"})

	freeTurn := runBudgetTurn(t, ctx, f, freeAgentID, userID, "free", f.Now.Add(time.Minute),
		budgetResponse("resp_free", "1"), nil)
	require.Equal(t, 1, freeTurn.responded, "a model priced at 0 keeps running after the budget is reached")
	denied := runBudgetTurn(t, ctx, f, agentID, userID, "denied", f.Now.Add(2*time.Minute),
		budgetResponse("resp_denied", ""), nil)
	require.Zero(t, denied.responded, "paid models in the project still stop")
	incomplete := budgetResponse("resp_free_incomplete", "1")
	incomplete.Usage.Incomplete = true
	unmetered := budgetResponse("resp_free_unmetered", "1")
	unmetered.Usage = model.Usage{}
	for i, response := range []model.Response{incomplete, unmetered} {
		turn := runBudgetTurn(t, ctx, f, freeAgentID, userID, response.ID,
			f.Now.Add(time.Duration(3+i)*time.Minute), response, nil)
		require.Equal(t, 1, turn.responded, response.ID)
	}
	require.Equal(t, "2.000000000000000", dailyModelCostForKernelTest(t, ctx, f), "calls priced at 0 add no spend")
}

func TestAgentExecutorRecordsEffectiveCosts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	estimator := &fixedCostEstimator{cost: "0.25"}
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(estimator))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	runBudgetTurn(t, ctx, f, agentID, userID, "estimated", f.Now.Add(time.Second),
		budgetResponse("resp_estimated", ""), nil)
	runBudgetTurn(t, ctx, f, agentID, userID, "reported", f.Now.Add(time.Minute),
		budgetResponse("resp_reported", "1"), nil)
	pricing := budgetTestPricing
	pricedAgentID, _ := f.createNamedAgentWithModelOptions(t, ctx, "Priced", "priced/budget-model",
		f.Now, kernelConfiguredModelOptions{Pricing: &pricing})
	runBudgetTurn(t, ctx, f, pricedAgentID, userID, "custom", f.Now.Add(3*time.Minute),
		budgetResponse("resp_custom", "1"), nil)

	rows, err := f.Pool.Query(ctx, `
SELECT provider_response_id, coalesce(provider_reported_cost_usd::text, ''),
       coalesce(effective_cost_usd::text, ''), coalesce(effective_cost_kind, '')
FROM model_call_contexts WHERE project_id = $1 ORDER BY created_at`, kernelTestProjectID)
	require.NoError(t, err)
	defer rows.Close()
	type costs struct{ response, reported, effective, kind string }
	var got []costs
	for rows.Next() {
		var row costs
		require.NoError(t, rows.Scan(&row.response, &row.reported, &row.effective, &row.kind))
		got = append(got, row)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []costs{
		{response: "resp_estimated", effective: "0.250000000000000", kind: "estimated"},
		{response: "resp_reported", reported: "1.000000000000000", effective: "1.000000000000000", kind: "provider_reported"},
		{response: "resp_custom", reported: "1.000000000000000", effective: "2.000000000000000", kind: "custom"},
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
	f := newKernelFixture(t, ctx, storage.WithModelCostEstimator(&fixedCostEstimator{}))
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now, "run_command")
	runBudgetTurn(t, ctx, f, agentID, userID, "seed history before overflow", f.Now.Add(time.Second),
		budgetResponse("resp_seed", "0"), nil)
	createBudgetForKernelTest(t, ctx, f, agentID, kernelTestBudget{project: true, interval: "month", amount: "1"})

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

func dailyModelCostForKernelTest(t *testing.T, ctx context.Context, f kernelFixture) string {
	t.Helper()
	var cost string
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT effective_cost_usd::text FROM budget_interval_cost($1, NULL, NULL, NULL, 'lifetime')`,
		kernelTestOrgID).Scan(&cost))
	return cost
}

func TestDailyModelCostsAddEachFinishedCallOnce(t *testing.T) {
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

	cost := dailyModelCostForKernelTest(t, ctx, f)
	require.Equal(t, "2.500000000000000", cost)
	var raw string
	require.NoError(t, f.Pool.QueryRow(ctx, `
SELECT coalesce(sum(effective_cost_usd), 0)::text FROM model_call_contexts WHERE project_id = $1`,
		kernelTestProjectID).Scan(&raw))
	require.Equal(t, raw, cost)
	mergeDailyModelCostsForKernelTest(t, ctx, f)
	require.Equal(t, [2]int{1, 0}, dailyModelCostRowsForKernelTest(t, ctx, f))
	require.Equal(t, cost, dailyModelCostForKernelTest(t, ctx, f))
}

func TestDailyModelCostsMergeInBatches(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	agentID, userID := f.createAgent(t, ctx, "openai/budget-model", f.Now)
	for index := range 3 {
		id := fmt.Sprintf("resp_batch_%d", index)
		runBudgetTurn(t, ctx, f, agentID, userID, id, f.Now.Add(time.Duration(index+1)*time.Second),
			budgetResponse(id, "1"), nil)
	}
	merge := func() int64 {
		merged, err := f.Store.Identity().MergeDailyModelCosts(ctx, 2)
		require.NoError(t, err)
		return merged
	}
	require.Equal(t, [2]int{0, 3}, dailyModelCostRowsForKernelTest(t, ctx, f))
	pending := func(limit int32) int64 {
		count, err := f.Store.Identity().CountPendingModelCosts(ctx, limit)
		require.NoError(t, err)
		return count
	}
	require.Equal(t, int64(3), pending(10))
	require.Equal(t, int64(2), pending(2), "the backlog count stops at its ceiling")
	require.Equal(t, int64(2), merge())
	require.Equal(t, [2]int{1, 1}, dailyModelCostRowsForKernelTest(t, ctx, f))
	require.Equal(t, int64(1), merge())
	require.Equal(t, [2]int{1, 0}, dailyModelCostRowsForKernelTest(t, ctx, f))
	require.Zero(t, pending(10))
	require.Equal(t, "3.000000000000000", dailyModelCostForKernelTest(t, ctx, f))
}

func mergeDailyModelCostsForKernelTest(t *testing.T, ctx context.Context, f kernelFixture) int64 {
	t.Helper()
	merged, err := f.Store.Identity().MergeDailyModelCosts(ctx, 10_000)
	require.NoError(t, err)
	return merged
}

func dailyModelCostRowsForKernelTest(t *testing.T, ctx context.Context, f kernelFixture) [2]int {
	t.Helper()
	var rows [2]int
	require.NoError(t, f.Pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM daily_model_costs WHERE org_id = $1 AND project_id IS NULL),
       (SELECT count(*) FROM pending_model_costs WHERE org_id = $1)`,
		kernelTestOrgID).Scan(&rows[0], &rows[1]))
	return rows
}

func TestDailyModelCostsRejectInvalidRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	for _, cost := range []string{"NaN", "Infinity", "-1"} {
		_, err := f.Pool.Exec(ctx, `
INSERT INTO daily_model_costs(org_id, day, effective_cost_usd)
VALUES ($1, current_date, $2::numeric)`, kernelTestOrgID, cost)
		require.Error(t, err, cost)
	}
}

func TestDailyModelCostsSumConcurrentFinishes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	_, err := f.Pool.Exec(ctx, `
INSERT INTO daily_model_costs(org_id, day, effective_cost_usd)
VALUES ($1, (statement_timestamp() AT TIME ZONE 'UTC')::date, 0)`, kernelTestOrgID)
	require.NoError(t, err)
	day, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = day.Rollback(ctx) }()
	_, err = day.Exec(ctx, `SELECT 1 FROM daily_model_costs WHERE org_id = $1 AND project_id IS NULL FOR UPDATE`,
		kernelTestOrgID)
	require.NoError(t, err)
	merging, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = merging.Rollback(ctx) }()
	_, err = merging.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('daily_model_costs', 0))`)
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
	require.Equal(t, [2]int{1, agents}, dailyModelCostRowsForKernelTest(t, ctx, f))
	require.NoError(t, day.Rollback(ctx))
	require.Zero(t, mergeDailyModelCostsForKernelTest(t, ctx, f), "a merge must skip while another merge runs")
	require.NoError(t, merging.Rollback(ctx))
	_, err = f.Pool.Exec(ctx, `
UPDATE pending_model_costs SET day = day - 1
WHERE model_call_context_id = (SELECT model_call_context_id FROM pending_model_costs WHERE org_id = $1 LIMIT 1)`,
		kernelTestOrgID)
	require.NoError(t, err)
	require.Equal(t, int64(agents), mergeDailyModelCostsForKernelTest(t, ctx, f))
	require.Equal(t, [2]int{2, 0}, dailyModelCostRowsForKernelTest(t, ctx, f),
		"a merge must fold leftovers from earlier days into their own day")
	var buckets int
	require.NoError(t, f.Pool.QueryRow(ctx, `SELECT count(*) FROM daily_model_costs WHERE org_id = $1`,
		kernelTestOrgID).Scan(&buckets))
	require.Equal(t, 4+2*agents, buckets, "each call adds to its org, project, profile, and agent totals")
	require.Equal(t, "6.000000000000000", dailyModelCostForKernelTest(t, ctx, f))
}

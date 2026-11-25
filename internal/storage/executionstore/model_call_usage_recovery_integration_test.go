//go:build integration

package executionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestModelCallGenerationSurvivesTeardownInEitherOrder(t *testing.T) {
	t.Parallel()
	teardowns := map[string]struct {
		run   func(context.Context, processDaemonFixture) error
		state executionstore.ModelCallState
	}{
		"cancel": {func(ctx context.Context, fixture processDaemonFixture) error {
			_, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID,
			})
			return err
		}, executionstore.ModelCallContextCanceled},
		"release": {func(ctx context.Context, fixture processDaemonFixture) error {
			return fixture.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, fixture.AgentID, fixture.Lock.ID)
		}, executionstore.ModelCallContextFailed},
	}
	for name, teardown := range teardowns {
		for _, generationFirst := range []bool{true, false} {
			order := "teardown_first"
			if generationFirst {
				order = "generation_first"
			}
			t.Run(name+"_"+order, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "generation_"+name+"_"+order)
				record := func(ctx context.Context, fixture processDaemonFixture) error {
					return fixture.Store.Execution().RecordModelCallGenerationID(
						ctx, fixture.AgentID, fixture.Lock.ID, claim.Context.ID, "gen-1",
					)
				}
				first, second := teardown.run, record
				if generationFirst {
					first, second = record, teardown.run
				}
				holder, err := fixture.Store.pool.Begin(ctx)
				require.NoError(t, err)
				_, err = holder.Exec(ctx, `SELECT 1 FROM agent_runtime_locks WHERE id = $1 FOR UPDATE`, fixture.Lock.ID)
				require.NoError(t, err)
				firstDone, secondDone := make(chan error, 1), make(chan error, 1)
				go func() { firstDone <- first(ctx, fixture) }()
				waitForRuntimeLockWaiters(t, ctx, fixture, 1)
				go func() { secondDone <- second(ctx, fixture) }()
				waitForRuntimeLockWaiters(t, ctx, fixture, 2)
				require.NoError(t, holder.Rollback(ctx))
				require.NoError(t, <-firstDone)
				require.NoError(t, <-secondDone)
				assertModelCallGeneration(t, ctx, fixture, claim.Context.ID, teardown.state, "gen-1")
			})
		}
	}
}

func TestReapCopiesModelCallGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "generation_reap")
	execution := fixture.Store.Execution()
	recordGeneration(t, ctx, fixture, claim.Context.ID, "gen-1")
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, fixture.Lock.ID)
	reaped, err := execution.ReapExpiredAgentRuntimeLocks(ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, reaped)
	assertModelCallGeneration(t, ctx, fixture, claim.Context.ID, executionstore.ModelCallContextFailed, "gen-1")
}

func TestTeardownIgnoresAnotherCallsGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "generation_other_call")
	recordGeneration(t, ctx, fixture, uuid.New(), "gen-other")
	cancelAgent(t, ctx, fixture)
	assertModelCallGeneration(t, ctx, fixture, claim.Context.ID, executionstore.ModelCallContextCanceled, "")
}

func TestRecoveredModelCallUsageIsWrittenOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "generation_recovered_usage")
	execution := fixture.Store.Execution()
	recordGeneration(t, ctx, fixture, claim.Context.ID, "gen-1")
	cancelAgent(t, ctx, fixture)
	recoverUsage := func(responseID string, cost modelenvelope.ProviderReportedCostUSD, usage modelenvelope.Usage) bool {
		t.Helper()
		recorded, err := execution.RecordRecoveredModelCallUsage(ctx, executionstore.RecordRecoveredModelCallUsageInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, ModelCallContextID: claim.Context.ID,
			ProviderResponseID: responseID, ProviderReportedCostUSD: cost, Usage: usage,
		})
		require.NoError(t, err)
		return recorded
	}
	recovered := modelenvelope.Usage{
		InputTokens: 5253, UncachedInputTokens: 4253, CacheReadTokens: 1000, OutputTokens: 257, ReasoningTokens: 111,
	}
	require.False(t, recoverUsage("gen-other", "1", modelenvelope.Usage{InputTokens: 1, OutputTokens: 1}))
	require.True(t, recoverUsage("gen-1", "0.012806", recovered))
	require.False(t, recoverUsage("gen-1", "9", modelenvelope.Usage{InputTokens: 9, OutputTokens: 9}))

	row := loadModelCallContext(t, ctx, fixture, claim.Context.ID)
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("0.012806"), row.ProviderReportedCostUSD)
	require.Equal(t, recovered, row.Usage)
	var effectiveCost, effectiveKind, pendingCost string
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
SELECT context.effective_cost_usd::text, context.effective_cost_kind, pending.effective_cost_usd::text
FROM model_call_contexts context
JOIN pending_model_costs pending ON pending.model_call_context_id = context.id
WHERE context.id = $1`, claim.Context.ID).Scan(&effectiveCost, &effectiveKind, &pendingCost))
	require.Equal(t, "provider_reported", effectiveKind)
	require.Equal(t, effectiveCost, pendingCost)
	_, err := fixture.Store.pool.Exec(ctx, `UPDATE model_call_contexts SET provider_reported_cost_usd = 2 WHERE id = $1`,
		claim.Context.ID)
	assertPgErrorMessage(t, err, "25006", "terminal model_call_contexts are immutable")
}

func TestRecoveredUsageUsesCustomPricesOnlyWhenComplete(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]struct {
		usage  modelenvelope.Usage
		stored modelenvelope.Usage
		kind   string
		cost   string
	}{
		"complete usage": {
			modelenvelope.Usage{InputTokens: 1000, OutputTokens: 100},
			modelenvelope.Usage{InputTokens: 1000, UncachedInputTokens: 1000, OutputTokens: 100}, "custom", "0.0035",
		},
		"incomplete usage": {
			modelenvelope.Usage{OutputTokens: 100, Incomplete: true},
			modelenvelope.Usage{OutputTokens: 100}, "provider_reported", "0.05",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "generation_pricing_"+want.kind)
			recordGeneration(t, ctx, fixture, claim.Context.ID, "gen-1")
			cancelAgent(t, ctx, fixture)
			_, err := fixture.Store.pool.Exec(ctx,
				`ALTER TABLE configured_model_revisions DISABLE TRIGGER configured_model_revisions_immutable`)
			require.NoError(t, err)
			_, err = fixture.Store.pool.Exec(ctx, `
UPDATE configured_model_revisions SET pricing = jsonb_build_object(
  'input_mtok', '3', 'cache_write_mtok', '3.75', 'output_mtok', '5')
WHERE id = (SELECT configured_model_revision_id FROM model_call_contexts WHERE id = $1)`,
				claim.Context.ID)
			require.NoError(t, err)
			recorded, err := fixture.Store.Execution().RecordRecoveredModelCallUsage(ctx,
				executionstore.RecordRecoveredModelCallUsageInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, ModelCallContextID: claim.Context.ID,
					ProviderResponseID: "gen-1", ProviderReportedCostUSD: "0.05", Usage: want.usage,
				})
			require.NoError(t, err)
			require.True(t, recorded)
			var cost, kind, pending string
			require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
SELECT trim_scale(context.effective_cost_usd)::text, context.effective_cost_kind,
  trim_scale(pending.effective_cost_usd)::text
FROM model_call_contexts context
JOIN pending_model_costs pending ON pending.model_call_context_id = context.id
WHERE context.id = $1`, claim.Context.ID).Scan(&cost, &kind, &pending))
			require.Equal(t, []string{want.kind, want.cost, want.cost}, []string{kind, cost, pending})
			require.Equal(t, want.stored, loadModelCallContext(t, ctx, fixture, claim.Context.ID).Usage)
		})
	}
}

func TestListModelUsageRecoveriesFollowsSchedule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "generation_schedule")
	execution := fixture.Store.Execution()
	recordGeneration(t, ctx, fixture, claim.Context.ID, "gen-1")
	cancelAgent(t, ctx, fixture)
	_, err := fixture.Store.pool.Exec(ctx,
		`ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard`)
	require.NoError(t, err)
	endedAgo := func(ago time.Duration) {
		t.Helper()
		_, err := fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET created_at = statement_timestamp() - $2::bigint * interval '1 microsecond' - interval '1 second',
  completed_at = statement_timestamp() - $2::bigint * interval '1 microsecond'
WHERE id = $1`, claim.Context.ID, ago.Microseconds())
		require.NoError(t, err)
	}
	schedule := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 24 * time.Hour}
	list := func(after executionstore.ModelUsageRecoveryCursor, lookback time.Duration) (
		[]executionstore.ModelUsageRecovery, executionstore.ModelUsageRecoveryCursor,
	) {
		t.Helper()
		due, next, _, err := execution.ListModelUsageRecoveries(ctx, after, lookback, 10, schedule)
		require.NoError(t, err)
		return due, next
	}
	var start executionstore.ModelUsageRecoveryCursor

	endedAgo(12 * time.Second)
	due, _ := list(start, 10*time.Second)
	require.Empty(t, due, "no scheduled lookup falls in the window yet")

	endedAgo(20 * time.Second)
	due, _ = list(start, 10*time.Second)
	require.Len(t, due, 1)
	require.Equal(t, executionstore.ModelUsageRecovery{
		OrgID: testOrgID, ProjectID: testProjectID, AgentID: fixture.AgentID, ModelCallContextID: claim.Context.ID,
		ModelProviderConfigID: due[0].ModelProviderConfigID, GenerationID: "gen-1",
	}, due[0])

	endedAgo(2*time.Minute + 5*time.Second)
	due, cursor := list(start, 2*time.Minute)
	require.Len(t, due, 1, "several scheduled lookups in one window return the call once")
	due, _ = list(cursor, 0)
	require.Empty(t, due, "the next window starts where the last one ended")

	endedAgo(15 * time.Second)
	due, _ = list(cursor, 0)
	require.Len(t, due, 1, "a lookup time after the cursor is picked up by the next window")

	endedAgo(24*time.Hour + 5*time.Second)
	due, _ = list(start, 10*time.Second)
	require.Len(t, due, 1)
	require.True(t, due[0].Final)

	endedAgo(20 * time.Second)
	recorded, err := execution.RecordRecoveredModelCallUsage(ctx, executionstore.RecordRecoveredModelCallUsageInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ModelCallContextID: claim.Context.ID,
		ProviderResponseID: "gen-1", ProviderReportedCostUSD: "0.01",
	})
	require.NoError(t, err)
	require.True(t, recorded)
	due, _ = list(start, 10*time.Second)
	require.Empty(t, due, "a call with a cost leaves the sweep")
}

func TestListModelUsageRecoveriesPagesThroughEveryDueCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "generation_pages")
	recordGeneration(t, ctx, fixture, claim.Context.ID, "gen-0")
	cancelAgent(t, ctx, fixture)
	_, err := fixture.Store.pool.Exec(ctx,
		`ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard`)
	require.NoError(t, err)
	_, err = fixture.Store.pool.Exec(ctx, `
INSERT INTO model_call_contexts
SELECT (jsonb_populate_record(c, jsonb_build_object(
  'id', gen_random_uuid(), 'attempt_number', c.attempt_number + g, 'provider_response_id', 'gen-' || g))).*
FROM model_call_contexts c, generate_series(1, 4) g WHERE c.id = $1`, claim.Context.ID)
	require.NoError(t, err)
	_, err = fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts SET created_at = statement_timestamp() - interval '1 minute',
  completed_at = statement_timestamp() - interval '20 seconds'
    - substr(provider_response_id, 5)::int * interval '1 second'
WHERE agent_id = $1`, fixture.AgentID)
	require.NoError(t, err)
	schedule := []time.Duration{15 * time.Second, 30 * time.Second, 24 * time.Hour}
	var cursor executionstore.ModelUsageRecoveryCursor
	var pages [][]string
	for more := true; more && len(pages) < 10; {
		var due []executionstore.ModelUsageRecovery
		due, cursor, more, err = fixture.Store.Execution().ListModelUsageRecoveries(ctx, cursor, time.Minute, 2, schedule)
		require.NoError(t, err)
		var generations []string
		for _, call := range due {
			generations = append(generations, call.GenerationID)
		}
		pages = append(pages, generations)
	}
	require.Equal(t, [][]string{{"gen-4", "gen-3"}, {"gen-2", "gen-1"}, {"gen-0"}}, pages,
		"pages list every due call once, oldest due first, and stop at the clock")
}

func recordGeneration(
	t *testing.T, ctx context.Context, fixture processDaemonFixture, id uuid.UUID, generationID string,
) {
	t.Helper()
	require.NoError(t, fixture.Store.Execution().RecordModelCallGenerationID(
		ctx, fixture.AgentID, fixture.Lock.ID, id, generationID,
	))
}

func cancelAgent(t *testing.T, ctx context.Context, fixture processDaemonFixture) {
	t.Helper()
	_, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID,
	})
	require.NoError(t, err)
}

func assertModelCallGeneration(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	id uuid.UUID,
	state executionstore.ModelCallState,
	generationID string,
) {
	t.Helper()
	row := loadModelCallContext(t, ctx, fixture, id)
	require.Equal(t, state, row.State)
	require.Equal(t, generationID, row.ProviderResponseID)
	if generationID == "" {
		require.Empty(t, row.APIFormat)
		require.Empty(t, row.APIVariant)
		return
	}
	require.Equal(t, modelprotocol.APIFormatOpenAIChatCompletions, row.APIFormat)
	require.Equal(t, modelprotocol.APIVariantOpenRouter, row.APIVariant)
}

func waitForRuntimeLockWaiters(t *testing.T, ctx context.Context, fixture processDaemonFixture, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting)
		return err == nil && waiting >= want
	}, 10*time.Second, 5*time.Millisecond)
}

func loadModelCallContext(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	id uuid.UUID,
) executionstore.ModelCallContextRecord {
	t.Helper()
	row, found, err := fixture.Store.Execution().GetModelCallContext(ctx, testProjectID, fixture.AgentID, id)
	require.NoError(t, err)
	require.True(t, found)
	return row
}

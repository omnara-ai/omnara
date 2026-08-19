//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCompletedToolCallsForModelContextSpanTurnsAndRespectWatermark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "completed_tool_calls_at_watermark")
	now := fixture.Now.Add(time.Minute)
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "completed_tool_calls_at_watermark", "read_process")
	modelContextID := modelContextIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(ctx, testProjectID, fixture.AgentID, modelContextID)
	if err != nil {
		t.Fatalf("load model context: %v", err)
	}
	if !found {
		t.Fatalf("model context %s not found", modelContextID)
	}
	completed, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"late result"}]`),
	})
	if err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	unbounded, err := fixture.Store.Execution().ListCompletedToolCallsForTurn(ctx, testProjectID, fixture.AgentID, completed.TurnID)
	if err != nil {
		t.Fatalf("list unbounded completed tool calls: %v", err)
	}
	if len(unbounded) != 1 {
		t.Fatalf("unbounded completed tool calls = %d, want 1", len(unbounded))
	}
	bounded, err := fixture.Store.Execution().ListCompletedToolCallsAtWatermark(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		contextRecord.InputEventSequence,
	)
	if err != nil {
		t.Fatalf("list bounded completed tool calls: %v", err)
	}
	if len(bounded) != 0 {
		t.Fatalf("bounded completed tool calls at model context watermark = %+v, want none", bounded)
	}
	resultEvents := listTypedToolResultEventsForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)
	if len(resultEvents) != 1 {
		t.Fatalf("tool result events = %+v, want one", resultEvents)
	}
	boundedAfterResult, err := fixture.Store.Execution().ListCompletedToolCallsAtWatermark(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		resultEvents[0].Sequence,
	)
	if err != nil {
		t.Fatalf("list bounded completed tool calls after result: %v", err)
	}
	if len(boundedAfterResult) != 1 || boundedAfterResult[0].ID != toolCallID {
		t.Fatalf("bounded completed tool calls after result = %+v, want %s", boundedAfterResult, toolCallID)
	}
	if boundedAfterResult[0].SourceEventSequence <= 0 ||
		boundedAfterResult[0].ToolResultEventSequence != resultEvents[0].Sequence ||
		boundedAfterResult[0].SourceEventSequence >= boundedAfterResult[0].ToolResultEventSequence {
		t.Fatalf("tool-call event chronology = %+v", boundedAfterResult[0])
	}
	var sourceContentBlockOrdinal int32
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT block.ordinal
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = $1
  AND block.agent_id = $2
  AND block.tool_call_id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&sourceContentBlockOrdinal); err != nil {
		t.Fatalf("load tool-call source content block ordinal: %v", err)
	}
	if sourceContentBlockOrdinal != 0 {
		t.Fatalf("tool-call source ordinal = %d, want 0", sourceContentBlockOrdinal)
	}
	boundedAfterCheckpoint, err := fixture.Store.Execution().ListCompletedToolCallsAtWatermark(
		ctx,
		testProjectID,
		fixture.AgentID,
		resultEvents[0].Sequence,
		resultEvents[0].Sequence+1,
	)
	if err != nil {
		t.Fatalf("list completed tool calls after checkpoint boundary: %v", err)
	}
	if len(boundedAfterCheckpoint) != 0 {
		t.Fatalf("checkpointed tool calls remained in model projection: %+v", boundedAfterCheckpoint)
	}
	emptySuffix, err := fixture.Store.Execution().ListCompletedToolCallsAtWatermark(
		ctx,
		testProjectID,
		fixture.AgentID,
		resultEvents[0].Sequence,
		resultEvents[0].Sequence,
	)
	if err != nil {
		t.Fatalf("list completed tool calls at an empty checkpoint suffix: %v", err)
	}
	if len(emptySuffix) != 0 {
		t.Fatalf("empty checkpoint suffix contained tool calls: %+v", emptySuffix)
	}
	if _, err := fixture.Store.Execution().ListCompletedToolCallsAtWatermark(
		ctx,
		testProjectID,
		fixture.AgentID,
		resultEvents[0].Sequence+1,
		resultEvents[0].Sequence,
	); err == nil {
		t.Fatal("tool-result projection accepted an event cursor beyond its watermark")
	}

	historicalTurnID := completed.TurnID
	laterTurnID := appendSyntheticLatestContentTurnForFrontierTest(
		t,
		ctx,
		fixture,
		"completed_tool_calls_later_turn",
		now.Add(time.Second),
	)
	if laterTurnID == historicalTurnID {
		t.Fatalf("later turn = historical tool turn %s", historicalTurnID)
	}
	laterWatermark, err := fixture.Store.Execution().MaxEventSequence(
		ctx,
		testProjectID,
		fixture.AgentID,
	)
	if err != nil {
		t.Fatalf("load later-turn watermark: %v", err)
	}
	acrossTurns, err := fixture.Store.Execution().ListCompletedToolCallsAtWatermark(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		laterWatermark,
	)
	if err != nil {
		t.Fatalf("list completed tool calls from a later turn: %v", err)
	}
	if len(acrossTurns) != 1 || acrossTurns[0].ID != toolCallID ||
		acrossTurns[0].TurnID != historicalTurnID {
		t.Fatalf(
			"later-turn model projection = %+v, want historical tool %s from turn %s",
			acrossTurns,
			toolCallID,
			historicalTurnID,
		)
	}
}

func TestSemanticContextIdentityPreventsLaterOutputAtStaleFrontier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "continuation_seed_later_unseen_result")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "continuation_seed_later_unseen_result", "read_process")
	modelContextID := modelContextIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	initialContext, found, err := fixture.Store.Execution().GetModelCallContext(ctx, testProjectID, fixture.AgentID, modelContextID)
	if err != nil {
		t.Fatalf("load initial model context: %v", err)
	}
	if !found {
		t.Fatalf("initial model context %s not found", modelContextID)
	}
	completed, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"result not yet covered"}]`),
	})
	if err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	resultEvents := listTypedToolResultEventsForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)
	if len(resultEvents) != 1 {
		t.Fatalf("tool result events = %+v, want one", resultEvents)
	}
	openingInputID, _ := openingInputAndWatermarkForProcessToolCallTest(t, ctx, fixture, toolCallID)
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	_, err = fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    []ID{openingInputID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: initialContext.InputEventSequence,
	})
	if !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("stale semantic model context claim error = %v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}
	seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("next continuation seed: %v", err)
	}
	if !found || seed.TurnID != completed.TurnID || len(seed.InputIDs) != 1 || seed.InputIDs[0] != openingInputID {
		t.Fatalf(
			"continuation seed found=%v seed=%+v, want turn %s input %s",
			found,
			seed,
			completed.TurnID,
			openingInputID,
		)
	}
}

func TestLatestTurnFrontierHelpersIgnoreHistoricalTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("active model context", func(t *testing.T) {
		fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "latest_frontier_active_context")
		contextRecord := createContextForAdmittedTurnTest(t, ctx, fixture, admitted, agent, "latest-frontier-active-context", fixture.Now.Add(4*time.Second))
		assertFrontierCountTest(t, ctx, fixture.Store, "active context before newer turn", `SELECT count(*) FROM agent_continuable_model_contexts($1, $2) WHERE model_call_context_id = $3 AND NOT has_later_semantic_event`, fixture.AgentID, contextRecord.ID, 1)

		appendSyntheticLatestContentTurnForFrontierTest(t, ctx, fixture, "latest_frontier_active_context_newer", fixture.Now.Add(5*time.Second))

		assertFrontierCountTest(t, ctx, fixture.Store, "active context after newer turn", `SELECT count(*) FROM agent_continuable_model_contexts($1, $2) WHERE model_call_context_id = $3 AND NOT has_later_semantic_event`, fixture.AgentID, contextRecord.ID, 0)
	})

	t.Run("pending built-in tool", func(t *testing.T) {
		fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "latest_frontier_pending_tool")
		contextRecord := createContextForAdmittedTurnTest(t, ctx, fixture, admitted, agent, "latest-frontier-pending-tool", fixture.Now.Add(4*time.Second))
		recordToolCallBatchForContextTest(
			t,
			ctx,
			fixture,
			contextRecord.ID,
			"latest-frontier-pending-tool",
			[]toolCallForContextTest{{
				ProviderCallID: "call_latest_frontier_pending_tool",
				Name:           "read_process",
				Input:          json.RawMessage(`{}`),
			}},
			fixture.Now.Add(5*time.Second),
		)
		assertFrontierCountTest(t, ctx, fixture.Store, "pending tool before newer turn", `SELECT count(*) FROM agent_tool_work_frontiers($1, $2) WHERE model_call_context_id = $3`, fixture.AgentID, contextRecord.ID, 1)

		appendSyntheticLatestContentTurnForFrontierTest(t, ctx, fixture, "latest_frontier_pending_tool_newer", fixture.Now.Add(9*time.Second))

		assertFrontierCountTest(t, ctx, fixture.Store, "pending tool after newer turn", `SELECT count(*) FROM agent_tool_work_frontiers($1, $2) WHERE model_call_context_id = $3`, fixture.AgentID, contextRecord.ID, 0)
	})

	t.Run("completed tool result", func(t *testing.T) {
		fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "latest_frontier_completed_tool")
		contextRecord := createContextForAdmittedTurnTest(t, ctx, fixture, admitted, agent, "latest-frontier-completed-tool", fixture.Now.Add(4*time.Second))
		completeToolCallForContinuationSeedTest(t, ctx, fixture, admitted.Turn.ID, contextRecord.ID, "latest_frontier_completed_tool", fixture.Now.Add(5*time.Second))
		assertFrontierCountTest(t, ctx, fixture.Store, "completed tool before newer turn", `SELECT count(*) FROM agent_model_result_frontiers($1, $2) WHERE model_call_context_id = $3`, fixture.AgentID, contextRecord.ID, 1)

		appendSyntheticLatestContentTurnForFrontierTest(t, ctx, fixture, "latest_frontier_completed_tool_newer", fixture.Now.Add(7*time.Second))

		assertFrontierCountTest(t, ctx, fixture.Store, "completed tool after newer turn", `SELECT count(*) FROM agent_model_result_frontiers($1, $2) WHERE model_call_context_id = $3`, fixture.AgentID, contextRecord.ID, 0)
	})

	t.Run("incomplete tool batch", func(t *testing.T) {
		fixture := newProcessDaemonFixture(t, ctx, "latest_frontier_running_barrier")
		toolCallID := createToolCallForProcessTest(t, ctx, fixture, "latest_frontier_running_barrier", "read_process")
		claimToolCallForTest(
			t,
			ctx,
			fixture.Store,
			fixture.AgentID,
			toolCallID,
			fixture.Lock.ID,
			true,
		)
		var incomplete bool
		if err := fixture.Store.pool.QueryRow(
			ctx,
			`SELECT agent_has_incomplete_tool_batch($1, $2)`,
			testProjectID,
			fixture.AgentID,
		).Scan(&incomplete); err != nil || !incomplete {
			t.Fatalf("incomplete batch before newer turn = %v, err = %v", incomplete, err)
		}

		appendSyntheticLatestContentTurnForFrontierTest(t, ctx, fixture, "latest_frontier_running_barrier_newer", fixture.Now.Add(time.Minute+time.Second))

		if err := fixture.Store.pool.QueryRow(
			ctx,
			`SELECT agent_has_incomplete_tool_batch($1, $2)`,
			testProjectID,
			fixture.AgentID,
		).Scan(&incomplete); err != nil || incomplete {
			t.Fatalf("incomplete batch after newer turn = %v, err = %v", incomplete, err)
		}
	})
}

func TestDatabaseRejectsCompletedToolCallWithoutTypedResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"continuation_seed_missing_sibling_result",
	)
	now := fixture.Now.Add(time.Minute)
	contextRecord := createContextForAdmittedTurnTest(
		t,
		ctx,
		fixture,
		admitted,
		agent,
		"continuation-seed-missing-sibling-result",
		now,
	)
	_, toolCalls := recordToolCallBatchForContextTest(
		t,
		ctx,
		fixture,
		contextRecord.ID,
		"continuation-seed-missing-sibling-result",
		[]toolCallForContextTest{
			{
				ProviderCallID: "call_continuation_seed_missing_sibling_result_1",
				Name:           "read_process",
				Input:          json.RawMessage(`{}`),
			},
			{
				ProviderCallID: "call_continuation_seed_missing_sibling_result_2",
				Name:           "read_process",
				Input:          json.RawMessage(`{}`),
			},
		},
		now.Add(time.Millisecond),
	)
	if len(toolCalls) != 2 {
		t.Fatalf("tool call batch = %+v, want two calls", toolCalls)
	}
	firstToolCallID := toolCalls[0].ID
	secondToolCall := toolCalls[1]
	markToolCallReadyForTest(t, ctx, fixture, firstToolCallID, now.Add(2*time.Millisecond))
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 firstToolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"first done"}]`),
	}); err != nil {
		t.Fatalf("complete first tool call: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE tool_calls
SET state = 'completed',
    runtime_lock_id = NULL
WHERE agent_id = $1
  AND id = $2
`, fixture.AgentID, secondToolCall.ID); err == nil ||
		!strings.Contains(err.Error(), "completion state must match result existence") {
		t.Fatalf("complete sibling without typed result error = %v", err)
	}
	if seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("next continuation seed: %v", err)
	} else if found {
		t.Fatalf("continuation seed = %+v, want none while sibling tool remains open", seed)
	}
}

func TestContinuationSeedDoesNotResumeStoppedIncompleteBatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"continuation_seed_stopped_sibling_result",
	)
	now := fixture.Now.Add(time.Minute)
	contextRecord := createContextForAdmittedTurnTest(
		t,
		ctx,
		fixture,
		admitted,
		agent,
		"continuation-seed-stopped-sibling-result",
		now,
	)
	_, toolCalls := recordToolCallBatchForContextTest(
		t,
		ctx,
		fixture,
		contextRecord.ID,
		"continuation-seed-stopped-sibling-result",
		[]toolCallForContextTest{
			{
				ProviderCallID: "call_continuation_seed_stopped_sibling_open",
				Name:           "read_process",
				Input:          json.RawMessage(`{}`),
			},
			{
				ProviderCallID: "call_continuation_seed_stopped_sibling_bad",
				Name:           "read_process",
				Input:          json.RawMessage(`{}`),
			},
		},
		now.Add(time.Millisecond),
	)
	if len(toolCalls) != 2 {
		t.Fatalf("tool call batch = %+v, want two calls", toolCalls)
	}
	turnID := admitted.Turn.ID
	appendCancelStopEventForContinuationSeedTest(t, ctx, fixture, turnID, now.Add(2*time.Second))
	if seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("next continuation seed: %v", err)
	} else if found {
		t.Fatalf("continuation seed = %+v, want none after the turn was stopped", seed)
	}
}

func TestContinuationSeedKeepsLaterToolResultAfterEarlierResultConsumed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "continuation_seed_later_result")
	firstContext := claimNormalContextAtFrontierTest(
		t, ctx, fixture, []ID{admitted.Inputs[0].ID, admitted.Inputs[1].ID}, agent.CurrentConfigID,
		admitted.Events[1].Sequence, fixture.Now.Add(4*time.Second),
	)
	firstCompleted := completeToolCallForContinuationSeedTest(t, ctx, fixture, admitted.Turn.ID, firstContext.ID, "continuation_seed_later_result_first", fixture.Now.Add(5*time.Second))
	firstResultEvents := listTypedToolResultEventsForToolCall(t, ctx, fixture.Store, fixture.AgentID, firstCompleted.ID)
	if len(firstResultEvents) != 1 {
		t.Fatalf("first tool result events = %d, want 1", len(firstResultEvents))
	}
	laterContext := claimNormalContextAtFrontierTest(
		t, ctx, fixture, []ID{admitted.Inputs[0].ID, admitted.Inputs[1].ID}, agent.CurrentConfigID,
		firstResultEvents[0].Sequence, fixture.Now.Add(7*time.Second),
	)
	completed := completeToolCallForContinuationSeedTest(t, ctx, fixture, admitted.Turn.ID, laterContext.ID, "continuation_seed_later_result_second", fixture.Now.Add(8*time.Second))
	if seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("next continuation seed: %v", err)
	} else if !found || seed.TurnID != completed.TurnID || len(seed.InputIDs) != 2 ||
		seed.InputIDs[0] != admitted.Inputs[0].ID || seed.InputIDs[1] != admitted.Inputs[1].ID {
		t.Fatalf("continuation seed found=%v seed=%+v, want turn %s inputs [%s %s]", found, seed, completed.TurnID, admitted.Inputs[0].ID, admitted.Inputs[1].ID)
	}
}

func TestNextAgentContinuationSeedUsesOpeningInputsAtContextWatermark(t *testing.T) {
	t.Parallel()
	t.Run("orders multi-input opening events", func(t *testing.T) {
		ctx := context.Background()
		fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "continuation_seed_multi_input")
		contextRecord := claimNormalContextAtFrontierTest(
			t, ctx, fixture, []ID{admitted.Inputs[0].ID, admitted.Inputs[1].ID}, agent.CurrentConfigID,
			admitted.Events[1].Sequence, fixture.Now.Add(4*time.Second),
		)
		completed := completeToolCallForContinuationSeedTest(
			t,
			ctx,
			fixture,
			admitted.Turn.ID,
			contextRecord.ID,
			"continuation_seed_multi_input",
			fixture.Now.Add(5*time.Second),
		)
		seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID)
		if err != nil {
			t.Fatalf("next continuation seed: %v", err)
		}
		if !found || seed.TurnID != completed.TurnID {
			t.Fatalf("continuation seed found=%v seed=%+v want turn %s", found, seed, completed.TurnID)
		}
		if len(seed.InputIDs) != 2 || seed.InputIDs[0] != admitted.Inputs[0].ID ||
			seed.InputIDs[1] != admitted.Inputs[1].ID {
			t.Fatalf("seed input order = %v, want [%s %s]", seed.InputIDs, admitted.Inputs[0].ID, admitted.Inputs[1].ID)
		}
	})

	t.Run("rejects stale opening frontier", func(t *testing.T) {
		ctx := context.Background()
		fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "continuation_seed_watermark")
		_, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			OpeningInputIDs:    []ID{admitted.Inputs[0].ID},
			AgentConfigID:      agent.CurrentConfigID,
			InputEventSequence: admitted.Events[0].Sequence,
		})
		if !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
			t.Fatalf("stale opening frontier claim error = %v, want %v", err, storeerr.ErrAgentNotAdvanceable)
		}
	})
}

func TestClaimNormalModelCallFencesContinuationToSelectedSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"continuation_source_fence",
	)
	sourceContext := claimNormalContextAtFrontierTest(
		t,
		ctx,
		fixture,
		[]ID{admitted.Inputs[0].ID, admitted.Inputs[1].ID},
		agent.CurrentConfigID,
		admitted.Events[1].Sequence,
		fixture.Now.Add(4*time.Second),
	)
	completeToolCallForContinuationSeedTest(
		t,
		ctx,
		fixture,
		admitted.Turn.ID,
		sourceContext.ID,
		"continuation_source_fence",
		fixture.Now.Add(5*time.Second),
	)
	sourceOutput, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		sourceContext.ID,
	)
	if err != nil || !found {
		t.Fatalf("load continuation source output found=%v err=%v", found, err)
	}
	seed, found, err := fixture.Store.Execution().NextAgentModelWork(
		ctx,
		testProjectID,
		fixture.AgentID,
	)
	if err != nil || !found {
		t.Fatalf("load selected continuation work found=%v err=%v", found, err)
	}
	if seed.Kind != executionstore.ModelWorkContinue ||
		seed.ModelCallContextID != sourceContext.ID ||
		seed.SourceModelOutputID != sourceOutput.ID {
		t.Fatalf(
			"selected continuation source = kind %s context %s output %s, want continue/%s/%s",
			seed.Kind,
			seed.ModelCallContextID,
			seed.SourceModelOutputID,
			sourceContext.ID,
			sourceOutput.ID,
		)
	}
	frontier, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load continuation frontier: %v", err)
	}
	claim := func(sourceContextID, sourceOutputID ID, now time.Time) (executionstore.ModelCallClaim, error) {
		return fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
			ProjectID:                testProjectID,
			AgentID:                  fixture.AgentID,
			RuntimeLockID:            fixture.Lock.ID,
			OpeningInputIDs:          seed.InputIDs,
			AgentConfigID:            agent.CurrentConfigID,
			InputEventSequence:       frontier,
			SourceModelCallContextID: sourceContextID,
			SourceModelOutputID:      sourceOutputID,
		})
	}

	if _, err := claim(NilID, NilID, fixture.Now.Add(8*time.Second)); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("continuation claim without source error = %v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}
	if _, err := claim(
		testID("wrong_continuation_source_context"),
		sourceOutput.ID,
		fixture.Now.Add(9*time.Second),
	); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("continuation claim with wrong source context error = %v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}
	if _, err := claim(
		sourceContext.ID,
		testID("wrong_continuation_source_output"),
		fixture.Now.Add(10*time.Second),
	); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("continuation claim with wrong source output error = %v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}

	created, err := claim(sourceContext.ID, sourceOutput.ID, fixture.Now.Add(11*time.Second))
	if err != nil {
		t.Fatalf("claim selected continuation source: %v", err)
	}
	if !created.Created || !created.Claimed ||
		created.Context.InputEventSequence != frontier {
		t.Fatalf("selected continuation claim = %+v", created)
	}
	replayed, err := claim(sourceContext.ID, sourceOutput.ID, fixture.Now.Add(12*time.Second))
	if err != nil {
		t.Fatalf("replay selected continuation claim: %v", err)
	}
	if replayed.Context.ID != created.Context.ID ||
		replayed.Created {
		t.Fatalf("replayed continuation claim = %+v, want existing context", replayed)
	}
}

func TestKernelContextEventsIncludesCanonicalTranscriptEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_context_events_content_only")
	historicalToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_context_events_historical_tool",
		"read_process",
	)
	historicalTurnID := turnIDForProcessToolCallTest(
		t,
		ctx,
		fixture,
		historicalToolCallID,
	)
	historicalContinuationID := modelOutputContextForTurnTest(
		t,
		ctx,
		fixture,
		historicalTurnID,
		"kernel_context_events_historical_continuation",
		fixture.Now.Add(30*time.Second),
	)
	createModelOutputEventForTurnTest(
		t,
		ctx,
		fixture,
		historicalTurnID,
		historicalContinuationID,
		"kernel_context_events_historical_end",
		string(modelenvelope.StopReasonEndTurn),
		"",
		fixture.Now.Add(31*time.Second),
	)

	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"visible"}]`),
		IdempotencyKey: "kernel-context-visible-input",
	})
	if err != nil {
		t.Fatalf("create visible agent input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found {
		t.Fatal("expected admitted visible agent input")
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for visible model output: %v", err)
	}
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    []ID{input.ID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: admitted.Events[0].Sequence,
	})
	if err != nil {
		t.Fatalf("claim visible model output context: %v", err)
	}
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		claim.Context.ID,
	)
	providerReplay := json.RawMessage(
		`[{"type":"message","role":"assistant","content":"assistant visible"}]`,
	)
	if _, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx, executionstore.RecordModelOutputAndCompleteContextInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		ModelCallContextID: claim.Context.ID,
		ProviderResponse: modelenvelope.ResponseEnvelope{
			RequestedProviderModelSlug: providerModelSlug,
			ServedProviderModelSlug:    providerModelSlug,
			APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
			APIVariant:                 modelprotocol.APIVariantDefault,
			ProviderReplay:             providerReplay,
			Normalized: modelenvelope.ResponseNormalized{
				ID: "resp_kernel_context_events_content_only",
				Content: []modelenvelope.ResponsePart{
					{Type: "reasoning", Text: "assistant hidden reasoning"},
					{Type: "text", Text: "assistant visible"},
				},
				StopReason: modelenvelope.StopReasonEndTurn,
			},
		},
	}); err != nil {
		t.Fatalf("record visible model output: %v", err)
	}

	watermark, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("max event sequence: %v", err)
	}
	contextEvents, err := fixture.Store.Execution().ListContextEvents(ctx, testProjectID, fixture.AgentID, 0, watermark, 100)
	if err != nil {
		t.Fatalf("list context events: %v", err)
	}
	var foundConfigChange, foundVisibleInput, foundHistoricalTool, foundVisibleOutput bool
	for _, event := range contextEvents {
		content := string(event.ContentParts)
		if event.Sequence == 1 && strings.Contains(content, "Agent configuration changed") {
			t.Fatalf("baseline config change should not be projected into model context: %+v", event)
		}
		if event.Role == modelprotocol.RoleUser && strings.Contains(content, "Agent configuration changed") {
			foundConfigChange = true
		}
		if event.SourceEventID == admitted.Events[0].ID &&
			admitted.Inputs[0].ID == input.ID &&
			strings.Contains(content, "visible") {
			foundVisibleInput = true
		}
		if event.Role == modelprotocol.RoleAssistant &&
			strings.Contains(content, historicalToolCallID.String()) {
			foundHistoricalTool = true
		}
		if event.Role == modelprotocol.RoleAssistant &&
			strings.Contains(content, "assistant visible") {
			foundVisibleOutput = sameJSON(event.ProviderReplay, providerReplay) &&
				event.ModelProviderConfigID != (executionstore.ID{}) &&
				event.RequestedModelSlug == providerModelSlug &&
				event.APIFormat == modelprotocol.APIFormatOpenAIResponses &&
				event.APIVariant == modelprotocol.APIVariantDefault
		}
	}
	if !foundConfigChange || !foundVisibleInput || !foundHistoricalTool || !foundVisibleOutput {
		t.Fatalf(
			"canonical context events missing required history: config=%v input=%v historical_tool=%v output_with_replay=%v events=%+v",
			foundConfigChange,
			foundVisibleInput,
			foundHistoricalTool,
			foundVisibleOutput,
			contextEvents,
		)
	}

	compactionEvents, err := fixture.Store.Execution().ListCompactionSourceEvents(ctx, testProjectID, fixture.AgentID, 0, 100)
	if err != nil {
		t.Fatalf("list compaction source events: %v", err)
	}
	for _, event := range compactionEvents {
		if event.Sequence == 1 && strings.Contains(string(event.ContentParts), "Agent configuration changed") {
			t.Fatalf("baseline config change should not be projected into compaction source: %+v", event)
		}
	}
}

func TestCompactionSourceEventsIncludeToolCallAndResultSemantics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"compaction_source_tool_semantics",
	)
	now := fixture.Now.Add(time.Minute)
	contextRecord := createContextForAdmittedTurnTest(
		t,
		ctx,
		fixture,
		admitted,
		agent,
		"compaction-source-tool-semantics",
		now,
	)
	_, toolCalls := recordToolCallBatchForContextTest(
		t,
		ctx,
		fixture,
		contextRecord.ID,
		"compaction-source-tool-semantics",
		[]toolCallForContextTest{
			{
				ProviderCallID: "call_compaction_source_tool_semantics",
				Name:           "run_command",
				Input:          json.RawMessage(`{}`),
			},
			{
				ProviderCallID: "call_compaction_source_tool_semantics_sibling",
				Name:           "read_process",
				Input:          json.RawMessage(`{}`),
			},
		},
		now.Add(time.Millisecond),
	)
	if len(toolCalls) != 2 {
		t.Fatalf("tool call batch = %+v, want two calls", toolCalls)
	}
	toolCallID := toolCalls[0].ID
	siblingToolCall := toolCalls[1]
	markToolCallReadyForTest(t, ctx, fixture, toolCallID, now.Add(2*time.Millisecond))
	markToolCallReadyForTest(t, ctx, fixture, siblingToolCall.ID, now.Add(3*time.Millisecond))
	openRows, err := fixture.Store.Execution().ListCompactionSourceEvents(ctx, testProjectID, fixture.AgentID, 0, 100)
	if err != nil {
		t.Fatalf("list open tool compaction source events: %v", err)
	}
	var openToolCallSequence int64
	for _, row := range openRows {
		if row.Kind == string(events.KindModelOutput) && strings.Contains(string(row.ContentParts), `"name": "run_command"`) {
			openToolCallSequence = row.Sequence
		}
	}
	if openToolCallSequence == 0 {
		t.Fatalf("open tool call source missing from compaction events: %+v", openRows)
	}
	openGroups, err := fixture.Store.Execution().ListCompactionAtomicGroups(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		openToolCallSequence,
	)
	if err != nil {
		t.Fatalf("list open tool compaction atomic groups: %v", err)
	}
	requireCompactionAtomicGroup(
		t,
		openGroups,
		"tool_call_result",
		openToolCallSequence,
		openToolCallSequence+1,
	)
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"command output visible to model"},{"type":"structured_data","value":{"exit_code":0}}]`),
	}); err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	partialRows, err := fixture.Store.Execution().ListCompactionSourceEvents(ctx, testProjectID, fixture.AgentID, 0, 100)
	if err != nil {
		t.Fatalf("list partially completed tool compaction source events: %v", err)
	}
	partialGroups, err := fixture.Store.Execution().ListCompactionAtomicGroups(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		partialRows[len(partialRows)-1].Sequence,
	)
	if err != nil {
		t.Fatalf("list partially completed tool compaction atomic groups: %v", err)
	}
	requireCompactionAtomicGroup(
		t,
		partialGroups,
		"tool_call_result",
		openToolCallSequence,
		partialRows[len(partialRows)-1].Sequence+1,
	)
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 siblingToolCall.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"sibling output visible to model"}]`),
	}); err != nil {
		t.Fatalf("complete sibling tool call: %v", err)
	}
	rows, err := fixture.Store.Execution().ListCompactionSourceEvents(ctx, testProjectID, fixture.AgentID, 0, 100)
	if err != nil {
		t.Fatalf("list compaction source events: %v", err)
	}
	var sawToolCall, sawToolResult bool
	var toolCallSequence, toolResultSequence int64
	for _, row := range rows {
		body := string(row.ContentParts)
		switch row.Kind {
		case string(events.KindModelOutput):
			if strings.Contains(body, `"type": "tool_call"`) &&
				strings.Contains(body, `"name": "run_command"`) &&
				strings.Contains(body, `"input": {}`) {
				sawToolCall = true
				toolCallSequence = row.Sequence
			}
		case string(events.KindToolResult):
			toolResultSequence = max(toolResultSequence, row.Sequence)
			if row.ToolName == "run_command" &&
				strings.Contains(body, "command output visible to model") &&
				strings.Contains(body, `"exit_code": 0`) {
				sawToolResult = true
			}
		}
	}
	if !sawToolCall || !sawToolResult {
		t.Fatalf(
			"compaction source rows lost tool semantics saw_call=%v saw_result=%v rows=%+v",
			sawToolCall,
			sawToolResult,
			rows,
		)
	}
	groups, err := fixture.Store.Execution().ListCompactionAtomicGroups(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		rows[len(rows)-1].Sequence,
	)
	if err != nil {
		t.Fatalf("list compaction atomic groups: %v", err)
	}
	requireCompactionAtomicGroup(
		t,
		groups,
		"tool_call_result",
		toolCallSequence,
		toolResultSequence,
	)
}

func requireCompactionAtomicGroup(
	t *testing.T,
	groups []executionstore.CompactionAtomicGroupRecord,
	kind string,
	startSequence, endSequence int64,
) {
	t.Helper()
	for _, group := range groups {
		if group.Kind == kind &&
			group.StartSequence == startSequence &&
			group.EndSequence == endSequence {
			return
		}
	}
	t.Fatalf(
		"compaction atomic groups = %+v, want %s range %d..%d",
		groups,
		kind,
		startSequence,
		endSequence,
	)
}

func TestCompactionAtomicGroupsKeepMultiInputTurnOpeningTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"compaction_multi_input_opening_group",
	)
	if len(admitted.Events) != 2 {
		t.Fatalf("opening events = %+v, want two", admitted.Events)
	}
	rows, err := fixture.Store.Execution().ListCompactionSourceEvents(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		100,
	)
	if err != nil {
		t.Fatalf("list compaction source events: %v", err)
	}
	openingRows := make([]executionstore.CompactionSourceEventRecord, 0, 2)
	for _, row := range rows {
		if row.TurnID == admitted.Turn.ID && row.IsOpeningEvent {
			openingRows = append(openingRows, row)
		}
	}
	if len(openingRows) != 2 || openingRows[0].Sequence != admitted.Events[0].Sequence ||
		openingRows[1].Sequence != admitted.Events[1].Sequence {
		t.Fatalf("projected opening rows = %+v, admitted=%+v", openingRows, admitted.Events)
	}

	groups, err := fixture.Store.Execution().ListCompactionAtomicGroups(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		admitted.Events[1].Sequence,
	)
	if err != nil {
		t.Fatalf("list compaction atomic groups: %v", err)
	}
	for _, group := range groups {
		if group.Kind != "turn_opening" {
			continue
		}
		if group.StartSequence != admitted.Events[0].Sequence ||
			group.EndSequence != admitted.Events[1].Sequence {
			t.Fatalf("turn opening group = %+v, admitted=%+v", group, admitted.Events)
		}
		return
	}
	t.Fatalf("turn opening group missing from %+v", groups)
}

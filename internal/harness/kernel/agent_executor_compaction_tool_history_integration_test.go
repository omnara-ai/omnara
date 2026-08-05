//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
)

func TestAgentExecutorCompactionAbsorbsCompletedHistoricalToolGroup(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(
		t,
		ctx,
		"openai/kernel-tool-history",
		fixture.Now,
		kernelConfiguredModelOptions{
			ContextWindowTokens: intPtrForKernelCompactionTest(1600),
			MaxOutputTokens:     intPtrForKernelCompactionTest(64),
		},
		"run_command",
	)

	const (
		oldInputNeedle = "HISTORICAL_TOOL_INPUT_8A2C"
		commandNeedle  = "COMPACTED_HISTORICAL_TOOL_COMMAND_4D91"
		oldFinalOutput = "historical tool turn finished"
		summaryText    = "The earlier completed tool operation was summarized."
		currentInput   = "continue after compacting the completed historical tool operation"
		finalOutput    = "continued without replaying compacted raw tool history"
	)
	oldInput := oldInputNeedle + " " + strings.Repeat("old compactable detail ", 120)
	oldModel := &sequenceKernelModel{
		providerModelSlug: "kernel-tool-history",
		capabilities: model.Capabilities{
			ContextWindowTokens: 10000,
			MaxOutputTokens:     64,
		},
		responses: []model.Response{
			{
				ID:         "resp_historical_tool_call",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
					ID:    "call_historical_tool_for_compaction",
					Name:  "run_command",
					Input: json.RawMessage(`{"command":"printf '` + commandNeedle + `\n'"}`),
				}}),
			},
			{
				ID:         "resp_historical_tool_complete",
				Content:    []model.ResponsePart{{Type: "text", Text: oldFinalOutput}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	oldTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		oldInput,
		fixture.Now.Add(time.Second),
	)
	oldExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, oldModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := oldExecutor.ExecuteModelWork(ctx, oldTurn); err != nil {
		t.Fatalf("execute historical tool call: %v", err)
	}
	scope := executeNextToolWork(t, ctx, fixture, oldExecutor, oldTurn)
	select {
	case <-scope.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("historical tool execution did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("execute historical tool: %v", err)
	}
	oldContinuation := executeNextModelWork(t, ctx, fixture, oldExecutor, oldTurn)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		oldContinuation.ProjectID,
		oldContinuation.AgentID,
		oldContinuation.RuntimeLockID,
	); err != nil {
		t.Fatalf("release historical tool turn runtime: %v", err)
	}

	compactingModel := &sequenceKernelModel{
		providerModelSlug: "kernel-tool-history",
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if bundle.ContextCheckpoint != nil || isCompactionRequestBundle(bundle) {
				return 500
			}
			return 2_000
		},
		capabilities: model.Capabilities{
			ContextWindowTokens: 1600,
			MaxOutputTokens:     64,
		},
		responses: []model.Response{
			{
				ID:         "resp_historical_tool_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: summaryText}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_after_historical_tool_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: finalOutput}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	currentTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		currentInput,
		fixture.Now.Add(8*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, compactingModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(9 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, currentTurn); err != nil {
		t.Fatalf("execute historical-tool compaction: %v", err)
	}
	if compactingModel.respondedCount() != 1 {
		t.Fatalf(
			"historical-tool compaction provider calls = %d, want summary call",
			compactingModel.respondedCount(),
		)
	}
	finalTurn := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		fixture,
		currentTurn,
		fixture.Now.Add(10*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, finalTurn); err != nil {
		t.Fatalf("execute post-compaction historical-tool turn: %v", err)
	}
	if compactingModel.respondedCount() != 2 {
		t.Fatalf(
			"historical-tool compaction provider calls = %d, want summary and final calls",
			compactingModel.respondedCount(),
		)
	}

	summaryRequest := string(compactingModel.responded[0].ProviderRequest)
	for _, needle := range []string{
		oldInputNeedle,
		commandNeedle,
		"no_active_agent_machine_binding",
		oldFinalOutput,
	} {
		if !strings.Contains(summaryRequest, needle) {
			t.Fatalf("compaction source omitted completed tool history %q: %s", needle, summaryRequest)
		}
	}
	if strings.Contains(summaryRequest, currentInput) {
		t.Fatalf("compaction source included the still-unanswered current input: %s", summaryRequest)
	}
	retryRequest := string(compactingModel.responded[1].ProviderRequest)
	if !strings.Contains(retryRequest, summaryText) ||
		!strings.Contains(retryRequest, currentInput) {
		t.Fatalf("post-compaction request omitted summary or current input: %s", retryRequest)
	}
	for _, rawNeedle := range []string{
		oldInputNeedle,
		commandNeedle,
		"no_active_agent_machine_binding",
		oldFinalOutput,
	} {
		if strings.Contains(retryRequest, rawNeedle) {
			t.Fatalf(
				"post-compaction request duplicated raw historical tool content %q: %s",
				rawNeedle,
				retryRequest,
			)
		}
	}

	var summarizedThrough, oldFinalSequence, currentInputSequence int64
	if err := fixture.Pool.QueryRow(ctx, `
SELECT checkpoint.summarized_through_event_sequence
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
`, currentTurn.ProjectID, agentID).Scan(&summarizedThrough); err != nil {
		t.Fatalf("load historical-tool checkpoint: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT max(event.sequence)
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.turn_id = $3
  AND event.event_kind = 'model_output'
`, oldTurn.ProjectID, agentID, oldTurn.TurnID).Scan(&oldFinalSequence); err != nil {
		t.Fatalf("load historical tool turn final sequence: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT event.sequence
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.agent_input_id = $3
`, currentTurn.ProjectID, agentID, currentTurn.InputIDs[0]).Scan(&currentInputSequence); err != nil {
		t.Fatalf("load current input sequence: %v", err)
	}
	if summarizedThrough != oldFinalSequence || summarizedThrough >= currentInputSequence {
		t.Fatalf(
			"checkpoint summarized through %d, want completed tool turn %d before current input %d",
			summarizedThrough,
			oldFinalSequence,
			currentInputSequence,
		)
	}

	var canonicalToolCalls, canonicalToolResults int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection
WHERE project_id = $1
  AND agent_id = $2
  AND provider_call_id = 'call_historical_tool_for_compaction'
`, currentTurn.ProjectID, agentID).Scan(&canonicalToolCalls); err != nil {
		t.Fatalf("count canonical historical tool calls: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
JOIN tool_call_results result
  ON result.agent_id = event.agent_id
 AND result.id = event.tool_call_result_id
JOIN tool_calls call
  ON call.agent_id = result.agent_id
 AND call.id = result.tool_call_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'tool_result'
  AND call.provider_call_id = 'call_historical_tool_for_compaction'
`, currentTurn.ProjectID, agentID).Scan(&canonicalToolResults); err != nil {
		t.Fatalf("count canonical historical tool results: %v", err)
	}
	if canonicalToolCalls != 1 || canonicalToolResults != 1 {
		t.Fatalf(
			"compaction mutated canonical tool history calls=%d results=%d, want 1/1",
			canonicalToolCalls,
			canonicalToolResults,
		)
	}
}

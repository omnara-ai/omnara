package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestRunnerOptionalFailureResumesNormalAfterOneProviderAttempt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result summaryResult
	}{
		{"overflow", summaryResult{err: model.ProviderError{Kind: model.ErrorKindContextWindow, Code: "too_big"}}},
		{"timeout", summaryResult{err: model.ProviderError{Kind: model.ErrorKindTransient, Code: "timeout"}}},
		{"refusal", summaryResult{response: model.Response{StopReason: model.StopReasonRefusal}}},
		{"invalid parsed response", summaryResult{response: model.Response{StopReason: model.StopReasonEndTurn,
			Content: []model.ResponsePart{{Type: "unexpected"}}}}},
		{"truncated", summaryResult{response: model.Response{StopReason: model.StopReasonMaxTokens,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial"}}}}},
		{"not reduced", summaryResult{response: completeSummaryResponse(strings.Repeat("expanded summary ", 1_000))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{parentRecoveryKind: executionstore.ModelCallRecoveryCompactOptional,
				events: []executionstore.CompactionSourceEventRecord{
					textCompactionEvent(1, strings.Repeat("old first observation ", 60)),
					textCompactionEvent(2, strings.Repeat("old second observation ", 60)),
				}}
			client := &summaryModel{results: []summaryResult{tc.result}}
			compactionInput := runInput(testPlan(1, 2, 3))
			result, err := testRunner(store, client).
				RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
			require.NoError(t, err)
			require.Equal(t, RunResumeNormal, result.State)
			require.Len(t, client.requests, 1)
			require.Len(t, store.resumedFailures, 1)
			require.Empty(t, store.replacements)
			require.Empty(t, store.retryFailures)
			require.Empty(t, store.terminalFailures)
			require.Empty(t, store.publishInputs)
		})
	}
}

func TestRunnerOptionalSourceLoadFailureResumesNormalWithOriginalCause(t *testing.T) {
	store := &fakeStore{
		parentRecoveryKind: executionstore.ModelCallRecoveryCompactOptional,
		atomicGroupsErr:    errors.New("source database unavailable"),
		events:             []executionstore.CompactionSourceEventRecord{textCompactionEvent(1, "closed history")},
	}
	client := &summaryModel{}
	input := runInput(testPlan(1, 1, 2))
	result, err := testRunner(store, client).RunClaimed(t.Context(), input, store.addStartedClaim(input))
	require.NoError(t, err)
	require.Equal(t, RunResumeNormal, result.State)
	require.Empty(t, client.requests)
	require.Len(t, store.resumedFailures, 1)
	require.Equal(t, compactionErrorCodeLoadSourceFailed, store.resumedFailures[0].ErrorCode)
	require.Contains(t, string(store.resumedFailures[0].ErrorDetails), store.atomicGroupsErr.Error())
}

func TestRunnerExcerptRecoveryMeasuresReductionAgainstReplacedHistory(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		acceptedExcerptBytes int
		summary              string
	}{
		{"omission notice at excerpt floor", minimumSourceExcerptBytes, strings.Repeat("state ", 58)},
		{"summary larger than excerpt", initialSourceExcerptBytes, strings.Repeat("Preserve verified context. ", 1800)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
				textCompactionEvent(1, strings.Repeat("old closed history ", 12000)),
			}}
			input := runInput(testPlan(1, 1, 2))
			client := &summaryModel{
				caps: model.Capabilities{ContextWindowTokens: 32768, MaxOutputTokens: new(16384), DefaultMaxOutputTokens: 16384},
				respond: func(request model.Request) (model.Response, error) {
					if strings.Count(string(request.ProviderRequest), "old closed history ") >
						tc.acceptedExcerptBytes/len("old closed history ") {
						return model.Response{}, model.ProviderError{Kind: model.ErrorKindContextWindow}
					}
					return completeSummaryResponse(tc.summary), nil
				},
			}
			result, err := testRunner(store, client).RunClaimed(t.Context(), input, store.addStartedClaim(input))
			require.NoError(t, err)
			require.Equal(t, RunCompleted, result.State)
			require.Greater(t, len(client.requests), 1)
			require.Empty(t, store.terminalFailures)
			require.Contains(t, result.Checkpoint.Summary, strings.TrimSpace(tc.summary))
			require.Contains(t, result.Checkpoint.Summary, "details of old events 1..1 were omitted")
		})
	}
}

func TestRunnerExcerptsOldClosedAtomicGroupOnlyAfterActualOverflow(t *testing.T) {
	old := textCompactionEvent(1, "OLD_HEAD "+strings.Repeat("历史记录🙂 ", 12_000)+" OLD_TAIL")
	old.Kind = string(events.KindAgentInput)
	old.TurnID = testIDN(7001)
	originalOldContent := append(json.RawMessage(nil), old.ContentParts...)
	call := mustCompactionEvent(2, string(events.KindModelOutput), "content", json.RawMessage(
		`[{"type":"tool_call","name":"run_command","provider_call_id":"call_old","input":{"command":"`+strings.Repeat("x", 40_000)+`"}}]`))
	call.TurnID = old.TurnID
	tool := textCompactionEvent(3, "command result")
	tool.Kind = string(events.KindToolResult)
	tool.ToolName, tool.ProviderCallID, tool.ToolOutcome = "run_command", "call_old", "completed"
	current := textCompactionEvent(4, "CURRENT_REQUEST_MUST_REMAIN_COMPLETE")
	current.Kind = string(events.KindAgentInput)
	originalCurrentContent := append(json.RawMessage(nil), current.ContentParts...)
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{old, call, tool, current},
		atomicGroups: []executionstore.CompactionAtomicGroupRecord{{
			Kind: "tool_call_result", StartSequence: 1, EndSequence: 3,
		}}}
	client := &summaryModel{sourceInputTokens: 300_000, respond: func(request model.Request) (model.Response, error) {
		if len(request.ProviderRequest) > 20_000 {
			return model.Response{}, model.ProviderError{Kind: model.ErrorKindContextWindow, Code: "too_big"}
		}
		return completeSummaryResponse("Continue the earlier work with the current request."), nil
	}}
	compactionInput := runInput(testPlan(1, 3, 4))
	result, err := testRunner(store, client).
		RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
	require.NoError(t, err)
	require.Equal(t, RunCompleted, result.State)
	require.Greater(t, len(client.requests), 1)
	require.NotContains(t, string(client.requests[0].ProviderRequest), "OVERSIZED CLOSED HISTORY EXCERPT")
	for i, replacement := range store.replacements {
		require.Equal(t, int64(3), replacement.NextSourceEventSequenceEnd, "atomic group must remain whole")
		require.Less(t, len(client.requests[i+1].ProviderRequest), len(client.requests[i].ProviderRequest))
	}
	last := string(client.requests[len(client.requests)-1].ProviderRequest)
	require.Contains(t, last, "OVERSIZED CLOSED HISTORY EXCERPT")
	require.Contains(t, last, "OLD_HEAD")
	require.Contains(t, last, "OLD_TAIL")
	require.Contains(t, last, old.ID.String())
	require.Contains(t, last, "call_old")
	require.NotContains(t, last, "CURRENT_REQUEST_MUST_REMAIN_COMPLETE")
	require.True(t, utf8.ValidString(last))
	require.Equal(t, originalOldContent, store.events[0].ContentParts)
	require.Equal(t, originalCurrentContent, store.events[3].ContentParts)
	require.Contains(t, result.Checkpoint.Summary, "details of old events 1..3 were omitted")
}

func TestRunnerNeverExcerptsCurrentRequest(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("current request ", 20_000)),
		textCompactionEvent(2, "answer"),
	}}
	store.events[0].Kind = string(events.KindAgentInput)
	input := runInput(testPlan(1, 1, 2))
	input.OpeningEventSequence = 1
	client := &summaryModel{results: []summaryResult{{err: model.ProviderError{Kind: model.ErrorKindContextWindow}}}}
	result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
	require.NoError(t, err)
	require.Equal(t, RunTerminal, result.State)
	require.Len(t, client.requests, 1)
	require.Empty(t, store.replacements)
}

func TestRunnerOptionalPublishConflictDoesNotSendAnotherSummary(t *testing.T) {
	store := &fakeStore{parentRecoveryKind: executionstore.ModelCallRecoveryCompactOptional,
		events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("first ", 100)),
			textCompactionEvent(2, strings.Repeat("second ", 100)),
		}, publishErrs: []error{executionstore.ErrCheckpointBoundaryUnsafe}}
	client := &summaryModel{}
	compactionInput := runInput(testPlan(1, 2, 3))
	result, err := testRunner(store, client).
		RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
	require.NoError(t, err)
	require.Equal(t, RunResumeNormal, result.State)
	require.Len(t, client.requests, 1)
	require.Empty(t, store.replacements)
	require.Nil(t, store.published)
	require.Len(t, store.resumedFailures, 1)
}

func TestRunnerExcerptsStopAtFiniteFloor(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("large closed history ", 10_000)),
	}}
	client := &summaryModel{respond: func(model.Request) (model.Response, error) {
		return model.Response{}, model.ProviderError{Kind: model.ErrorKindContextWindow, Code: "fixed_overhead_too_large"}
	}}
	compactionInput := runInput(testPlan(1, 1, 2))
	result, err := testRunner(store, client).
		RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
	require.NoError(t, err)
	require.Equal(t, RunTerminal, result.State)
	require.Greater(t, len(client.requests), 1)
	require.Less(t, len(client.requests), 16)
	last := string(client.requests[len(client.requests)-1].ProviderRequest)
	require.LessOrEqual(t,
		strings.Count(last, "large closed history "), minimumSourceExcerptBytes/len("large closed history "))
	require.Empty(t, store.retryFailures)
	require.Empty(t, store.publishInputs)
	claim := store.addStartedClaim(compactionInput)
	claim.Context.SourceExcerptReductionCount = 100
	store.claims[len(store.claims)-1] = claim
	restartedClient := &summaryModel{}
	result, err = testRunner(store, restartedClient).RunClaimed(t.Context(), compactionInput, claim)
	require.NoError(t, err)
	require.Equal(t, RunCompleted, result.State)
	require.Len(t, restartedClient.requests, 1)
	require.Equal(t, last, string(restartedClient.requests[0].ProviderRequest))
}

func TestRunnerReconstructsExcerptAfterTransientRetry(t *testing.T) {
	store := &fakeStore{atomicGroups: []executionstore.CompactionAtomicGroupRecord{{
		Kind: "tool_call_result", StartSequence: 1, EndSequence: 51,
	}}}
	for sequence := int64(1); sequence <= 50; sequence++ {
		store.events = append(store.events, textCompactionEvent(sequence, "done"))
	}
	store.events = append(store.events, textCompactionEvent(51, strings.Repeat("old history ", 700)))
	input := runInput(testPlan(1, 51, 52))
	original, err := renderCompactionSource(input, executionstore.ContextCheckpointRecord{}, store.events, nil)
	require.NoError(t, err)
	firstLimit := min(initialSourceExcerptBytes, len(original.sourceText)/2)
	first, err := renderCompactionSource(input, executionstore.ContextCheckpointRecord{}, store.events, &firstLimit)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(first.sourceText), len(original.sourceText), "fixture must require skipping a candidate")
	client := &summaryModel{results: []summaryResult{
		{err: model.ProviderError{Kind: model.ErrorKindContextWindow}},
		{err: model.ProviderError{Kind: model.ErrorKindTransient}},
	}}
	result, err := testRunner(store, client).RunClaimed(t.Context(), input, store.addStartedClaim(input))
	require.NoError(t, err)
	require.Equal(t, RunRetryScheduled, result.State)
	require.Len(t, client.requests, 2)
	require.Len(t, store.replacements, 1)
	require.Less(t, len(client.requests[1].ProviderRequest), len(client.requests[0].ProviderRequest))
	previous := store.claims[len(store.claims)-1].Context
	retry := store.addStartedClaim(input)
	retry.Context.AttemptNumber = previous.AttemptNumber + 1
	retry.Context.SourceExcerptReductionCount = previous.SourceExcerptReductionCount
	store.claims[len(store.claims)-1] = retry
	restartedClient := &summaryModel{}
	result, err = testRunner(store, restartedClient).RunClaimed(t.Context(), input, retry)
	require.NoError(t, err)
	require.Equal(t, RunCompleted, result.State)
	require.Len(t, restartedClient.requests, 1)
	require.Equal(t, client.requests[1].ProviderRequest, restartedClient.requests[0].ProviderRequest)
	require.Len(t, store.replacements, 1)
	require.Contains(t, result.Checkpoint.Summary, "details of old events 1..51 were omitted")
}

func TestRunnerPreservesInheritedOmissionNotice(t *testing.T) {
	const notice = "Some details of earlier history were omitted after provider overflow. " +
		"Original events and checkpoints remain stored; this checkpoint does not contain their full content."
	for _, echoed := range []bool{false, true} {
		store := &fakeStore{
			priorCheckpoint: &executionstore.ContextCheckpointRecord{
				ID: testIDN(7840), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
				Summary: strings.Repeat("old state ", 100) + "\n\n" + notice, HasOmittedHistory: true,
			},
			events: []executionstore.CompactionSourceEventRecord{
				{Sequence: 2, Kind: string(events.KindContextCheckpoint)},
				textCompactionEvent(3, strings.Repeat("recent work ", 100)),
			},
		}
		input := runInput(testPlan(2, 3, 4))
		summary := "Continue the recent work."
		if echoed {
			summary += "\n\n" + notice
		}
		client := &summaryModel{results: []summaryResult{{response: completeSummaryResponse(summary)}}}
		result, err := testRunner(store, client).RunClaimed(t.Context(), input, store.addStartedClaim(input))
		require.NoError(t, err)
		require.Equal(t, RunCompleted, result.State)
		require.Equal(t, int64(3), result.Checkpoint.SummarizedThroughEventSequence)
		require.Equal(t, 1, strings.Count(result.Checkpoint.Summary, notice))
	}
}

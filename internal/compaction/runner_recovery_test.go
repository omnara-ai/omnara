package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestRunnerOptionalFailureResumesNormalAfterOneProviderAttempt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  summaryResult
		outcome executionstore.OptionalCompactionOutcome
	}{
		{"overflow", summaryResult{err: model.ProviderError{Kind: model.ErrorKindContextWindow, Code: "too_big"}},
			executionstore.OptionalCompactionIneffective},
		{"payload", summaryResult{err: model.ProviderError{Kind: model.ErrorKindPayloadTooLarge, Code: "too_big"}},
			executionstore.OptionalCompactionIneffective},
		{"timeout", summaryResult{err: model.ProviderError{Kind: model.ErrorKindTransient, Code: "timeout"}},
			executionstore.OptionalCompactionInterrupted},
		{"unknown provider failure", summaryResult{err: model.ProviderError{Kind: model.ErrorKindUnknown,
			Code: "malformed_error_envelope"}}, executionstore.OptionalCompactionInterrupted},
		{"refusal", summaryResult{response: model.Response{StopReason: model.StopReasonRefusal}},
			executionstore.OptionalCompactionIneffective},
		{"empty", summaryResult{response: model.Response{StopReason: model.StopReasonEndTurn}},
			executionstore.OptionalCompactionIneffective},
		{"unknown stop reason", summaryResult{response: model.Response{StopReason: model.StopReasonUnknown}},
			executionstore.OptionalCompactionIneffective},
		{"invalid parsed response", summaryResult{response: model.Response{StopReason: model.StopReasonEndTurn,
			Content: []model.ResponsePart{{Type: "unexpected"}}}}, executionstore.OptionalCompactionIneffective},
		{"tool calls", summaryResult{response: model.Response{StopReason: model.StopReasonToolUse,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeToolCall, ToolName: "unexpected",
				ProviderCallID: "call_1", ToolInput: json.RawMessage(`{}`)}}}},
			executionstore.OptionalCompactionIneffective},
		{"truncated", summaryResult{response: model.Response{StopReason: model.StopReasonMaxTokens,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial"}}}},
			executionstore.OptionalCompactionIneffective},
		{"not reduced", summaryResult{response: completeSummaryResponse(strings.Repeat("expanded summary ", 1_000))},
			executionstore.OptionalCompactionIneffective},
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
			require.Equal(t, tc.outcome, store.resumedFailures[0].Outcome)
			require.Empty(t, store.replacements)
			require.Empty(t, store.retryFailures)
			require.Empty(t, store.terminalFailures)
			require.Empty(t, store.publishInputs)
		})
	}
}

func TestOptionalCompactionOutcomeUsesTypedFailureInsteadOfProviderCode(t *testing.T) {
	for _, kind := range []model.ErrorKind{
		model.ErrorKindTransient, model.ErrorKindRateLimit, model.ErrorKindProviderUnavailable,
		model.ErrorKindUnknown, model.ErrorKindAuth, model.ErrorKindBillingAccount, model.ErrorKindReplayRejected,
		modelprotocol.ErrorKindRuntime, modelprotocol.ErrorKindCanceled,
	} {
		t.Run(string(kind), func(t *testing.T) {
			cause := model.ProviderError{Kind: kind, Code: compactionErrorCodeSummaryTruncated}
			require.Equal(t, executionstore.OptionalCompactionInterrupted, optionalCompactionOutcome(cause, true))
		})
	}
	for _, kind := range []model.ErrorKind{
		model.ErrorKindInvalidRequest, model.ErrorKindContextWindow, model.ErrorKindPayloadTooLarge,
	} {
		t.Run(string(kind), func(t *testing.T) {
			cause := model.ProviderError{Kind: kind}
			require.Equal(t, executionstore.OptionalCompactionInterrupted, optionalCompactionOutcome(cause, false))
			require.Equal(t, executionstore.OptionalCompactionIneffective, optionalCompactionOutcome(cause, true))
		})
	}
	cause := irreducibleCompactionError("no reducible source")
	require.Equal(t, executionstore.OptionalCompactionIneffective, optionalCompactionOutcome(cause, false))
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
		require.NotNil(t, replacement.NextSourceExcerptBytes)
		if i > 0 {
			require.Less(t, *replacement.NextSourceExcerptBytes, *store.replacements[i-1].NextSourceExcerptBytes)
		}
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

func TestRunnerSourceReplacementDoesNotResetTransientRetryBudget(t *testing.T) {
	store := &fakeStore{compactionRetryCount: executionstore.MaxModelCallRetriesPerOperation,
		events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("first ", 100)),
			textCompactionEvent(2, strings.Repeat("second ", 100)),
		}}
	client := &summaryModel{results: []summaryResult{
		{err: model.ProviderError{Kind: model.ErrorKindContextWindow, Code: "too_big"}},
		{err: model.ProviderError{Kind: model.ErrorKindTransient, Code: "unavailable"}},
	}}
	compactionInput := runInput(testPlan(1, 2, 3))
	result, err := testRunner(store, client).
		RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
	require.NoError(t, err)
	require.Equal(t, RunTerminal, result.State)
	require.Len(t, store.replacements, 1)
	require.Empty(t, store.retryFailures)
	require.Equal(t, "unavailable", store.terminalFailures[0].ErrorCode)
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

func TestRunnerNeverExcerptsForOtherSummaryFailures(t *testing.T) {
	for _, result := range []summaryResult{
		{err: model.ProviderError{Kind: model.ErrorKindRateLimit, Code: "rate_limited"}},
		{response: model.Response{StopReason: model.StopReasonMaxTokens,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial"}}}},
		{response: completeSummaryResponse(strings.Repeat("larger summary ", 30_000))},
	} {
		store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("large closed history ", 10_000)),
		}}
		client := &summaryModel{results: []summaryResult{result}}
		compactionInput := runInput(testPlan(1, 1, 2))
		_, err := testRunner(store, client).
			RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
		require.NoError(t, err)
		require.Len(t, client.requests, 1)
		require.Empty(t, store.replacements)
		require.Empty(t, store.publishInputs)
	}
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
	require.Len(t, client.requests, 9)
	require.Equal(t, minimumSourceExcerptBytes, *store.replacements[len(store.replacements)-1].NextSourceExcerptBytes)
	require.Empty(t, store.retryFailures)
	require.Empty(t, store.publishInputs)
}

func TestRunnerReturnsSuppliedOptionalAdmissionFailureAsNormalContinuation(t *testing.T) {
	input := runInput(testPlan(1, 1, 2))
	claim := newCompactionClaim(input, 1, time.Time{})
	claim.Claimed = false
	claim.Context.State = executionstore.ModelCallContextFailed
	claim.Context.RecoveryKind = executionstore.ModelCallRecoveryResumeNormal
	store := &fakeStore{}
	client := &summaryModel{}
	result, err := testRunner(store, client).RunClaimed(context.Background(), input, claim)
	require.NoError(t, err)
	require.Equal(t, RunResumeNormal, result.State)
	require.Empty(t, client.requests)
	require.Empty(t, store.resumedFailures)
}

package compaction

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestRunnerExcerptsPriorCheckpointOnlyAfterProviderOverflow(t *testing.T) {
	for _, replacing := range []bool{false, true} {
		name := "advancing"
		if replacing {
			name = "same coverage"
		}
		t.Run(name, func(t *testing.T) {
			prior := executionstore.ContextCheckpointRecord{
				ID: testIDN(7801), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
				Summary: "PRIOR_HEAD " + strings.Repeat("earlier completed history ", 8_000) + " PRIOR_TAIL",
			}
			current := textCompactionEvent(4, "CURRENT_REQUEST_MUST_REMAIN_COMPLETE")
			current.Kind = string(events.KindAgentInput)
			store := &fakeStore{priorCheckpoint: &prior, events: []executionstore.CompactionSourceEventRecord{
				{Sequence: 2, Kind: string(events.KindContextCheckpoint)},
				textCompactionEvent(3, "closed recent output"), current,
			}}
			input := runInput(testPlan(2, 3, 4))
			if replacing {
				input.Plan.EventSequenceEnd = 1
				input.Plan.ReplacesCheckpointID = prior.ID
			}
			client := &summaryModel{sourceInputTokens: 300_000, respond: func(request model.Request) (model.Response, error) {
				if len(request.ProviderRequest) > 20_000 {
					return model.Response{}, model.ProviderError{Kind: model.ErrorKindContextWindow, Code: "too_big"}
				}
				return completeSummaryResponse("Continue the earlier work with the current request."), nil
			}}
			result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
			require.NoError(t, err)
			require.Equal(t, RunCompleted, result.State)
			require.Equal(t, input.Plan.EventSequenceEnd, result.Checkpoint.SummarizedThroughEventSequence)
			require.Greater(t, len(client.requests), 1)
			require.NotContains(t, string(client.requests[0].ProviderRequest), "OVERSIZED CLOSED HISTORY EXCERPT")
			for i, replacement := range store.replacements {
				require.Equal(t, input.Plan.EventSequenceEnd, replacement.NextSourceEventSequenceEnd)
				require.NotNil(t, replacement.NextSourceExcerptBytes)
				require.Less(t, len(client.requests[i+1].ProviderRequest), len(client.requests[i].ProviderRequest))
			}
			last := string(client.requests[len(client.requests)-1].ProviderRequest)
			require.Contains(t, last, "OVERSIZED CLOSED HISTORY EXCERPT")
			require.Contains(t, last, "PRIOR_HEAD")
			require.Contains(t, last, "PRIOR_TAIL")
			require.Contains(t, last, prior.ID.String())
			require.NotContains(t, last, "CURRENT_REQUEST_MUST_REMAIN_COMPLETE")
			require.Contains(t, result.Checkpoint.Summary, "details of prior checkpoint "+prior.ID.String()+" were omitted")
		})
	}
}

func TestRunnerNeverExcerptsCheckpointCoveringCurrentRequest(t *testing.T) {
	prior := executionstore.ContextCheckpointRecord{
		ID: testIDN(7830), SummarizedThroughEventSequence: 2, CheckpointEventSequence: 3,
		Summary: strings.Repeat("current request ", 10_000),
	}
	store := &fakeStore{priorCheckpoint: &prior, events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(2, "answer"),
	}}
	input := runInput(testPlan(3, 2, 4))
	input.OpeningEventSequence = 1
	input.Plan.ReplacesCheckpointID = prior.ID
	client := &summaryModel{results: []summaryResult{{err: model.ProviderError{Kind: model.ErrorKindContextWindow}}}}
	result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
	require.NoError(t, err)
	require.Equal(t, RunResumeNormal, result.State)
	require.Len(t, client.requests, 1)
	require.Empty(t, store.replacements)
	require.Empty(t, store.terminalFailures)
}

func TestRunnerExcerptsOldCheckpointWithoutClippingAnsweredCurrentSource(t *testing.T) {
	prior := executionstore.ContextCheckpointRecord{
		ID: testIDN(7835), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
		Summary: strings.Repeat("old closed history ", 10_000),
	}
	currentText := "CURRENT_HEAD " + strings.Repeat("keep my exact request ", 200) + " CURRENT_TAIL"
	current := textCompactionEvent(3, currentText)
	current.Kind = string(events.KindAgentInput)
	current.TurnID = testTurnID
	answer := textCompactionEvent(4, "Answered current request")
	answer.TurnID = testTurnID
	store := &fakeStore{priorCheckpoint: &prior, events: []executionstore.CompactionSourceEventRecord{
		{Sequence: 2, Kind: string(events.KindContextCheckpoint)}, current, answer,
	}, atomicGroups: []executionstore.CompactionAtomicGroupRecord{{StartSequence: 3, EndSequence: 4}}}
	input := runInput(testPlan(2, 4, 5))
	input.OpeningEventSequence = 3
	client := &summaryModel{respond: func(request model.Request) (model.Response, error) {
		if len(request.ProviderRequest) > 20_000 {
			return model.Response{}, model.ProviderError{Kind: model.ErrorKindContextWindow}
		}
		return completeSummaryResponse("Continue with the answered request."), nil
	}}
	result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
	require.NoError(t, err)
	require.Equal(t, RunCompleted, result.State)
	require.NotEmpty(t, store.replacements)
	for _, request := range client.requests {
		require.Contains(t, string(request.ProviderRequest), currentText)
	}
	require.Equal(t, int64(4), result.Checkpoint.SummarizedThroughEventSequence)
	require.Contains(t, result.Checkpoint.Summary, "details of prior checkpoint")
	require.NotContains(t, result.Checkpoint.Summary, "details of old events")
}

func TestRunnerCheckpointRecompressionRejectsMismatchedSource(t *testing.T) {
	for _, changedCoverage := range []bool{false, true} {
		t.Run(fmt.Sprint(changedCoverage), func(t *testing.T) {
			prior := executionstore.ContextCheckpointRecord{
				ID: testIDN(7836), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
				Summary: strings.Repeat("old state ", 100),
			}
			store := &fakeStore{priorCheckpoint: &prior}
			input := runInput(testPlan(2, 1, 3))
			input.Plan.ReplacesCheckpointID = prior.ID
			if changedCoverage {
				input.Plan.EventSequenceEnd = 2
			} else {
				input.Plan.ReplacesCheckpointID = testIDN(7837)
			}
			client := &summaryModel{}
			_, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
			require.Error(t, err)
			require.Empty(t, client.requests)
		})
	}
}

func TestRunnerPreservesInheritedOmissionNotice(t *testing.T) {
	const notice = "Some details of earlier history were omitted after provider overflow. " +
		"Original events and checkpoints remain stored; this checkpoint does not contain their full content."
	prior := executionstore.ContextCheckpointRecord{
		ID: testIDN(7840), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
		Summary: strings.Repeat("old state ", 100) + "\n\n" + notice, HasOmittedHistory: true,
	}
	for _, echoed := range []bool{false, true} {
		for _, extraBytes := range []int{0, 1} {
			t.Run(fmt.Sprintf("echoed=%t/extra_bytes=%d", echoed, extraBytes), func(t *testing.T) {
				store := &fakeStore{priorCheckpoint: &prior}
				input := runInput(testPlan(2, 1, 3))
				input.Plan.ReplacesCheckpointID = prior.ID
				summary := strings.Repeat("s", len(prior.Summary)*9/10-len(notice)-2+extraBytes)
				if echoed {
					summary += "\n\n" + notice
				}
				client := &summaryModel{results: []summaryResult{{response: completeSummaryResponse(summary)}}}
				result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
				require.NoError(t, err)
				require.Len(t, client.requests, 1)
				require.Empty(t, store.replacements)
				if extraBytes > 0 {
					require.Equal(t, RunResumeNormal, result.State)
					require.Empty(t, store.publishInputs)
					return
				}
				require.Equal(t, RunCompleted, result.State)
				require.Equal(t, 1, strings.Count(result.Checkpoint.Summary, notice))
				require.Len(t, result.Checkpoint.Summary, len(prior.Summary)*9/10)
			})
		}
	}
}

func TestRunnerUnusableCheckpointRecompressionResumesNormal(t *testing.T) {
	for _, test := range []struct {
		name   string
		result summaryResult
	}{
		{"refusal", summaryResult{response: model.Response{StopReason: model.StopReasonRefusal}}},
		{"malformed", summaryResult{response: model.Response{StopReason: model.StopReasonEndTurn,
			Content: []model.ResponsePart{{Type: "unexpected"}}}}},
		{"not reduced", summaryResult{response: completeSummaryResponse(strings.Repeat("old state ", 1_000))}},
		{"authentication", summaryResult{err: model.ProviderError{Kind: model.ErrorKindAuth, Message: "Access denied"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prior := executionstore.ContextCheckpointRecord{
				ID: testIDN(7850), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
				Summary: strings.Repeat("old state ", 1_000),
			}
			store := &fakeStore{priorCheckpoint: &prior}
			input := runInput(testPlan(2, 1, 3))
			input.Plan.ReplacesCheckpointID = prior.ID
			client := &summaryModel{results: []summaryResult{test.result}}
			result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
			require.NoError(t, err)
			require.Equal(t, RunResumeNormal, result.State)
			require.Len(t, client.requests, 1)
			require.Len(t, store.resumedFailures, 1)
			require.Empty(t, store.resumedFailures[0].Outcome)
			require.Equal(t, result.ModelCallContextID, store.resumedFailures[0].ModelCallContextID)
			require.Empty(t, store.retryFailures)
			require.Empty(t, store.replacements)
			require.Empty(t, store.terminalFailures)
			require.Empty(t, store.publishInputs)
		})
	}
}

func TestRunnerCheckpointRecompressionTransientBudgetResumesNormalOnlyWhenExhausted(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprint(exhausted), func(t *testing.T) {
			prior := executionstore.ContextCheckpointRecord{
				ID: testIDN(7851), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
				Summary: strings.Repeat("old state ", 1_000),
			}
			store := &fakeStore{priorCheckpoint: &prior}
			if exhausted {
				store.compactionRetryCount = executionstore.MaxModelCallRetriesPerOperation
			}
			input := runInput(testPlan(2, 1, 3))
			input.Plan.ReplacesCheckpointID = prior.ID
			client := &summaryModel{results: []summaryResult{{err: model.ProviderError{
				Kind: model.ErrorKindTransient, Code: "unavailable", Message: "Try again later",
			}}}}
			result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
			require.NoError(t, err)
			require.Len(t, client.requests, 1)
			if exhausted {
				require.Equal(t, RunResumeNormal, result.State)
				require.Len(t, store.resumedFailures, 1)
				require.Empty(t, store.resumedFailures[0].Outcome)
				require.Empty(t, store.retryFailures)
			} else {
				require.Equal(t, RunRetryScheduled, result.State)
				require.Len(t, store.retryFailures, 1)
				require.NotNil(t, result.RetryAt)
				require.Empty(t, store.resumedFailures)
			}
			require.Empty(t, store.replacements)
			require.Empty(t, store.terminalFailures)
			require.Empty(t, store.publishInputs)
		})
	}
}

func TestRunnerCheckpointRecompressionOverflowResumesAfterExcerptFloor(t *testing.T) {
	prior := executionstore.ContextCheckpointRecord{
		ID: testIDN(7852), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
		Summary: strings.Repeat("old state ", 1_000),
	}
	store := &fakeStore{priorCheckpoint: &prior}
	input := runInput(testPlan(2, 1, 3))
	input.Plan.ReplacesCheckpointID = prior.ID
	client := &summaryModel{respond: func(model.Request) (model.Response, error) {
		return model.Response{}, model.ProviderError{Kind: model.ErrorKindContextWindow, Message: "Still too large"}
	}}
	result, err := testRunner(store, client).RunClaimed(context.Background(), input, store.addStartedClaim(input))
	require.NoError(t, err)
	require.Equal(t, RunResumeNormal, result.State)
	require.Greater(t, len(client.requests), 1)
	require.Less(t, len(client.requests), 16)
	require.Len(t, store.resumedFailures, 1)
	require.Empty(t, store.resumedFailures[0].Outcome)
	require.Equal(t, minimumSourceExcerptBytes, *store.replacements[len(store.replacements)-1].NextSourceExcerptBytes)
	require.Empty(t, store.terminalFailures)
	require.Empty(t, store.publishInputs)
}

func TestRunnerCheckpointRecompressionConfigurationFailureResumesBeforeProviderRequest(t *testing.T) {
	prior := executionstore.ContextCheckpointRecord{
		ID: testIDN(7853), SummarizedThroughEventSequence: 1, CheckpointEventSequence: 2,
		Summary: strings.Repeat("old state ", 100),
	}
	store := &fakeStore{priorCheckpoint: &prior}
	input := runInput(testPlan(2, 1, 3))
	input.Plan.ReplacesCheckpointID = prior.ID
	client := &summaryModel{}
	runner := testRunner(store, client)
	runner.Resolver = errorResolver{err: model.ProviderError{
		Kind: model.ErrorKindInvalidRequest, Message: "Configured model is unavailable",
	}}
	result, err := runner.RunClaimed(context.Background(), input, store.addStartedClaim(input))
	require.NoError(t, err)
	require.Equal(t, RunResumeNormal, result.State)
	require.Len(t, store.resumedFailures, 1)
	require.Empty(t, store.resumedFailures[0].Outcome)
	require.Empty(t, client.requests)
	require.Empty(t, store.retryFailures)
	require.Empty(t, store.terminalFailures)
}

package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestRunnerPublishesProgressiveCheckpointInsteadOfReexpandingShrunkSource(t *testing.T) {
	store := &fakeStore{
		consecutiveCheckpointCount: 2,
		events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("old state one ", 60)),
			textCompactionEvent(2, strings.Repeat("old state two ", 60)),
			textCompactionEvent(3, "remaining closed state"),
			textCompactionEvent(4, "current opening input"),
		},
	}
	client := &summaryModel{results: []summaryResult{
		{response: model.Response{
			ID:         "resp_truncated_larger_source",
			StopReason: model.StopReasonMaxTokens,
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial"}},
		}},
		{response: completeSummaryResponse("## Goal\nContinue.\n\n## Next Steps\nUse retained input.")},
	}, checkpointPreparedEstimates: []int{300_000}}
	builder := &fakeContextBuilder{}
	progressivePolicy := &fakeProgressiveCheckpointPolicy{decision: ProgressiveCheckpointDecision{
		Allowed: true,
	}}
	runner := Runner{
		Store:             store,
		Resolver:          compactionResolver(client),
		ContextBuilder:    builder,
		ProgressivePolicy: progressivePolicy,
	}
	input := runInput(testPlan(1, 2, 3))
	input.ParentModelCallContextID = testIDN(777)
	input.OpeningEventSequence = 3

	result, err := runner.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("run shrunk progressive compaction: %v", err)
	}
	if result.State != RunCompleted || len(store.replacements) != 1 ||
		store.replacements[0].NextSourceEventSequenceEnd != 1 {
		t.Fatalf("shrunk progressive result=%+v replacements=%+v", result, store.replacements)
	}
	if len(store.claimInputs) != 1 || store.claimInputs[0].SourceEventSequenceEnd != 2 ||
		len(store.claims) != 2 || store.claims[1].Context.SourceEventSequenceEnd == nil ||
		*store.claims[1].Context.SourceEventSequenceEnd != 1 || len(client.requests) != 2 {
		t.Fatalf("source claims=%+v provider requests=%d, want monotone 2 then 1", store.claimInputs, len(client.requests))
	}
	if len(builder.inputs) != 1 || builder.inputs[0].CheckpointOverride == nil ||
		builder.inputs[0].CheckpointOverride.SummarizedThroughEventSequence != 1 {
		t.Fatalf("candidate validation inputs = %+v", builder.inputs)
	}
	if len(store.publishInputs) != 1 {
		t.Fatalf("published checkpoints = %d, want one progressive intermediate", len(store.publishInputs))
	}
	if len(progressivePolicy.inputs) != 1 ||
		progressivePolicy.inputs[0].CandidateSummarizedThroughSequence != 1 ||
		progressivePolicy.inputs[0].ConsecutiveCheckpointCount != 2 {
		t.Fatalf("progressive policy inputs = %+v", progressivePolicy.inputs)
	}
}

func TestRunnerDoesNotCountUnansweredOpeningAsProgressiveSource(t *testing.T) {
	steeringEvent := textCompactionEvent(3, "later unanswered steering input")
	steeringEvent.Kind = string(events.KindAgentInput)
	checkpointEvent := textCompactionEvent(4, "checkpoint control event")
	checkpointEvent.Kind = string(events.KindContextCheckpoint)
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("old completed state ", 60)),
		textCompactionEvent(2, "current unanswered opening input"),
		steeringEvent,
		checkpointEvent,
	}}
	client := &summaryModel{checkpointPreparedEstimates: []int{300_000}}
	runner := Runner{
		Store:          store,
		Resolver:       compactionResolver(client),
		ContextBuilder: &fakeContextBuilder{},
	}
	input := runInput(testPlan(1, 1, 4))
	input.ParentModelCallContextID = testIDN(778)
	input.OpeningEventSequence = 2

	result, err := runner.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("run protected-opening progressive fallback: %v", err)
	}
	if result.State != RunTerminal || len(store.publishInputs) != 0 ||
		len(store.terminalFailures) != 1 ||
		!strings.Contains(store.terminalFailures[0].ErrorMessage, "no model-visible source") {
		t.Fatalf(
			"protected-opening result=%+v terminal=%+v publishes=%+v",
			result,
			store.terminalFailures,
			store.publishInputs,
		)
	}
}

func TestRunnerCheckpointControlEventDoesNotMakeOpeningCompactable(t *testing.T) {
	checkpointEvent := textCompactionEvent(3, "checkpoint control event")
	checkpointEvent.Kind = string(events.KindContextCheckpoint)
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "old completed state"),
		textCompactionEvent(2, "current unanswered opening input"),
		checkpointEvent,
	}}
	input := runInput(testPlan(1, 2, 3))
	input.ParentModelCallContextID = testIDN(779)
	input.OpeningEventSequence = 2

	_, err := testRunner(store, &summaryModel{}).Run(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "retain unanswered opening events") {
		t.Fatalf("checkpoint control opening protection error = %v", err)
	}
	if len(store.claimInputs) != 0 {
		t.Fatalf("invalid protected-opening plan created claims: %+v", store.claimInputs)
	}
}

func TestRunnerPublishesProgressiveCheckpointOnlyAfterOrdinaryProjectionCannotFit(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("old state one ", 60)),
		textCompactionEvent(2, strings.Repeat("old state two ", 60)),
		textCompactionEvent(3, "current opening input"),
	}}
	client := &summaryModel{checkpointPreparedEstimates: []int{300_000}}
	builder := &fakeContextBuilder{}
	progressivePolicy := &fakeProgressiveCheckpointPolicy{decision: ProgressiveCheckpointDecision{
		Allowed: true,
	}}
	runner := Runner{
		Store:             store,
		Resolver:          compactionResolver(client),
		ContextBuilder:    builder,
		ProgressivePolicy: progressivePolicy,
	}

	result, err := runner.Run(context.Background(), runInput(testPlan(1, 2, 4)))
	if err != nil {
		t.Fatalf("run progressive fallback: %v", err)
	}
	if result.State != RunCompleted || result.Checkpoint == nil || len(store.publishInputs) != 1 {
		t.Fatalf("progressive result=%+v publishes=%+v", result, store.publishInputs)
	}
	if len(progressivePolicy.inputs) != 1 ||
		progressivePolicy.inputs[0].CandidateSummarizedThroughSequence != 2 ||
		progressivePolicy.inputs[0].ConsecutiveCheckpointCount != 0 {
		t.Fatalf("progressive policy inputs = %+v", progressivePolicy.inputs)
	}
}

func TestRunnerValidatesCandidateWithSerializedNormalProviderRequest(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("old state one ", 60)),
		textCompactionEvent(2, strings.Repeat("old state two ", 60)),
		textCompactionEvent(3, "remaining closed state"),
		textCompactionEvent(4, "current opening input"),
	}}
	client := &summaryModel{
		caps: model.Capabilities{
			ContextWindowTokens:    10_000,
			MaxOutputTokens:        1_024,
			DefaultMaxOutputTokens: 1_024,
		},
		sourceInputTokens:           500,
		checkpointPreparedEstimates: []int{9_000},
	}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 2, 4)))
	if err != nil {
		t.Fatalf("run serialized candidate validation: %v", err)
	}
	if result.State != RunCompleted || result.Checkpoint == nil || len(store.publishInputs) != 1 {
		t.Fatalf("serialized candidate result=%+v publishes=%+v", result, store.publishInputs)
	}
	checkpointPrepares := 0
	for _, bundle := range client.preparedBundles {
		if bundle.ContextCheckpoint != nil {
			checkpointPrepares++
		}
	}
	if checkpointPrepares != 1 || len(client.requests) != 1 {
		t.Fatalf(
			"serialized checkpoint prepares/provider calls = %d/%d, want 1/1",
			checkpointPrepares,
			len(client.requests),
		)
	}
}

func TestRunnerRejectsProgressiveCheckpointWithoutRemainingSemanticSource(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("old state one ", 60)),
		textCompactionEvent(2, strings.Repeat("old state two ", 60)),
	}}
	client := &summaryModel{
		caps: model.Capabilities{
			ContextWindowTokens:    10_000,
			MaxOutputTokens:        1_024,
			DefaultMaxOutputTokens: 1_024,
		},
		sourceInputTokens:           500,
		checkpointPreparedEstimates: []int{9_000},
	}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run exhausted serialized candidate: %v", err)
	}
	if result.State != RunTerminal || len(store.publishInputs) != 0 ||
		len(store.terminalFailures) != 1 ||
		!strings.Contains(store.terminalFailures[0].ErrorMessage, "no model-visible source") {
		t.Fatalf(
			"exhausted serialized candidate result=%+v terminal=%+v publishes=%+v",
			result,
			store.terminalFailures,
			store.publishInputs,
		)
	}
}

func TestRunnerTerminalizesWhenProgressivePolicyRejectsFallback(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("irreducible state ", 60)),
		textCompactionEvent(2, "remaining closed state"),
		textCompactionEvent(3, "current opening input"),
	}}
	client := &summaryModel{checkpointPreparedEstimates: []int{300_000}}
	progressivePolicy := &fakeProgressiveCheckpointPolicy{decision: ProgressiveCheckpointDecision{
		Reason: "test progression exhausted",
	}}
	runner := Runner{
		Store:             store,
		Resolver:          compactionResolver(client),
		ContextBuilder:    &fakeContextBuilder{},
		ProgressivePolicy: progressivePolicy,
	}

	result, err := runner.Run(context.Background(), runInput(testPlan(1, 1, 3)))
	if err != nil {
		t.Fatalf("run rejected progressive fallback: %v", err)
	}
	if result.State != RunTerminal || len(store.publishInputs) != 0 ||
		len(store.terminalFailures) != 1 ||
		store.terminalFailures[0].ErrorCode != "compaction_source_irreducible" ||
		!strings.Contains(store.terminalFailures[0].ErrorMessage, "test progression exhausted") {
		t.Fatalf("rejected result=%+v terminal=%+v publishes=%+v", result, store.terminalFailures, store.publishInputs)
	}
}

func TestRunnerUsesPriorCumulativeCheckpointAndNextClosedRange(t *testing.T) {
	store := &fakeStore{
		priorCheckpoint: &executionstore.ContextCheckpointRecord{
			ID: testIDN(800), SummarizedThroughEventSequence: 2,
			CheckpointEventSequence: 3, Summary: "Earlier cumulative state.",
		},
		events: []executionstore.CompactionSourceEventRecord{
			mustCompactionEvent(3, string(events.KindContextCheckpoint), "published", nil),
			textCompactionEvent(4, strings.Repeat("New durable fact. ", 30)),
			textCompactionEvent(5, strings.Repeat("New next step. ", 30)),
		},
	}
	client := &summaryModel{}
	result, err := testRunner(store, client).
		Run(context.Background(), runInput(testPlan(3, 5, 5)))
	if err != nil {
		t.Fatalf("run cumulative compaction: %v", err)
	}
	if result.Checkpoint == nil ||
		result.Checkpoint.SummarizedThroughEventSequence != 5 {
		t.Fatalf("checkpoint lineage = %+v", result.Checkpoint)
	}
	prompt := string(client.preparedBundles[0].Messages[0].Content)
	if !strings.Contains(prompt, "Earlier cumulative state.") {
		t.Fatalf("prompt omitted prior cumulative checkpoint: %s", prompt)
	}
	if strings.Contains(prompt, "context_checkpoint") || !strings.Contains(prompt, "New durable fact.") {
		t.Fatalf("prompt rendered checkpoint control or omitted new content: %s", prompt)
	}
}

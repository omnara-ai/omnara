package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestRunnerCheckpointControlEventDoesNotMakeOpeningCompactable(t *testing.T) {
	checkpointEvent := textCompactionEvent(3, "checkpoint control event")
	checkpointEvent.Kind = string(events.KindContextCheckpoint)
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, "old completed state"),
		textCompactionEvent(2, "current unanswered opening input"),
		checkpointEvent,
	}}
	input := runInput(testPlan(1, 2, 3))
	input.OpeningEventSequence = 2

	_, err := testRunner(store, &summaryModel{}).RunClaimed(context.Background(), input, store.addStartedClaim(input))
	if err == nil || !strings.Contains(err.Error(), "retain unanswered opening events") {
		t.Fatalf("checkpoint control opening protection error = %v", err)
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
	compactionInput := runInput(testPlan(3, 5, 5))
	result, err := testRunner(store, client).
		RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
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

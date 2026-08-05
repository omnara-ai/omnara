package compaction

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestRunnerPublishesAuditedCumulativeCheckpoint(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("User asked for a detailed status. ", 20)),
		textCompactionEvent(2, strings.Repeat("Assistant reported completed work and next steps. ", 20)),
	}}
	client := &summaryModel{}
	now := time.Unix(123, 0).UTC()
	result, err := testRunner(store, client, func() time.Time { return now }).
		Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run compaction: %v", err)
	}
	if result.State != RunCompleted || result.Checkpoint == nil {
		t.Fatalf("run result = %+v, want completed checkpoint", result)
	}
	if len(store.claimInputs) != 1 ||
		store.claimInputs[0].SourceEventSequenceEnd != 2 {
		t.Fatalf("compaction claim = %+v", store.claimInputs)
	}
	if len(store.publishInputs) != 1 ||
		store.publishInputs[0].ModelCallContextID != result.ModelCallContextID ||
		store.publishInputs[0].APIFormat != client.APIFormat() ||
		store.publishInputs[0].APIVariant != client.ModelAPIVariant() {
		t.Fatalf("checkpoint publication = %+v", store.publishInputs)
	}
	if result.Checkpoint.SummarizedThroughEventSequence != 2 ||
		result.Checkpoint.ProducerModelCallContextID != result.ModelCallContextID {
		t.Fatalf("checkpoint lineage = %+v", result.Checkpoint)
	}
	prompt := string(client.requests[0].ProviderRequest)
	if !strings.Contains(prompt, "Event 1 (model_output.content)") ||
		!strings.Contains(prompt, "Event 2 (model_output.content)") {
		t.Fatalf("compaction prompt was not derived from canonical events: %s", prompt)
	}
}

func TestRunnerReplaysCheckpointByProducingContext(t *testing.T) {
	now := time.Unix(123, 0).UTC()
	input := runInput(testPlan(1, 1, 1))
	claim := newCompactionClaim(compactionClaimInput(input, now), 1, now)
	claim.Created = false
	claim.Claimed = false
	claim.Context.State = executionstore.ModelCallContextSucceeded
	checkpoint := executionstore.ContextCheckpointRecord{
		ID:                             testIDN(900),
		ProjectID:                      input.Plan.ProjectID,
		AgentID:                        input.Plan.AgentID,
		SummarizedThroughEventSequence: input.Plan.EventSequenceEnd,
		ProducerModelCallContextID:     claim.Context.ID,
		Summary:                        "Existing durable summary.",
	}
	store := &fakeStore{
		events:       []executionstore.CompactionSourceEventRecord{textCompactionEvent(1, "closed source")},
		claimResults: []executionstore.ModelCallClaim{claim},
		published:    &checkpoint,
	}

	client := &summaryModel{}
	result, err := testRunner(store, client, func() time.Time { return now }).
		Run(context.Background(), input)
	if err != nil {
		t.Fatalf("replay completed compaction: %v", err)
	}
	if result.State != RunCompleted || result.Checkpoint == nil ||
		result.Checkpoint.ID != checkpoint.ID ||
		result.ModelCallContextID != claim.Context.ID ||
		len(client.requests) != 0 || len(store.publishInputs) != 0 {
		t.Fatalf(
			"replayed result=%+v requests=%+v publications=%+v",
			result,
			client.requests,
			store.publishInputs,
		)
	}
}

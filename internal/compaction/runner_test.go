package compaction

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type reasoningSelectionResolver struct {
	client     *summaryModel
	selections []model.Selection
}

func (r *reasoningSelectionResolver) Resolve(
	_ context.Context,
	selection model.Selection,
) (model.ResolvedClient, error) {
	r.selections = append(r.selections, selection)
	caps := r.client.caps
	caps.DefaultReasoningEffort = selection.Options.ReasoningEffort
	r.client.caps = caps
	return model.ResolvedClient{
		Client:                    r.client,
		ConfiguredModelRevisionID: selection.ConfiguredModelRevisionID,
	}, nil
}

func TestRunnerPublishesAuditedCumulativeCheckpoint(t *testing.T) {
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(1, strings.Repeat("User asked for a detailed status. ", 20)),
		textCompactionEvent(2, strings.Repeat("Assistant reported completed work and next steps. ", 20)),
	}}
	response := completeSummaryResponse(
		"## Goal\nContinue the current task.\n\n## Next Steps\nProceed with the next verified action.",
	)
	response.ProviderReportedCostUSD = "0.0000025"
	client := &summaryModel{results: []summaryResult{{response: response}}}
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
		store.publishInputs[0].APIVariant != client.ModelAPIVariant() ||
		store.publishInputs[0].ProviderReportedCostUSD != "0.0000025" {
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

func TestRunnerCarriesAgentReasoningSelectionThroughCompaction(t *testing.T) {
	const reasoningEffort = "high"
	store := &fakeStore{
		agentConfig: compactionAgentConfigWithReasoning(reasoningEffort),
		events: []executionstore.CompactionSourceEventRecord{
			textCompactionEvent(1, strings.Repeat("User supplied durable context. ", 20)),
			textCompactionEvent(2, strings.Repeat("Assistant recorded the durable context. ", 20)),
		},
	}
	client := &summaryModel{caps: model.Capabilities{
		ContextWindowTokens:    200_000,
		MaxOutputTokens:        64_000,
		DefaultMaxOutputTokens: 2_048,
		SupportsReasoning:      true,
		SupportedReasoningEfforts: []string{
			"low", "high",
		},
	}}
	resolver := &reasoningSelectionResolver{client: client}
	now := time.Unix(123, 0).UTC()
	runner := Runner{
		Store:          store,
		Resolver:       resolver,
		ContextBuilder: &fakeContextBuilder{},
		Now:            func() time.Time { return now },
	}
	store.clock = runner.now

	result, err := runner.Run(context.Background(), runInput(testPlan(1, 2, 2)))
	if err != nil {
		t.Fatalf("run compaction with agent reasoning selection: %v", err)
	}
	if result.State != RunCompleted || result.Checkpoint == nil {
		t.Fatalf("run result = %+v, want completed checkpoint", result)
	}
	if len(resolver.selections) != 1 {
		t.Fatalf("model selections = %+v, want one", resolver.selections)
	}
	selection := resolver.selections[0]
	if selection.ConfiguredModelRevisionID != testIDN(601).String() ||
		selection.Options.ReasoningEffort != reasoningEffort {
		t.Fatalf("compaction model selection = %+v", selection)
	}
	if len(client.preparedPolicies) != len(client.preparedBundles) ||
		len(client.preparedPolicies) < 2 {
		t.Fatalf(
			"prepared policies=%d bundles=%d, want summary and candidate requests",
			len(client.preparedPolicies),
			len(client.preparedBundles),
		)
	}
	sawSummaryRequest := false
	sawCandidateRequest := false
	for index, policy := range client.preparedPolicies {
		if policy.DefaultReasoningEffort != reasoningEffort {
			t.Fatalf("prepared policy %d = %+v, want inherited reasoning", index, policy)
		}
		if client.preparedBundles[index].ContextCheckpoint == nil {
			sawSummaryRequest = true
			if policy.MaxOutputTokens != 16_384 {
				t.Fatalf("summary policy %d output = %d, want 16384", index, policy.MaxOutputTokens)
			}
			continue
		}
		sawCandidateRequest = true
		if policy.MaxOutputTokens != 2_048 {
			t.Fatalf("candidate policy %d output = %d, want normal 2048", index, policy.MaxOutputTokens)
		}
	}
	if !sawSummaryRequest || !sawCandidateRequest || len(client.requests) != 1 {
		t.Fatalf(
			"summary=%v candidate=%v provider requests=%d",
			sawSummaryRequest,
			sawCandidateRequest,
			len(client.requests),
		)
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

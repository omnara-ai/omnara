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
	caps.DefaultReasoningEffort = selection.Overrides.ReasoningEffort
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
	compactionInput := runInput(testPlan(1, 2, 2))
	result, err := testRunner(store, client, func() time.Time { return now }).
		RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
	if err != nil {
		t.Fatalf("run compaction: %v", err)
	}
	if result.State != RunCompleted || result.Checkpoint == nil {
		t.Fatalf("run result = %+v, want completed checkpoint", result)
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
		MaxOutputTokens:        new(64_000),
		DefaultMaxOutputTokens: 2_048,
		SupportsReasoning:      true,
		SupportedReasoningEfforts: []string{
			"low", "high",
		},
	}}
	resolver := &reasoningSelectionResolver{client: client}
	now := time.Unix(123, 0).UTC()
	runner := Runner{
		Store:    store,
		Resolver: resolver,
		Now:      func() time.Time { return now },
	}
	store.clock = runner.now
	compactionInput := runInput(testPlan(1, 2, 2))
	result, err := runner.RunClaimed(context.Background(), compactionInput, store.addStartedClaim(compactionInput))
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
		selection.Overrides.ReasoningEffort != reasoningEffort {
		t.Fatalf("compaction model selection = %+v", selection)
	}
	if len(client.preparedPolicies) == 0 || len(client.requests) != 1 {
		t.Fatalf("prepared policies=%d provider requests=%d", len(client.preparedPolicies), len(client.requests))
	}
	for index, policy := range client.preparedPolicies {
		if policy.ReasoningEffort != reasoningEffort {
			t.Fatalf("prepared policy %d = %+v, want inherited reasoning", index, policy)
		}
		if policy.MaxOutputTokens != client.Capabilities().DefaultMaxOutputTokens {
			t.Fatalf(
				"summary policy %d output = %d, want %d",
				index,
				policy.MaxOutputTokens,
				client.Capabilities().DefaultMaxOutputTokens,
			)
		}
	}
}

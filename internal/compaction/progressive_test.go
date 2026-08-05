package compaction

import "testing"

func TestBoundedProgressiveCheckpointPolicyAdvancesAndBoundsImmediateChain(t *testing.T) {
	policy := BoundedProgressiveCheckpointPolicy{}
	decision, err := policy.Evaluate(ProgressiveCheckpointInput{
		PriorSummarizedThroughEventSequence: 70,
		ConsecutiveCheckpointCount:          2,
		CandidateSummarizedThroughSequence:  80,
		HasRemainingSemanticSource:          true,
	})
	if err != nil {
		t.Fatalf("evaluate third progressive checkpoint: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("third progressive decision = %+v", decision)
	}

	decision, err = policy.Evaluate(ProgressiveCheckpointInput{
		PriorSummarizedThroughEventSequence: 80,
		ConsecutiveCheckpointCount:          3,
		CandidateSummarizedThroughSequence:  90,
		HasRemainingSemanticSource:          true,
	})
	if err != nil {
		t.Fatalf("evaluate exhausted progressive checkpoint: %v", err)
	}
	if decision.Allowed || decision.Reason == "" {
		t.Fatalf("exhausted progressive decision = %+v", decision)
	}
}

func TestBoundedProgressiveCheckpointPolicyStartsAfterLineageReset(t *testing.T) {
	decision, err := (BoundedProgressiveCheckpointPolicy{}).Evaluate(ProgressiveCheckpointInput{
		PriorSummarizedThroughEventSequence: 80,
		ConsecutiveCheckpointCount:          0,
		CandidateSummarizedThroughSequence:  90,
		HasRemainingSemanticSource:          true,
	})
	if err != nil {
		t.Fatalf("evaluate new progression chain: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("new progression decision = %+v", decision)
	}
}

func TestBoundedProgressiveCheckpointPolicyRequiresFrontierProgress(t *testing.T) {
	decision, err := (BoundedProgressiveCheckpointPolicy{}).Evaluate(ProgressiveCheckpointInput{
		PriorSummarizedThroughEventSequence: 80,
		CandidateSummarizedThroughSequence:  80,
	})
	if err != nil {
		t.Fatalf("evaluate non-progressing checkpoint: %v", err)
	}
	if decision.Allowed || decision.Reason == "" {
		t.Fatalf("non-progressing decision = %+v", decision)
	}
}

func TestBoundedProgressiveCheckpointPolicyRequiresRemainingSemanticSource(t *testing.T) {
	decision, err := (BoundedProgressiveCheckpointPolicy{}).Evaluate(ProgressiveCheckpointInput{
		CandidateSummarizedThroughSequence: 80,
	})
	if err != nil {
		t.Fatalf("evaluate exhausted source: %v", err)
	}
	if decision.Allowed || decision.Reason == "" {
		t.Fatalf("exhausted-source decision = %+v", decision)
	}
}

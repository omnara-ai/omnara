package compaction

import "fmt"

// Bound repeated checkpointing so an input that cannot gain enough headroom
// terminates instead of cycling through summaries indefinitely.
const defaultMaxProgressiveCheckpoints = 3

type ProgressiveCheckpointPolicy interface {
	Evaluate(ProgressiveCheckpointInput) (ProgressiveCheckpointDecision, error)
}

type ProgressiveCheckpointInput struct {
	PriorSummarizedThroughEventSequence int64
	ConsecutiveCheckpointCount          int
	CandidateSummarizedThroughSequence  int64
	HasRemainingSemanticSource          bool
}

type ProgressiveCheckpointDecision struct {
	Allowed bool
	Reason  string
}

type BoundedProgressiveCheckpointPolicy struct{}

func (BoundedProgressiveCheckpointPolicy) Evaluate(
	input ProgressiveCheckpointInput,
) (ProgressiveCheckpointDecision, error) {
	decision := ProgressiveCheckpointDecision{}
	if input.CandidateSummarizedThroughSequence <= input.PriorSummarizedThroughEventSequence {
		decision.Reason = "candidate checkpoint does not advance the summarized event frontier"
		return decision, nil
	}
	if !input.HasRemainingSemanticSource {
		decision.Reason = "candidate checkpoint leaves no model-visible source for another compaction step"
		return decision, nil
	}
	if input.ConsecutiveCheckpointCount < 0 {
		return ProgressiveCheckpointDecision{}, fmt.Errorf("consecutive checkpoint count cannot be negative")
	}
	if input.ConsecutiveCheckpointCount >= defaultMaxProgressiveCheckpoints {
		decision.Reason = fmt.Sprintf(
			"progressive checkpoint limit of %d was reached",
			defaultMaxProgressiveCheckpoints,
		)
		return decision, nil
	}
	decision.Allowed = true
	return decision, nil
}

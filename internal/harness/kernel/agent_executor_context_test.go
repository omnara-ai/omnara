package kernel

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestOptionalCompactionRearmRequiresObservedHeadroomAtOrBelowThreeQuarters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		minimum int
		want    bool
	}{{0, false}, {749, true}, {750, true}, {751, false}} {
		recovery := executionstore.ModelCallRecoveryState{
			LastOptionalContextID: uuid.New(), LastOptionalInputTargetTokens: 1000,
			LastOptionalCompactionNeedsHeadroom: true, LatestObservedNormalInputTokens: 1000,
			MinimumObservedNormalInputTokens: test.minimum,
		}
		prepared := model.PreparedRequest{
			InputBudget: model.InputBudgetAssessment{EstimatedInputTokens: 1100, UsableInputTokens: 1000},
		}
		if got := shouldAttemptOptionalCompaction(prepared, 1000, recovery); got != test.want {
			t.Errorf("minimum observed input %d: optional compaction = %v, want %v", test.minimum, got, test.want)
		}
	}
}

func TestModelCallOpeningRequiresVerbatimRetention(t *testing.T) {
	t.Parallel()

	unanswered := []executionstore.CompactionSourceEventRecord{
		{Sequence: 11, Kind: string(events.KindAgentInput)},
	}
	if !modelCallOpeningRequiresVerbatimRetention(unanswered, 10, 11) {
		t.Fatal("unanswered opening after the checkpoint was not protected")
	}
	if modelCallOpeningRequiresVerbatimRetention(unanswered, 11, 11) {
		t.Fatal("opening already represented by the checkpoint was protected again")
	}

	answered := append(unanswered, executionstore.CompactionSourceEventRecord{
		Sequence: 12,
		Kind:     string(events.KindModelOutput),
	})
	if modelCallOpeningRequiresVerbatimRetention(answered, 10, 11) {
		t.Fatal("opening followed by a model output was treated as unanswered")
	}
}

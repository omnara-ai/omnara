package kernel

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

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

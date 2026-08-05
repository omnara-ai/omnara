package executionstore

import (
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestPrepareCreateAgentContentInputRejectsQueuedInteractionCancellation(t *testing.T) {
	t.Parallel()

	_, err := prepareCreateAgentContentInput(CreateAgentContentInputInput{
		CancelOpenInteractions: true,
	})
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("queued cancellation option error = %v, want ErrInvalidRequest", err)
	}
}

func TestPrepareCreateAgentContentInputAllowsSteeringInteractionCancellation(t *testing.T) {
	t.Parallel()

	input, err := prepareCreateAgentContentInput(CreateAgentContentInputInput{
		DeliveryMode:           DeliveryModeSteering,
		CancelOpenInteractions: true,
	})
	if err != nil {
		t.Fatalf("prepare steering cancellation option: %v", err)
	}
	if !input.CancelOpenInteractions {
		t.Fatal("steering cancellation option was not preserved for request execution")
	}
}

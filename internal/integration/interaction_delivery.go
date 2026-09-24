package integration

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const interactionPresentationTimeout = 30 * time.Second

type interactionRunner interface {
	TrySubmit(string, func(context.Context) error) bool
}

func (p InteractionPresenter) Enqueue(
	runner interactionRunner, projectID, agentID, interactionID uuid.UUID,
) {
	if runner == nil {
		return
	}
	submitted := runner.TrySubmit("interaction_prompt", func(ctx context.Context) error {
		if err := p.Present(ctx, projectID, agentID, interactionID); err != nil {
			return fmt.Errorf("present interaction %s for agent %s: %w", interactionID, agentID, err)
		}
		return nil
	})
	if !submitted {
		log := p.Log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("best-effort interaction presentation dropped",
			"project_id", projectID, "agent_id", agentID, "interaction_id", interactionID)
	}
}

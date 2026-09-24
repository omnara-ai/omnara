package integration

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const interactionPresentationTimeout = 30 * time.Second

type interactionRunner interface {
	TrySubmit(string, func(context.Context) error) bool
}

func (p InteractionPresenter) Enqueue(
	runner interactionRunner, projectID, agentID, interactionID uuid.UUID,
) bool {
	if runner == nil {
		return false
	}
	return runner.TrySubmit("interaction_prompt", func(ctx context.Context) error {
		if err := p.Present(ctx, projectID, agentID, interactionID); err != nil {
			return fmt.Errorf("present interaction %s for agent %s: %w", interactionID, agentID, err)
		}
		return nil
	})
}

func (p InteractionPresenter) EnqueuePending(ctx context.Context, runner interactionRunner) error {
	var integrationTypes []string
	for _, definition := range integrationdefinition.All() {
		if definition.InteractionHandler != nil {
			integrationTypes = append(integrationTypes, string(definition.IntegrationType))
		}
	}
	pending, err := p.Store.Execution().ListPendingInteractionPresentations(ctx,
		integrationTypes,
		executionstore.MaxPendingInteractionPresentations)
	if err != nil {
		return err
	}
	for _, item := range pending {
		if ctx.Err() != nil || !p.Enqueue(runner, item.ProjectID, item.AgentID, item.ID) {
			break
		}
	}
	return ctx.Err()
}

func (p InteractionPresenter) RunPending(ctx context.Context, runner interactionRunner) {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := p.EnqueuePending(ctx, runner); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "discover pending interaction presentations", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

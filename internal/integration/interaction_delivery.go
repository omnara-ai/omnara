package integration

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const interactionPresentationTimeout = 30 * time.Second

// interactionRunner is also used for the worker's other bounded background work.
// Enqueueing never claims delivery: shutdown or saturation leaves work discoverable.
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

// EnqueuePending discovers only work that has never been attempted. Present
// atomically claims when it actually runs, so immediate delivery and replicas
// can safely race. An attempted but unconfirmed send is never automatically reposted.
func (p InteractionPresenter) EnqueuePending(ctx context.Context, runner interactionRunner) error {
	pending, err := p.Store.Execution().ListPendingInteractionPresentations(ctx,
		[]string{appdefinition.Slack, appdefinition.Discord},
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

// RunPending supplements immediate scheduling with bounded durable discovery.
// The runner owns concurrency and shutdown; a full queue is retried next tick.
func (p InteractionPresenter) RunPending(ctx context.Context, runner interactionRunner) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := p.EnqueuePending(ctx, runner); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "discover pending interaction presentations", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

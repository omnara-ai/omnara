package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const integrationPermissionPromptCopyTimeout = 30 * time.Second

func (e Executor) postIntegrationPrompt(
	ctx context.Context,
	turn Turn,
	interaction executionstore.AgentInteractionRecord,
) error {
	return e.postIntegrationPromptToPinnedChannel(
		ctx,
		turn,
		interaction,
		func(ctx context.Context) error {
			return e.Store.Execution().EnsureRuntimeLockActive(ctx, turn.ProjectID, turn.AgentID, turn.RuntimeLockID)
		},
	)
}

func (e Executor) enqueueIntegrationPromptCopy(
	turn Turn,
	interaction executionstore.AgentInteractionRecord,
) {
	if e.BackgroundRunner == nil {
		return
	}
	if e.BackgroundRunner.TrySubmit(
		"integration_permission_prompt_copy",
		func(ctx context.Context) error {
			deliveryCtx, cancel := context.WithTimeout(ctx, integrationPermissionPromptCopyTimeout)
			defer cancel()
			if err := e.copyPermissionPromptToIntegration(
				deliveryCtx,
				turn,
				interaction.ID,
			); err != nil &&
				!(errors.Is(err, context.Canceled) && ctx.Err() != nil) {
				slog.WarnContext(
					deliveryCtx,
					"integration permission prompt copy failed",
					"agent_id",
					turn.AgentID,
					"interaction_id",
					interaction.ID,
					"error",
					err,
				)
			}
			return nil
		},
	) {
		return
	}
	slog.Warn(
		"integration permission prompt copy dropped",
		"agent_id",
		turn.AgentID,
		"interaction_id",
		interaction.ID,
	)
}

func (e Executor) copyPermissionPromptToIntegration(
	ctx context.Context,
	turn Turn,
	interactionID storage.ID,
) error {
	current, found, err := e.Store.Execution().GetAgentInteraction(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		interactionID,
	)
	if err != nil {
		return err
	}
	if !found || current.State != executionstore.AgentInteractionStateOpen {
		return nil
	}
	if err := e.postIntegrationPromptToPinnedChannel(
		ctx,
		turn,
		current,
		nil,
	); err != nil {
		return fmt.Errorf("copy permission interaction to integration: %w", err)
	}
	return nil
}

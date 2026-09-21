package tools

import (
	"context"

	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (e Executor) interactionPresenter() integration.InteractionPresenter {
	return integration.InteractionPresenter{Store: e.Store, HTTPClient: e.IntegrationHTTPClient}
}

// PostIntegrationRuntimeMessage preserves the kernel entrypoint while using
// current handler selection, independently of model send tools or subscriptions.
func (e Executor) PostIntegrationRuntimeMessage(ctx context.Context, turn Turn, text string) error {
	return e.interactionPresenter().PostRuntimeMessage(ctx, turn.ProjectID, turn.AgentID, turn.RuntimeLockID, text)
}

func (e Executor) enqueueIntegrationPromptCopy(turn Turn, interaction executionstore.AgentInteractionRecord) {
	e.interactionPresenter().Enqueue(e.BackgroundRunner, turn.ProjectID, turn.AgentID, interaction.ID)
}

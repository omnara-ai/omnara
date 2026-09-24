package tools

import (
	"context"

	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (e Executor) interactionPresenter() integrationruntime.InteractionPresenter {
	return integrationruntime.InteractionPresenter{Store: e.Store, HTTPClient: e.IntegrationHTTPClient, Log: e.logger()}
}

func (e Executor) PostIntegrationRuntimeMessage(ctx context.Context, turn Turn, text string) error {
	return e.interactionPresenter().PostRuntimeMessage(ctx, turn.ProjectID, turn.AgentID, turn.RuntimeLockID, text)
}

func (e Executor) enqueueIntegrationPromptCopy(turn Turn, interaction executionstore.AgentInteractionRecord) {
	if len(interaction.Destination) == 0 {
		return
	}
	e.interactionPresenter().Enqueue(e.BackgroundRunner, turn.ProjectID, turn.AgentID, interaction.ID)
}

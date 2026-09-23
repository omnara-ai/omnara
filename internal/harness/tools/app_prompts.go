package tools

import (
	"context"

	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (e Executor) interactionPresenter() apps.InteractionPresenter {
	return apps.InteractionPresenter{Store: e.Store, HTTPClient: e.AppHTTPClient}
}

func (e Executor) PostAppRuntimeMessage(ctx context.Context, turn Turn, text string) error {
	return e.interactionPresenter().PostRuntimeMessage(ctx, turn.ProjectID, turn.AgentID, turn.RuntimeLockID, text)
}

func (e Executor) enqueueAppPromptCopy(turn Turn, interaction executionstore.AgentInteractionRecord) {
	e.interactionPresenter().Enqueue(e.BackgroundRunner, turn.ProjectID, turn.AgentID, interaction.ID)
}

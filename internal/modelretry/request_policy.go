package modelretry

import (
	"context"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
)

type ProviderReplayPolicyStore interface {
	GetProviderReplaySuppressionCutoff(
		context.Context,
		storage.ID,
		storage.ID,
		storage.ID,
	) (int64, error)
}

func RequestPolicyForModelCall(
	ctx context.Context,
	store ProviderReplayPolicyStore,
	projectID, agentID, modelCallContextID storage.ID,
	base model.RequestPolicy,
) (model.RequestPolicy, error) {
	cutoff, err := store.GetProviderReplaySuppressionCutoff(
		ctx,
		projectID,
		agentID,
		modelCallContextID,
	)
	if err != nil {
		return model.RequestPolicy{}, err
	}
	if cutoff > base.ProviderReplayCutoffEventSequence {
		base.ProviderReplayCutoffEventSequence = cutoff
	}
	return base, nil
}

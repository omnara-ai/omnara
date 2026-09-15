package modelretry

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
)

type ProviderReplayPolicyStore interface {
	GetProviderReplaySuppressionCutoff(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
	) (int64, error)
}

func RequestPolicyForModelCall(
	ctx context.Context,
	store ProviderReplayPolicyStore,
	projectID, agentID, modelCallContextID uuid.UUID,
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

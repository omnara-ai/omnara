package modelretry

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type ProviderReplayPolicyStore interface {
	ListProviderReplayRejections(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
	) (executionstore.ProviderReplayRejections, error)
}

func RequestPolicyForModelCall(
	ctx context.Context,
	store ProviderReplayPolicyStore,
	projectID, agentID, modelCallContextID uuid.UUID,
	base model.RequestPolicy,
) (model.RequestPolicy, error) {
	rejections, err := store.ListProviderReplayRejections(
		ctx,
		projectID,
		agentID,
		modelCallContextID,
	)
	if err != nil {
		return model.RequestPolicy{}, err
	}
	// A rejection under any model that shares replay with this one invalidated
	// the conversation's earlier replay, so switching models must not revive it.
	for slug, cutoff := range rejections.CutoffBySlug {
		if cutoff > base.ProviderReplayCutoffEventSequence &&
			model.ProviderReplaySlugsCompatible(rejections.APIFormat, slug, rejections.ProviderModelSlug) {
			base.ProviderReplayCutoffEventSequence = cutoff
		}
	}
	return base, nil
}

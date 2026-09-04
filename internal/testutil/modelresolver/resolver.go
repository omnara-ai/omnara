package modelresolver

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type LiveGrant struct {
	Store  *storage.Store
	Client model.Client
}

func (r LiveGrant) Resolve(
	ctx context.Context,
	selection model.Selection,
) (model.ResolvedClient, error) {
	if r.Store == nil || r.Client == nil {
		return model.ResolvedClient{}, errors.New("test model resolver store and client are required")
	}
	orgID, err := storage.ParseID(selection.OrgID)
	if err != nil {
		return model.ResolvedClient{}, fmt.Errorf("parse model selection org id: %w", err)
	}
	projectID, err := storage.ParseID(selection.ProjectID)
	if err != nil {
		return model.ResolvedClient{}, fmt.Errorf("parse model selection project id: %w", err)
	}
	revisionID, err := storage.ParseID(selection.ConfiguredModelRevisionID)
	if err != nil {
		return model.ResolvedClient{}, fmt.Errorf("parse configured model revision id: %w", err)
	}
	revision, err := r.Store.Models().GetConfiguredModelRevisionForUse(ctx, orgID, revisionID)
	if err != nil {
		return model.ResolvedClient{}, err
	}
	_, err = r.Store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		orgID,
		projectID,
		revision.ConfiguredModelID,
	)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return model.ResolvedClient{}, fmt.Errorf(
				"configured model has no active project grant: %w",
				storeerr.ErrModelGrantUnavailable,
			)
		}
		return model.ResolvedClient{}, err
	}
	return model.ResolvedClient{
		Client:                    r.Client,
		ConfiguredModelRevisionID: revision.ID.String(),
		ProviderRequestIdentity: model.ProviderReplayIdentityForClient(
			revision.ModelProviderConfigID.String(),
			r.Client,
		),
	}, nil
}

type Static struct {
	Clients []model.ResolvedClient
}

func (r Static) Resolve(_ context.Context, selection model.Selection) (model.ResolvedClient, error) {
	if selection.ConfiguredModelRevisionID == "" {
		return model.ResolvedClient{}, errors.New("configured model revision id is required")
	}
	var anonymous model.ResolvedClient
	anonymousCount := 0
	usableCount := 0
	var matched model.ResolvedClient
	matchCount := 0
	for _, resolved := range r.Clients {
		if resolved.Client == nil {
			continue
		}
		usableCount++
		if resolved.ConfiguredModelRevisionID == selection.ConfiguredModelRevisionID {
			matched = resolved
			matchCount++
		}
		if resolved.ConfiguredModelRevisionID == "" {
			anonymous = resolved
			anonymousCount++
		}
	}
	if matchCount == 1 {
		return matched, nil
	}
	if matchCount > 1 {
		return model.ResolvedClient{}, fmt.Errorf(
			"configured model revision %s has multiple static clients",
			selection.ConfiguredModelRevisionID,
		)
	}
	if anonymousCount == 1 && usableCount == 1 {
		anonymous.ConfiguredModelRevisionID = selection.ConfiguredModelRevisionID
		return anonymous, nil
	}
	return model.ResolvedClient{}, fmt.Errorf(
		"configured model revision %s is not configured for this test",
		selection.ConfiguredModelRevisionID,
	)
}

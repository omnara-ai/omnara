package executionstore

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxLaunchChannelBindings = 64

// LaunchChannelBinding grants access to an existing project-owned channel. It
// neither creates a conversation nor selects the agent's current channel.
type LaunchChannelBinding struct {
	ChannelID          uuid.UUID
	Grants             integrationstore.ChannelGrants
	ReplyChannelGrants *integrationstore.ChannelGrants
}

func (s *Store) prepareLaunchChannelBindingsTx(
	ctx context.Context, tx pgx.Tx, input LaunchAgentInput,
) ([]integrationstore.CreateIntegrationTargetBindingInput, error) {
	if len(input.ChannelBindings) > MaxLaunchChannelBindings {
		return nil, storeerr.InvalidRequest(fmt.Errorf(
			"channel_bindings exceeds the %d binding limit", MaxLaunchChannelBindings))
	}
	bindings := make([]integrationstore.CreateIntegrationTargetBindingInput, 0, len(input.ChannelBindings))
	seen := make(map[uuid.UUID]bool, len(input.ChannelBindings))
	installs := make([]uuid.UUID, 0, len(input.ChannelBindings))
	for _, binding := range input.ChannelBindings {
		if binding.ChannelID == uuid.Nil || seen[binding.ChannelID] {
			return nil, storeerr.InvalidRequest(errors.New("channel_bindings must name distinct channels"))
		}
		seen[binding.ChannelID] = true
		target, err := s.integrations.GetIntegrationTargetTx(ctx, tx, input.ProjectID, binding.ChannelID)
		if err != nil {
			return nil, err
		}
		installs = append(installs, target.IntegrationInstallID)
		bindings = append(bindings, integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: input.ProjectID, IntegrationInstallID: target.IntegrationInstallID,
			IntegrationTargetID: target.ID, Source: "api",
			ReceiveAllowed: binding.Grants.ReceiveAllowed, ReadAllowed: binding.Grants.ReadAllowed,
			SendAllowed: binding.Grants.SendAllowed, ReplyChannelGrants: binding.ReplyChannelGrants,
		})
	}
	// Enter every installation's deletion gate before profile and agent locks.
	// The same ordering is used regardless of the request's channel order.
	slices.SortFunc(installs, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	installs = slices.Compact(installs)
	for _, id := range installs {
		if err := s.q.WithTx(tx).LockIntegrationInstallLifecycleShared(ctx,
			dbsqlc.LockIntegrationInstallLifecycleSharedParams{InstallID: id}); err != nil {
			return nil, fmt.Errorf("lock channel installation for agent launch: %w", err)
		}
	}
	// Route/OAuth setup takes installation then profile row locks. Shared
	// lifecycle gates alone cannot fence those writers, so retain live install
	// and application rows in that same order before launch locks its profile.
	for _, id := range installs {
		if _, err := s.q.WithTx(tx).LockIntegrationTargetCreateAuthority(ctx,
			dbsqlc.LockIntegrationTargetCreateAuthorityParams{
				ProjectID: input.ProjectID, IntegrationInstallID: id,
			}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, storeerr.ErrNotFound
			}
			return nil, fmt.Errorf("lock channel authority for agent launch: %w", err)
		}
	}
	return bindings, nil
}

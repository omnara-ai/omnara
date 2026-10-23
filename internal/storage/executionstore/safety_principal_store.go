package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type SafetyPrincipal struct {
	Kind publicid.Kind
	ID   uuid.UUID
}

func (s *Store) SafetyPrincipal(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	inputIDs []uuid.UUID,
) (SafetyPrincipal, error) {
	project := SafetyPrincipal{Kind: publicid.KindProject, ID: projectID}
	row, err := s.q.GetLatestInputActor(ctx, dbsqlc.GetLatestInputActorParams{
		ProjectID: projectID, AgentID: agentID, InputIds: inputIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return project, nil
	}
	if err != nil {
		return SafetyPrincipal{}, fmt.Errorf("load responsible actor: %w", err)
	}
	principal := safetyPrincipalForActor(row.ID, row.Provider, row.ProviderUserID)
	if principal.Kind != publicid.KindAgent {
		return principal, nil
	}
	agentPrefix, _ := publicid.Prefix(publicid.KindAgent)
	root, err := s.q.GetLatestRootOpeningActorBefore(ctx, dbsqlc.GetLatestRootOpeningActorBeforeParams{
		ProjectID: projectID, AgentID: agentID, OpenedBefore: row.QueuedAt, AgentPublicIDPrefix: agentPrefix + "_",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return project, nil
	}
	if err != nil {
		return SafetyPrincipal{}, fmt.Errorf("load root responsible actor: %w", err)
	}
	return safetyPrincipalForActor(root.ID, root.Provider, root.ProviderUserID), nil
}

func safetyPrincipalForActor(actorID uuid.UUID, provider, providerUserID string) SafetyPrincipal {
	if ActorProvider(provider) == ActorProviderOmnara {
		for _, kind := range []publicid.Kind{
			publicid.KindUser, publicid.KindOrgAPIKey, publicid.KindCronTrigger, publicid.KindAgent,
		} {
			if id, err := publicid.Decode(kind, providerUserID); err == nil {
				return SafetyPrincipal{Kind: kind, ID: id}
			}
		}
	}
	return SafetyPrincipal{Kind: publicid.KindActor, ID: actorID}
}

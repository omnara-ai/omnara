package executionstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func selectAdmittedInteractionDestinationTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
	inputs []AgentInputRecord,
) error {
	q := dbsqlc.New(tx)
	row, err := q.GetInteractionSelection(ctx, dbsqlc.GetInteractionSelectionParams{
		ProjectID: projectID, AgentID: agentID,
	})
	if err != nil || !row.InteractionAutoSelect {
		return err
	}
	if row.InteractionTargetID == nil {
		hasOrigin := false
		for _, input := range inputs {
			if input.InputKind == "content" && input.IntegrationTargetID != uuid.Nil {
				hasOrigin = true
				break
			}
		}
		if !hasOrigin {
			return nil
		}
	}
	var actorIDs []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for i := len(inputs) - 1; i >= 0; i-- {
		input := inputs[i]
		if input.InputKind != "content" {
			continue
		}
		if input.IntegrationTargetID != uuid.Nil || input.ActorID == uuid.Nil {
			break
		}
		if !seen[input.ActorID] {
			actorIDs = append(actorIDs, input.ActorID)
			seen[input.ActorID] = true
		}
	}
	preserves := map[uuid.UUID]bool{}
	if len(actorIDs) > 0 {
		actors, err := q.ListActorIdentitiesByIDs(ctx, dbsqlc.ListActorIdentitiesByIDsParams{
			ProjectID: projectID, Ids: actorIDs,
		})
		if err != nil {
			return fmt.Errorf("load admitted input actor identities: %w", err)
		}
		for _, actor := range actors {
			preserves[actor.ID] = internalInputPreservesInteractionSelection(ActorProvider(actor.Provider), actor.ProviderUserID)
		}
	}
	for i := len(inputs) - 1; i >= 0; i-- {
		input := inputs[i]
		if input.InputKind != "content" || (input.IntegrationTargetID == uuid.Nil && preserves[input.ActorID]) {
			continue
		}
		selection, err := resolveInteractionSelectionForOriginTx(
			ctx, tx, projectID, agentID, row.CurrentConfigID, input.IntegrationTargetID,
		)
		if err != nil || selection == interactionSelectionFromRow(row) {
			return err
		}
		return writeInteractionSelection(ctx, q, projectID, agentID, selection)
	}
	return nil
}

func internalInputPreservesInteractionSelection(provider ActorProvider, userID string) bool {
	if provider != ActorProviderOmnara {
		return false
	}
	if _, err := publicid.Decode(publicid.KindAgent, userID); err == nil {
		return true
	}
	_, err := publicid.Decode(publicid.KindCronTrigger, userID)
	return err == nil
}

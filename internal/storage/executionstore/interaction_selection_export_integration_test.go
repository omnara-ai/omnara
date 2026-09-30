//go:build integration

package executionstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func IntegrationSelectAdmittedInteractionDestinationTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID uuid.UUID, inputs []AgentInputRecord,
) error {
	return selectAdmittedInteractionDestinationTx(ctx, tx, projectID, agentID, inputs)
}

func IntegrationSelectInteractionDestinationForOriginTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID, originTargetID uuid.UUID,
) (InteractionSelection, error) {
	if err := selectAdmittedInteractionDestinationTx(ctx, tx, projectID, agentID, []AgentInputRecord{{
		InputKind: "content", IntegrationTargetID: originTargetID,
	}}); err != nil {
		return InteractionSelection{}, err
	}
	row, err := dbsqlc.New(tx).GetInteractionSelection(ctx, dbsqlc.GetInteractionSelectionParams{
		ProjectID: projectID, AgentID: agentID,
	})
	return interactionSelectionFromRow(row), err
}

func IntegrationCaptureInteractionDestinationTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID uuid.UUID,
) (json.RawMessage, error) {
	return captureInteractionDestinationTx(ctx, tx, projectID, agentID)
}

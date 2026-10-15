//go:build integration

package executionstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func IntegrationApplyAdmissionDestination(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID uuid.UUID,
	inputs []AgentInputRecord,
) error {
	admission := agentexecution.InputAdmission{}
	for _, input := range inputs {
		admission.Inputs = append(
			admission.Inputs,
			agentexecution.AdmittedContent{
				ID:                  input.ID,
				ActorID:             input.ActorID,
				IntegrationTargetID: input.IntegrationTargetID,
			},
		)
	}
	return applyAdmissionDestination(ctx, unit, projectID, agentID, admission)
}

func IntegrationSelectInteractionDestinationForOriginTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID, originTargetID uuid.UUID,
) (InteractionSelection, error) {
	if err := IntegrationApplyAdmissionDestination(ctx,
		unit,
		projectID,
		agentID,
		[]AgentInputRecord{{InputKind: "content",
			IntegrationTargetID: originTargetID}}); err != nil {
		return InteractionSelection{}, err
	}
	row, err := dbsqlc.New(unit.DB()).
		GetInteractionSelection(ctx, dbsqlc.GetInteractionSelectionParams{ProjectID: projectID, AgentID: agentID})
	return interactionSelectionFromRow(row), err
}

func IntegrationCaptureInteractionDestinationTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID uuid.UUID,
) (json.RawMessage, error) {
	return captureInteractionDestinationTx(ctx, tx, projectID, agentID)
}

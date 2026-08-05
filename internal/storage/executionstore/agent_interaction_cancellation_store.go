package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const interactionCancellationReasonSupersededByInput = "superseded_by_input"

func cancelOpenInteractionsForSteeringInputTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
	supersedingInputID ID,
) ([]ID, error) {
	turn, err := qtx.CurrentContinuableAgentTurn(
		ctx,
		dbsqlc.CurrentContinuableAgentTurnParams{ProjectID: projectID, AgentID: agentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load current turn for interaction cancellation: %w", err)
	}
	interactionIDs, err := qtx.CancelOpenAgentInteractionsForAgent(
		ctx,
		dbsqlc.CancelOpenAgentInteractionsForAgentParams{
			ProjectID:         projectID,
			AgentID:           agentID,
			TurnID:            turn.ID,
			Reason:            interactionCancellationReasonSupersededByInput,
			ResolvedByInputID: sqlcIDFromNil(supersedingInputID),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("cancel open interactions for steering input: %w", err)
	}
	rows, err := qtx.ListAgentInteractionsByIDs(ctx, dbsqlc.ListAgentInteractionsByIDsParams{
		ProjectID: projectID,
		AgentID:   agentID,
		Ids:       interactionIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("load canceled interactions for steering input: %w", err)
	}
	canceledInteractionIDs := make([]ID, 0, len(rows))
	for _, row := range rows {
		interaction := agentInteractionRecordFromSQLC(row)
		if err := applyPermissionInteractionResolutionTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			interaction,
		); err != nil {
			return nil, err
		}
		if err := completeQuestionToolCallTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			interaction,
		); err != nil {
			return nil, err
		}
		canceledInteractionIDs = append(canceledInteractionIDs, interaction.ID)
	}
	return canceledInteractionIDs, nil
}

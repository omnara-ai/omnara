package executionstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func resolveModelReadyBoundaryTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	projectID, agentID, modelCallContextID ID,
) (bool, error) {
	hasLaterSemanticEvent, err := q.ModelCallContextHasLaterSemanticEvent(
		ctx,
		dbsqlc.ModelCallContextHasLaterSemanticEventParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			ModelCallContextID: modelCallContextID,
		},
	)
	if err != nil {
		return false, fmt.Errorf("check model call context semantic frontier: %w", err)
	}
	steering, err := selectLockedSteeringAgentInputsForAdmissionTx(ctx, q, projectID, agentID)
	if err != nil {
		return false, err
	}
	if len(steering) == 0 {
		return hasLaterSemanticEvent, nil
	}
	if _, err := admitLockedAgentInputsAndOpenTurnTx(
		ctx,
		txNotifications,
		tx,
		q,
		admitAgentInputAndOpenTurnInput{
			ProjectID: projectID,
			AgentID:   agentID,
		},
		steering,
	); err != nil {
		return false, fmt.Errorf("admit steering at model-ready boundary: %w", err)
	}
	return true, nil
}

func supersedeUnacceptedAtLaterModelReadyBoundaryTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	projectID, agentID, modelCallContextID ID,
	wakeupReason string,
) (bool, error) {
	preempted, err := resolveModelReadyBoundaryTx(
		ctx,
		txNotifications,
		tx,
		q,
		projectID,
		agentID,
		modelCallContextID,
	)
	if err != nil || !preempted {
		return preempted, err
	}
	metadata, err := marshalJSON(map[string]string{"reason": wakeupReason})
	if err != nil {
		return false, fmt.Errorf("marshal model-ready boundary wakeup metadata: %w", err)
	}
	if err := q.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: projectID,
		AgentID:   agentID,
		Metadata:  metadata,
	}); err != nil {
		return false, fmt.Errorf("reconcile wakeup after model-ready boundary: %w", err)
	}
	return true, nil
}

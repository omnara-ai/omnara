package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PromoteQueuedInputToSteeringInput struct {
	ProjectID              ID
	AgentID                ID
	InputID                ID
	CancelOpenInteractions bool
}

type DemoteSteeringInputToQueuedInput struct {
	ProjectID ID
	AgentID   ID
	InputID   ID
}

func (s *Store) PromoteQueuedInputToSteering(
	ctx context.Context,
	input PromoteQueuedInputToSteeringInput,
) error {
	return s.changeAgentInputDeliveryMode(
		ctx,
		input.ProjectID,
		input.AgentID,
		input.InputID,
		input.CancelOpenInteractions,
		func(qtx *dbsqlc.Queries, ctx context.Context) (int64, error) {
			return qtx.PromoteQueuedInputToSteering(
				ctx,
				dbsqlc.PromoteQueuedInputToSteeringParams{
					RankStride: agentInputRankStride,
					ProjectID:  input.ProjectID,
					AgentID:    input.AgentID,
					ID:         input.InputID,
				},
			)
		},
	)
}

func (s *Store) DemoteSteeringInputToQueued(
	ctx context.Context,
	input DemoteSteeringInputToQueuedInput,
) error {
	return s.changeAgentInputDeliveryMode(
		ctx,
		input.ProjectID,
		input.AgentID,
		input.InputID,
		false,
		func(qtx *dbsqlc.Queries, ctx context.Context) (int64, error) {
			return qtx.DemoteSteeringInputToQueued(
				ctx,
				dbsqlc.DemoteSteeringInputToQueuedParams{
					RankStride: agentInputRankStride,
					ProjectID:  input.ProjectID,
					AgentID:    input.AgentID,
					ID:         input.InputID,
				},
			)
		},
	)
}

func (s *Store) changeAgentInputDeliveryMode(
	ctx context.Context,
	projectID, agentID, inputID ID,
	cancelOpenInteractions bool,
	mutate func(*dbsqlc.Queries, context.Context) (int64, error),
) error {
	if isNilID(projectID) || isNilID(agentID) || isNilID(inputID) {
		return errors.New("project id, agent id, and input id are required")
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin change agent input delivery mode: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); err != nil {
		return fmt.Errorf("lock agent for input delivery mode change: %w", err)
	}
	changed, err := mutate(qtx, ctx)
	if err != nil {
		return fmt.Errorf("change agent input delivery mode: %w", err)
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	if cancelOpenInteractions {
		if _, err := cancelOpenInteractionsForSteeringInputTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			projectID,
			agentID,
			inputID,
		); err != nil {
			return err
		}
	}
	if err := qtx.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: projectID,
		AgentID:   agentID,
		Metadata:  json.RawMessage(`{"reason":"input_delivery_mode_changed"}`),
	}); err != nil {
		return fmt.Errorf("reconcile wakeup after input delivery mode change: %w", err)
	}
	return s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"change agent input delivery mode",
	)
}

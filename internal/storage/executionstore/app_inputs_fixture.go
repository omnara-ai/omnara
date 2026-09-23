//go:build integration

package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) GetAppTargetInputByIdempotency(
	ctx context.Context,
	input GetAppTargetInputByIdempotencyInput,
) (AgentInputRecord, bool, error) {
	if input.AppID == uuid.Nil || input.AppTargetID == uuid.Nil ||
		input.IdempotencyKey == "" {
		return AgentInputRecord{}, false, errors.New(
			"app, app target, and idempotency key are required",
		)
	}
	app, err := s.apps.GetProjectAppByID(ctx, input.AppID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	target, err := s.apps.GetAppTarget(ctx, app.ProjectID, input.AppTargetID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	if target.AppID != app.ID {
		return AgentInputRecord{}, false, storeerr.ErrConflict
	}
	return appTargetInputByIdempotency(ctx, s.q, app, target, input.IdempotencyKey)
}

func appTargetInputByIdempotency(
	ctx context.Context,
	q *dbsqlc.Queries,
	app appstore.ProjectAppRecord,
	target appstore.AppTargetRecord,
	idempotencyKey string,
) (AgentInputRecord, bool, error) {
	row, err := q.GetAgentInputByIdempotency(
		ctx,
		dbsqlc.GetAgentInputByIdempotencyParams{
			ProjectID:           app.ProjectID,
			AgentID:             target.AgentID,
			IdempotencyScope:    appstore.IdempotencyScope(app),
			InputIdempotencyKey: idempotencyKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInputRecord{}, false, nil
	}
	if err != nil {
		return AgentInputRecord{}, false, fmt.Errorf("load agent input by idempotency: %w", err)
	}
	agentInput := agentInputRecordFromIdempotencySQLC(row)
	if agentInput.AppTargetID != target.ID {
		return AgentInputRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	return agentInput, true, nil
}

type GetAppTargetInputByIdempotencyInput struct {
	AppID          uuid.UUID
	AppTargetID    uuid.UUID
	IdempotencyKey string
}

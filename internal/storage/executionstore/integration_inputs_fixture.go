//go:build integration

package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) GetIntegrationTargetInputByIdempotency(
	ctx context.Context,
	input GetIntegrationTargetInputByIdempotencyInput,
) (AgentInputRecord, bool, error) {
	if input.IntegrationID == uuid.Nil || input.IntegrationTargetID == uuid.Nil ||
		input.IdempotencyKey == "" {
		return AgentInputRecord{}, false, errors.New(
			"integration, integration target, and idempotency key are required",
		)
	}
	integration, err := s.integrations.GetProjectIntegrationByID(ctx, input.IntegrationID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	target, err := s.integrations.GetIntegrationTarget(ctx, integration.ProjectID, input.IntegrationTargetID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	if target.IntegrationID != integration.ID {
		return AgentInputRecord{}, false, storeerr.ErrConflict
	}
	return integrationTargetInputByIdempotency(ctx, s.q, integration, target, input.IdempotencyKey)
}

func integrationTargetInputByIdempotency(
	ctx context.Context,
	q *dbsqlc.Queries,
	integration integrationstore.ProjectIntegrationRecord,
	target integrationstore.IntegrationTargetRecord,
	idempotencyKey string,
) (AgentInputRecord, bool, error) {
	row, err := q.GetAgentInputByIdempotency(
		ctx,
		dbsqlc.GetAgentInputByIdempotencyParams{
			ProjectID:           integration.ProjectID,
			AgentID:             target.AgentID,
			IdempotencyScope:    integrationstore.IdempotencyScope(integration),
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
	if agentInput.IntegrationTargetID != target.ID {
		return AgentInputRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	return agentInput, true, nil
}

type GetIntegrationTargetInputByIdempotencyInput struct {
	IntegrationID       uuid.UUID
	IntegrationTargetID uuid.UUID
	IdempotencyKey      string
}

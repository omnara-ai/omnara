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

// GetIntegrationTargetInputByIdempotency is an integration-test inspection helper.
// Production provider inputs are admitted and replayed through the inbox.
func (s *Store) GetIntegrationTargetInputByIdempotency(
	ctx context.Context,
	input GetIntegrationTargetInputByIdempotencyInput,
) (AgentInputRecord, bool, error) {
	if input.IntegrationConnectionID == uuid.Nil || input.IntegrationTargetID == uuid.Nil ||
		input.IdempotencyKey == "" {
		return AgentInputRecord{}, false, errors.New(
			"integration connection, integration target, and idempotency key are required",
		)
	}
	install, err := s.integrations.GetIntegrationConnectionByID(ctx, input.IntegrationConnectionID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	target, err := s.integrations.GetIntegrationTarget(ctx, install.ProjectID, input.IntegrationTargetID)
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	if target.IntegrationConnectionID != install.ID {
		return AgentInputRecord{}, false, storeerr.ErrConflict
	}
	return integrationTargetInputByIdempotency(ctx, s.q, install, target, input.IdempotencyKey)
}

func integrationTargetInputByIdempotency(
	ctx context.Context,
	q *dbsqlc.Queries,
	install integrationstore.IntegrationConnectionRecord,
	target integrationstore.IntegrationTargetRecord,
	idempotencyKey string,
) (AgentInputRecord, bool, error) {
	row, err := q.GetAgentInputByIdempotency(
		ctx,
		dbsqlc.GetAgentInputByIdempotencyParams{
			ProjectID:           install.ProjectID,
			AgentID:             target.AgentID,
			IdempotencyScope:    integrationstore.IdempotencyScope(install),
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
	IntegrationConnectionID uuid.UUID
	IntegrationTargetID     uuid.UUID
	IdempotencyKey          string
}

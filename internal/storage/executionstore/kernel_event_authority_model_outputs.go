package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type CreateModelOutputAuthorityInput struct {
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	ModelCallContextID      uuid.UUID
	ServedProviderModelSlug string
	StopReason              modelenvelope.StopReason
	ProviderReplay          json.RawMessage
	Usage                   modelenvelope.Usage
}

type ModelOutputAuthorityRecord struct {
	ID                      uuid.UUID
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	TurnID                  uuid.UUID
	ModelCallContextID      uuid.UUID
	ServedProviderModelSlug string
	StopReason              modelenvelope.StopReason
	ProviderResponseID      string
	ProviderReplay          json.RawMessage
	Usage                   modelenvelope.Usage
	CreatedAt               time.Time
}

func (s *Store) GetModelOutputForContext(
	ctx context.Context,
	projectID, agentID, modelCallContextID uuid.UUID,
) (ModelOutputAuthorityRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || modelCallContextID == uuid.Nil {
		return ModelOutputAuthorityRecord{}, false, errors.New(
			"project, agent, and model context are required",
		)
	}
	row, err := s.q.GetModelOutputByModelContext(ctx, dbsqlc.GetModelOutputByModelContextParams{
		ProjectID:          projectID,
		AgentID:            agentID,
		ModelCallContextID: modelCallContextID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelOutputAuthorityRecord{}, false, nil
	}
	if err != nil {
		return ModelOutputAuthorityRecord{}, false, fmt.Errorf("get model output for context: %w", err)
	}
	return modelOutputAuthorityFromGetSQLC(row), true, nil
}

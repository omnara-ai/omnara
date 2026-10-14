//go:build integration

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

const (
	ContentBlockOwnerAgentInput     = "agent_input"
	ContentBlockOwnerModelOutput    = "model_output"
	ContentBlockOwnerToolCallResult = "tool_call_result"
)

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
func modelOutputAuthorityFromGetSQLC(row dbsqlc.GetModelOutputByModelContextRow) ModelOutputAuthorityRecord {
	return ModelOutputAuthorityRecord{
		ID:                      row.ID,
		ProjectID:               row.ProjectID,
		AgentID:                 row.AgentID,
		TurnID:                  row.TurnID,
		ModelCallContextID:      row.ModelCallContextID,
		ServedProviderModelSlug: row.ServedProviderModelSlug,
		StopReason:              modelenvelope.StopReason(row.StopReason),
		ProviderResponseID:      row.ProviderResponseID,
		ProviderReplay:          rawMessageFromSQLCPtr(row.ProviderReplay),
		Usage: modelUsageFromSQLC(
			row.InputTokensTotal,
			row.UncachedInputTokens,
			row.CacheReadInputTokens,
			row.CacheWriteInputTokens,
			row.OutputTokensTotal,
			row.ReasoningOutputTokens,
		),
		CreatedAt: row.CreatedAt,
	}
}

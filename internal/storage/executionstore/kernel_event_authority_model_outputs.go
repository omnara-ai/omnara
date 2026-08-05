package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateModelOutputAuthorityInput struct {
	ProjectID               ID
	AgentID                 ID
	ModelCallContextID      ID
	ServedProviderModelSlug string
	StopReason              modelenvelope.StopReason
	ProviderReplay          json.RawMessage
	Usage                   modelenvelope.Usage
}

type ModelOutputAuthorityRecord struct {
	ID                      ID
	ProjectID               ID
	AgentID                 ID
	TurnID                  ID
	ModelCallContextID      ID
	ServedProviderModelSlug string
	StopReason              modelenvelope.StopReason
	ProviderResponseID      string
	ProviderReplay          json.RawMessage
	Usage                   modelenvelope.Usage
	CreatedAt               time.Time
}

func (s *Store) GetModelOutputForContext(
	ctx context.Context,
	projectID, agentID, modelCallContextID ID,
) (ModelOutputAuthorityRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(modelCallContextID) {
		return ModelOutputAuthorityRecord{}, false, errors.New("project, agent, and model context are required")
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

func createModelOutputAuthorityTx(
	ctx context.Context,
	db sqlExecutor,
	input CreateModelOutputAuthorityInput,
) (ModelOutputAuthorityRecord, error) {
	if err := validateModelOutputAuthorityInput(input); err != nil {
		return ModelOutputAuthorityRecord{}, err
	}
	input.Usage = modelUsageForStorage(input.Usage)
	providerReplay := bytes.TrimSpace(input.ProviderReplay)
	if len(providerReplay) == 0 || bytes.Equal(providerReplay, []byte("null")) {
		providerReplay = nil
	} else if !json.Valid(providerReplay) {
		return ModelOutputAuthorityRecord{}, errors.New("provider replay must be valid JSON")
	}
	input.ProviderReplay = providerReplay
	q := dbsqlc.New(db)
	row, err := q.InsertModelOutputAuthority(ctx, dbsqlc.InsertModelOutputAuthorityParams{
		ModelCallContextID:      input.ModelCallContextID,
		ServedProviderModelSlug: input.ServedProviderModelSlug,
		StopReason:              string(input.StopReason),
		ProviderReplay:          sqlcRawMessageFromEmpty(input.ProviderReplay),
		ProjectID:               input.ProjectID,
		AgentID:                 input.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := q.GetModelOutputByModelContext(
			ctx,
			dbsqlc.GetModelOutputByModelContextParams{
				ProjectID:          input.ProjectID,
				AgentID:            input.AgentID,
				ModelCallContextID: input.ModelCallContextID,
			},
		)
		if getErr != nil {
			return ModelOutputAuthorityRecord{}, fmt.Errorf(
				"load model output authority: %w",
				getErr,
			)
		}
		record := modelOutputAuthorityFromGetSQLC(existing)
		if !sameModelOutputAuthorityIntent(record, input) {
			return ModelOutputAuthorityRecord{}, storeerr.ErrIdempotencyConflict
		}
		return record, nil
	}
	if err != nil {
		return ModelOutputAuthorityRecord{}, fmt.Errorf("create model output authority: %w", err)
	}
	record := modelOutputAuthorityFromSQLC(row)
	record.Usage = input.Usage
	return record, nil
}

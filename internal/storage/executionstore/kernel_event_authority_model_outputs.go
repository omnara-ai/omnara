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
	ContinueAfterTruncation bool
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
	ContinueAfterTruncation bool
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
		ContinueAfterTruncation: input.ContinueAfterTruncation,
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

// ConsecutiveOutputContinuations counts only durable output continuations before
// a captured request watermark. New inputs, tool progress, and other outputs
// reset the run; retry attempts and compaction do not advance it.
func (s *Store) ConsecutiveOutputContinuations(
	ctx context.Context,
	projectID, agentID ID,
	watermark int64,
	limit int32,
) (int, error) {
	if isNilID(projectID) || isNilID(agentID) || watermark <= 0 || limit <= 0 {
		return 0, errors.New("project, agent, positive watermark, and positive lookback limit are required")
	}
	flags, err := s.q.RecentOutputContinuationFlags(ctx, dbsqlc.RecentOutputContinuationFlagsParams{
		ProjectID: projectID, AgentID: agentID, Watermark: watermark, LookbackLimit: limit,
	})
	if err != nil {
		return 0, fmt.Errorf("load consecutive output continuations: %w", err)
	}
	count := 0
	for _, continued := range flags {
		if !continued {
			break
		}
		count++
	}
	return count, nil
}

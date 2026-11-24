package executionstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type RecordRecoveredModelCallUsageInput struct {
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	ModelCallContextID      uuid.UUID
	ProviderResponseID      string
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
}

type ModelUsageRecoveryCursor struct {
	until time.Time
}

type ModelUsageRecovery struct {
	OrgID                 uuid.UUID
	ProjectID             uuid.UUID
	AgentID               uuid.UUID
	ModelCallContextID    uuid.UUID
	ModelProviderConfigID uuid.UUID
	GenerationID          string
	Final                 bool
}

func (s *Store) RecordModelCallGenerationID(
	ctx context.Context,
	agentID, runtimeLockID, modelCallContextID uuid.UUID,
	generationID string,
) error {
	_, err := agentexecution.Retry(ctx, "record_model_call_generation_id", func(ctx context.Context) (struct{}, error) {
		unit, err := s.cell.Begin(ctx)
		if err != nil {
			return struct{}{}, err
		}
		defer func() { _ = unit.Rollback(ctx) }()
		if err := unit.RecordInflightGenerationID(ctx, agentID, runtimeLockID, modelCallContextID, generationID); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, unit.Commit(ctx, "record_model_call_generation_id")
	})
	return err
}

func (s *Store) RecordRecoveredModelCallUsage(
	ctx context.Context,
	input RecordRecoveredModelCallUsageInput,
) (bool, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ModelCallContextID == uuid.Nil {
		return false, errors.New("project, agent, and model context are required")
	}
	return agentexecution.Retry(ctx, "record_recovered_model_call_usage", func(ctx context.Context) (bool, error) {
		unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, uuid.Nil)
		if err != nil {
			return false, err
		}
		defer func() { _ = unit.Rollback(ctx) }()
		recorded, err := h.RecordRecoveredUsage(ctx, input.ModelCallContextID, agentexecution.ModelEvidence{
			ResponseID: input.ProviderResponseID,
			Usage:      input.Usage,
			Cost:       input.ProviderReportedCostUSD,
		})
		if err != nil {
			return false, err
		}
		return recorded, unit.Commit(ctx, "record_recovered_model_call_usage")
	})
}

func (s *Store) ListModelUsageRecoveries(
	ctx context.Context,
	after ModelUsageRecoveryCursor,
	lookback time.Duration,
	schedule []time.Duration,
) ([]ModelUsageRecovery, ModelUsageRecoveryCursor, error) {
	until, err := s.q.ReadModelUsageRecoveryClock(ctx)
	if err != nil {
		return nil, after, err
	}
	since := after.until
	if since.IsZero() {
		since = until.Add(-lookback)
	}
	delays := make([]int64, len(schedule))
	for i, delay := range schedule {
		delays[i] = delay.Microseconds()
	}
	rows, err := s.q.ListModelUsageRecoveries(ctx, dbsqlc.ListModelUsageRecoveriesParams{
		Until: until, After: since, DelayMicroseconds: delays,
	})
	if err != nil {
		return nil, after, err
	}
	recoveries := make([]ModelUsageRecovery, len(rows))
	for i, row := range rows {
		recoveries[i] = ModelUsageRecovery{
			OrgID:                 row.OrgID,
			ProjectID:             row.ProjectID,
			AgentID:               row.AgentID,
			ModelCallContextID:    row.ID,
			ModelProviderConfigID: row.ModelProviderConfigID,
			GenerationID:          row.ProviderResponseID,
			Final:                 row.Final,
		}
	}
	return recoveries, ModelUsageRecoveryCursor{until: until}, nil
}

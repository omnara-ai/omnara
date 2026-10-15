package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) ClaimNextModelCallContext(
	ctx context.Context,
	input ClaimNextModelCallContextInput,
) (ModelCallClaim, error) {
	return agentexecution.Retry(ctx,
		"claim_next_model_call_context",
		func(ctx context.Context) (ModelCallClaim, error) {
			unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, input.RuntimeLockID)
			if err != nil {
				return ModelCallClaim{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			prepared, err := h.ResumeModel(ctx, input.RuntimeLockID, input.PredecessorModelCallContextID)
			if err != nil {
				return ModelCallClaim{}, err
			}
			result, err := executionClaim(ctx, unit, input.ProjectID, input.AgentID, prepared)
			if err != nil {
				return ModelCallClaim{}, err
			}
			if err = unit.Commit(ctx, "claim retry"); err != nil {
				return ModelCallClaim{}, err
			}
			return result, nil
		})
}

func (s *Store) GetModelCallContext(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (ModelCallContextRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return ModelCallContextRecord{}, false, errors.New("project, agent, and model context are required")
	}
	record, err := loadModelCallContextByID(ctx, s.q, projectID, agentID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, nil
	}
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf("get model call context: %w", err)
	}
	return record, true, nil
}

func loadModelCallContextByID(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, id uuid.UUID,
) (ModelCallContextRecord, error) {
	row, err := q.GetModelCallContext(ctx, dbsqlc.GetModelCallContextParams{
		ProjectID: projectID,
		AgentID:   agentID,
		ID:        id,
	})
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	return modelCallContextRecordFromSQLC(row), nil
}

func loadModelCallContextByIDTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	projectID, agentID, id uuid.UUID,
) (ModelCallContextRecord, error) {
	return loadModelCallContextByID(ctx, dbsqlc.New(tx), projectID, agentID, id)
}

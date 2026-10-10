package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) ClaimCompactionModelCall(
	ctx context.Context,
	input ClaimCompactionModelCallInput,
) (ModelCallClaim, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return ModelCallClaim{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	result, err := h.ResumeCompaction(ctx, agentexecution.ResumeCompactionInput{
		RuntimeLockID: input.RuntimeLockID, ParentID: input.ParentContextID,
		Watermark: input.InputEventSequence, SourceEnd: input.SourceEventSequenceEnd,
	})
	if err != nil {
		return ModelCallClaim{}, err
	}
	if err := applyAdmissionDestination(ctx,
		unit,
		input.ProjectID,
		input.AgentID,
		result.Admission); err != nil {
		return ModelCallClaim{}, err
	}
	if result.Preempted {
		return ModelCallClaim{}, storeerr.ErrAgentNotAdvanceable
	}
	claim, err := executionClaim(ctx, unit, input.ProjectID, input.AgentID, result.Model)
	if err != nil {
		return ModelCallClaim{}, err
	}
	if err = unit.Commit(ctx, "claim compaction"); err != nil {
		return ModelCallClaim{}, err
	}
	return claim, nil
}

func (s *Store) ClaimNextModelCallContext(
	ctx context.Context,
	input ClaimNextModelCallContextInput,
) (ModelCallClaim, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
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

func (s *Store) GetNormalModelCallContextForFrontier(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	inputEventSequence int64,
) (ModelCallContextRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || inputEventSequence <= 0 {
		return ModelCallContextRecord{}, false, errors.New(
			"project, agent, and positive input event sequence are required",
		)
	}
	id, err := s.q.GetNormalModelCallContextByIdentity(
		ctx,
		dbsqlc.GetNormalModelCallContextByIdentityParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			InputEventSequence: inputEventSequence,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, nil
	}
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf(
			"get normal model call context for frontier: %w",
			err,
		)
	}
	record, err := loadModelCallContextByID(ctx, s.q, projectID, agentID, id)
	if err != nil {
		return ModelCallContextRecord{}, false, fmt.Errorf(
			"load normal model call context for frontier: %w",
			err,
		)
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

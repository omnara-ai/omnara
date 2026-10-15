package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ModelCallRecoveryState struct {
	HasPriorOptionalCompaction            bool
	OptionalCompactionAttemptedAtFrontier bool
	LatestObservedNormalInputTokens       int
	MinimumObservedNormalInputTokens      int
	HasPriorNormalAttempt                 bool
	CheckpointNeedsNormalAttempt          bool
}

func (s *Store) GetModelCallRecoveryState(
	ctx context.Context,
	projectID, agentID, contextID uuid.UUID,
) (ModelCallRecoveryState, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || contextID == uuid.Nil {
		return ModelCallRecoveryState{}, errors.New("project, agent, and model context are required")
	}
	row, err := s.q.GetModelCallRecoveryState(ctx, dbsqlc.GetModelCallRecoveryStateParams{
		ProjectID: projectID, AgentID: agentID, ModelCallContextID: contextID,
	})
	if err != nil {
		return ModelCallRecoveryState{}, fmt.Errorf("load model call recovery state: %w", err)
	}
	return ModelCallRecoveryState{
		HasPriorOptionalCompaction:            row.HasPriorOptionalCompaction,
		OptionalCompactionAttemptedAtFrontier: row.OptionalCompactionAttemptedAtFrontier,
		LatestObservedNormalInputTokens:       int(row.LatestObservedNormalInputTokens),
		MinimumObservedNormalInputTokens:      int(row.MinimumObservedNormalInputTokens),
		HasPriorNormalAttempt:                 row.HasPriorNormalAttempt,
		CheckpointNeedsNormalAttempt:          row.CheckpointNeedsNormalAttempt,
	}, nil
}

type ModelCallRetryState struct {
	RetryCount         int
	ParentRecoveryKind ModelCallRecoveryKind
}

func (s *Store) GetModelCallRetryState(
	ctx context.Context,
	projectID, agentID, contextID uuid.UUID,
) (ModelCallRetryState, error) {
	return getModelCallRetryStateTx(ctx, s.q, projectID, agentID, contextID)
}

func getModelCallRetryStateTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, contextID uuid.UUID,
) (ModelCallRetryState, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || contextID == uuid.Nil {
		return ModelCallRetryState{}, errors.New("project, agent, and model context are required")
	}
	row, err := q.GetModelCallRetryState(ctx, dbsqlc.GetModelCallRetryStateParams{
		ProjectID: projectID, AgentID: agentID, ModelCallContextID: contextID,
	})
	if err != nil {
		return ModelCallRetryState{}, fmt.Errorf("load model call retry state: %w", err)
	}
	return ModelCallRetryState{
		RetryCount:         int(row.RetryCount),
		ParentRecoveryKind: ModelCallRecoveryKind(row.ParentRecoveryKind),
	}, nil
}

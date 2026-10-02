package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type ModelCallRecoveryState struct {
	RecoveryCheckpointID                  uuid.UUID
	RecoveryCheckpointRetainedBytes       *int
	CheckpointRecompressionAttempted      bool
	RetryCount                            int
	ParentRecoveryKind                    ModelCallRecoveryKind
	LastOptionalContextID                 uuid.UUID
	LastOptionalInputTargetTokens         int
	LastOptionalCompactionNeedsHeadroom   bool
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
	return getModelCallRecoveryStateTx(ctx, s.q, projectID, agentID, contextID)
}

func getModelCallRecoveryStateTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, contextID uuid.UUID,
) (ModelCallRecoveryState, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || contextID == uuid.Nil {
		return ModelCallRecoveryState{}, errors.New("project, agent, and model context are required")
	}
	row, err := q.GetModelCallRecoveryState(ctx, dbsqlc.GetModelCallRecoveryStateParams{
		ProjectID: projectID, AgentID: agentID, ModelCallContextID: contextID,
	})
	if err != nil {
		return ModelCallRecoveryState{}, fmt.Errorf("load model call recovery state: %w", err)
	}
	return ModelCallRecoveryState{
		RecoveryCheckpointID:                  storeutil.IDFromPtr(row.RecoveryCheckpointID),
		RecoveryCheckpointRetainedBytes:       intFromInt32Ptr(row.RecoveryCheckpointRetainedBytes),
		CheckpointRecompressionAttempted:      row.CheckpointRecompressionAttempted,
		RetryCount:                            int(row.RetryCount),
		ParentRecoveryKind:                    ModelCallRecoveryKind(row.ParentRecoveryKind),
		LastOptionalContextID:                 storeutil.IDFromPtr(row.LastOptionalContextID),
		LastOptionalInputTargetTokens:         int(row.LastOptionalInputTargetTokens),
		LastOptionalCompactionNeedsHeadroom:   row.LastOptionalCompactionNeedsHeadroom,
		OptionalCompactionAttemptedAtFrontier: row.OptionalCompactionAttemptedAtFrontier,
		LatestObservedNormalInputTokens:       int(row.LatestObservedNormalInputTokens),
		MinimumObservedNormalInputTokens:      int(row.MinimumObservedNormalInputTokens),
		HasPriorNormalAttempt:                 row.HasPriorNormalAttempt,
		CheckpointNeedsNormalAttempt:          row.CheckpointNeedsNormalAttempt,
	}, nil
}

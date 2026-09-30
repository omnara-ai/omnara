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
	OutputAllowanceRestored               bool
	RecoveryCheckpointID                  uuid.UUID
	RecoveryCheckpointRetainedBytes       *int
	CheckpointRecompressionAttempted      bool
	NormalRetryCount                      int
	CompactionRetryCount                  int
	ParentRecoveryKind                    ModelCallRecoveryKind
	LastOptionalContextID                 uuid.UUID
	LastOptionalInputTargetTokens         int
	LastOptionalCompactionNeedsHeadroom   bool
	OptionalCompactionAttemptedAtFrontier bool
	LatestObservedNormalInputTokens       int
	ProviderAttemptCount                  int
	RecoveryMaxOutputTokens               *int
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
	state := ModelCallRecoveryState{
		OutputAllowanceRestored:          row.OutputAllowanceRestored,
		RecoveryCheckpointID:             storeutil.IDFromPtr(row.RecoveryCheckpointID),
		RecoveryCheckpointRetainedBytes:  intFromInt32Ptr(row.RecoveryCheckpointRetainedBytes),
		CheckpointRecompressionAttempted: row.CheckpointRecompressionAttempted,
		NormalRetryCount:                 int(row.NormalRetryCount), CompactionRetryCount: int(row.CompactionRetryCount),
		ParentRecoveryKind:                    ModelCallRecoveryKind(row.ParentRecoveryKind),
		LastOptionalContextID:                 storeutil.IDFromPtr(row.LastOptionalContextID),
		LastOptionalInputTargetTokens:         int(row.LastOptionalInputTargetTokens),
		LastOptionalCompactionNeedsHeadroom:   row.LastOptionalCompactionNeedsHeadroom,
		OptionalCompactionAttemptedAtFrontier: row.OptionalCompactionAttemptedAtFrontier,
		LatestObservedNormalInputTokens:       int(row.LatestObservedNormalInputTokens),
		ProviderAttemptCount:                  int(row.ProviderAttemptCount),
		CheckpointNeedsNormalAttempt:          row.CheckpointNeedsNormalAttempt,
	}
	if row.RecoveryMaxOutputTokens > 0 {
		limit := int(row.RecoveryMaxOutputTokens)
		state.RecoveryMaxOutputTokens = &limit
	}
	return state, nil
}

func (s *Store) HasObservedInputHeadroomSince(
	ctx context.Context,
	projectID, agentID, optionalContextID, configuredModelRevisionID uuid.UUID,
	maxInputTokens int,
) (bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || optionalContextID == uuid.Nil ||
		configuredModelRevisionID == uuid.Nil || maxInputTokens <= 0 || int64(maxInputTokens) > 2147483647 {
		return false, errors.New("project, agent, optional context, revision, and positive input threshold are required")
	}
	return s.q.HasObservedInputHeadroomSince(ctx, dbsqlc.HasObservedInputHeadroomSinceParams{
		ProjectID: projectID, AgentID: agentID, AfterOptionalContextID: optionalContextID,
		ConfiguredModelRevisionID: configuredModelRevisionID, MaxInputTokens: int32(maxInputTokens),
	})
}

package modelcontext

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type preparedContextStore struct {
	Store
	data *executionstore.ModelContextData
}

func (s preparedContextStore) GetLatestApplicableContextCheckpoint(
	_ context.Context, _, _ uuid.UUID, _ int64,
) (executionstore.ContextCheckpointRecord, bool, error) {
	if s.data.Checkpoint == nil {
		return executionstore.ContextCheckpointRecord{}, false, nil
	}
	return *s.data.Checkpoint, true, nil
}

func (s preparedContextStore) IsOutputLimitBoundary(
	ctx context.Context, projectID, agentID uuid.UUID, sequence int64,
) (bool, error) {
	if s.data.Checkpoint != nil && sequence == s.data.Checkpoint.SummarizedThroughEventSequence {
		return s.data.CheckpointEndsWithOutputLimit, nil
	}
	return s.Store.IsOutputLimitBoundary(ctx, projectID, agentID, sequence)
}

func (s preparedContextStore) ListContextEvents(
	_ context.Context, _, _ uuid.UUID, after, watermark int64, limit int32,
) ([]executionstore.ContextEventRecord, error) {
	var result []executionstore.ContextEventRecord
	for _, event := range s.data.Events {
		if event.Sequence > after && event.Sequence <= watermark {
			result = append(result, event)
			if len(result) == int(limit) {
				break
			}
		}
	}
	return result, nil
}

func (s preparedContextStore) ListCompletedToolCallsAtWatermark(
	_ context.Context, _, _ uuid.UUID, _, _ int64,
) ([]executionstore.ToolCallRecord, error) {
	return s.data.ToolCalls, nil
}

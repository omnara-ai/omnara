package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ModelContextData struct {
	ProjectID                         uuid.UUID
	AgentID                           uuid.UUID
	InputEventSequence                int64
	Checkpoint                        *ContextCheckpointRecord
	CheckpointEndsWithOutputLimit     bool
	Events                            []ContextEventRecord
	ToolCalls                         []ToolCallRecord
	ProviderReplayCutoffEventSequence int64
}

func loadModelContextDataTx(
	ctx context.Context, q *dbsqlc.Queries, model ModelCallContextRecord,
) (*ModelContextData, error) {
	result := &ModelContextData{
		ProjectID: model.ProjectID, AgentID: model.AgentID, InputEventSequence: model.InputEventSequence,
	}
	checkpoint, err := q.GetLatestApplicableContextCheckpoint(ctx, dbsqlc.GetLatestApplicableContextCheckpointParams{
		ProjectID: model.ProjectID, AgentID: model.AgentID, MaxEventSequence: model.InputEventSequence,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load model checkpoint: %w", err)
	}
	after := int64(0)
	if err == nil {
		record := contextCheckpointRecordFromLatestSQLC(checkpoint)
		result.Checkpoint = &record
		after = record.SummarizedThroughEventSequence
		result.CheckpointEndsWithOutputLimit, err = q.IsOutputLimitBoundary(ctx, dbsqlc.IsOutputLimitBoundaryParams{
			ProjectID: model.ProjectID, AgentID: model.AgentID, EventSequence: after,
		})
		if err != nil {
			return nil, fmt.Errorf("load checkpoint boundary: %w", err)
		}
	}
	for cursor := after; ; {
		rows, err := q.ListContextEvents(ctx, dbsqlc.ListContextEventsParams{
			ProjectID: model.ProjectID, AgentID: model.AgentID,
			AfterSequence: cursor, Watermark: model.InputEventSequence, PageLimit: 500,
		})
		if err != nil {
			return nil, fmt.Errorf("load model transcript: %w", err)
		}
		result.Events = append(result.Events, contextEventsFromSQLC(rows, model.ProjectID, model.AgentID)...)
		if len(rows) < 500 {
			break
		}
		cursor = rows[len(rows)-1].Sequence
		if cursor >= model.InputEventSequence {
			break
		}
	}
	tools, err := q.ListCompletedToolCallsAtWatermark(ctx, dbsqlc.ListCompletedToolCallsAtWatermarkParams{
		ProjectID: model.ProjectID, AgentID: model.AgentID,
		AfterEventSequence: after, MaxEventSequence: model.InputEventSequence,
	})
	if err != nil {
		return nil, fmt.Errorf("load model tool results: %w", err)
	}
	for _, row := range tools {
		result.ToolCalls = append(result.ToolCalls, toolCallRecordFromWatermarkSQLC(row))
	}
	result.ProviderReplayCutoffEventSequence, err = q.GetProviderReplaySuppressionCutoff(ctx,
		dbsqlc.GetProviderReplaySuppressionCutoffParams{
			ProjectID: model.ProjectID, AgentID: model.AgentID, ModelCallContextID: model.ID,
		})
	if err != nil {
		return nil, fmt.Errorf("load model replay policy: %w", err)
	}
	return result, nil
}

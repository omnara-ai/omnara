package storagetest

import (
	"context"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func ListCompletedToolCallsForTurn(
	ctx context.Context,
	store *storage.Store,
	projectID, agentID, turnID storage.ID,
) ([]executionstore.ToolCallRecord, error) {
	watermark, err := store.Execution().MaxEventSequence(ctx, projectID, agentID)
	if err != nil {
		return nil, err
	}
	records, err := store.Execution().ListCompletedToolCallsAtWatermark(
		ctx,
		projectID,
		agentID,
		0,
		watermark,
	)
	if err != nil {
		return nil, err
	}
	filtered := make([]executionstore.ToolCallRecord, 0, len(records))
	for _, record := range records {
		if record.TurnID == turnID {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

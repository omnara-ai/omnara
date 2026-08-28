package executionstore

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const missingAgentWakeupRebuildBatchSize int32 = 500

func (s *Store) RebuildMissingAgentWakeups(
	ctx context.Context,
	projectID ID,
) (int64, error) {
	return s.rebuildMissingAgentWakeups(ctx, &projectID)
}

func (s *Store) rebuildMissingAgentWakeups(
	ctx context.Context,
	projectID *ID,
) (int64, error) {
	metadata, err := marshalJSON(map[string]any{"reason": "maintenance_rebuild"})
	if err != nil {
		return 0, fmt.Errorf("marshal wakeup rebuild metadata: %w", err)
	}
	var total int64
	var afterAgentID *ID
	for {
		rows, batchErr := s.q.RebuildMissingAgentWakeupsBatch(
			ctx,
			dbsqlc.RebuildMissingAgentWakeupsBatchParams{
				ProjectID:    projectID,
				AfterAgentID: afterAgentID,
				BatchLimit:   missingAgentWakeupRebuildBatchSize,
				Metadata:     metadata,
			},
		)
		if batchErr != nil {
			return total, fmt.Errorf("rebuild missing agent wakeups batch: %w", batchErr)
		}
		for _, row := range rows {
			if row.Inserted {
				total++
			}
		}
		if len(rows) < int(missingAgentWakeupRebuildBatchSize) {
			return total, nil
		}
		lastID := rows[len(rows)-1].AgentID
		afterAgentID = &lastID
	}
}

func (s *Store) RebuildMissingAgentWakeupsForAllProjects(
	ctx context.Context,
) (int64, error) {
	return s.rebuildMissingAgentWakeups(ctx, nil)
}

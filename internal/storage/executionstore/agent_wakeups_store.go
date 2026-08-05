package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const missingAgentWakeupRebuildBatchSize int32 = 500

func (s *Store) RebuildMissingAgentWakeups(
	ctx context.Context,
	projectID ID,
) (int64, error) {
	metadata, err := marshalJSON(map[string]any{"reason": "maintenance_rebuild"})
	if err != nil {
		return 0, fmt.Errorf("marshal wakeup rebuild metadata: %w", err)
	}
	var total int64
	var afterAgentID *ID
	for {
		tx, beginErr := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if beginErr != nil {
			return total, fmt.Errorf("begin missing agent wakeup rebuild batch: %w", beginErr)
		}
		rows, batchErr := dbsqlc.New(tx).RebuildMissingAgentWakeupsBatch(
			ctx,
			dbsqlc.RebuildMissingAgentWakeupsBatchParams{
				ProjectID:    projectID,
				AfterAgentID: afterAgentID,
				BatchLimit:   missingAgentWakeupRebuildBatchSize,
				Metadata:     metadata,
			},
		)
		if batchErr == nil {
			batchErr = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
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
	projectIDs, err := s.q.ListProjectIDsForMaintenance(ctx)
	if err != nil {
		return 0, fmt.Errorf("list projects for wakeup rebuild: %w", err)
	}
	var total int64
	var rebuildErrs []error
	for _, projectID := range projectIDs {
		count, err := s.RebuildMissingAgentWakeups(ctx, projectID)
		if err != nil {
			rebuildErrs = append(rebuildErrs, fmt.Errorf("project %s: %w", projectID, err))
			continue
		}
		total += count
	}
	return total, errors.Join(rebuildErrs...)
}

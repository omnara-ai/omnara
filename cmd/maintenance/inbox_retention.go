package main

import (
	"context"
	"time"
)

const (
	integrationInboxCleanupBudget  = 250 * time.Millisecond
	integrationInboxCleanupTimeout = 5 * time.Second
)

// Each batch commits separately. The soft budget stops new batches without
// canceling healthy SQL. The hard deadline bounds a stalled pass and returns an
// error. The bool reports normal soft-budget exhaustion.
func drainIntegrationInboxCleanup(
	ctx context.Context,
	cleanup func(context.Context) (int64, error),
) (int64, bool, error) {
	stopAt := time.Now().Add(integrationInboxCleanupBudget)
	cleanupCtx, cancel := context.WithTimeout(ctx, integrationInboxCleanupTimeout)
	defer cancel()
	var total int64
	for {
		if err := cleanupCtx.Err(); err != nil {
			return total, false, err
		}
		if !time.Now().Before(stopAt) {
			return total, true, nil
		}
		count, err := cleanup(cleanupCtx)
		total += count
		if err != nil {
			return total, false, err
		}
		if count < integrationInboxCleanupBatch {
			return total, false, nil
		}
	}
}

package maintenance

import (
	"context"
	"time"
)

const (
	AppInboxRetention      = 7 * 24 * time.Hour
	appInboxCleanupBatch   = 100
	appInboxCleanupBudget  = 250 * time.Millisecond
	appInboxCleanupTimeout = 5 * time.Second
)

func drainAppInboxCleanup(
	ctx context.Context,
	cleanup func(context.Context) (int64, error),
) (int64, bool, error) {
	stopAt := time.Now().Add(appInboxCleanupBudget)
	cleanupCtx, cancel := context.WithTimeout(ctx, appInboxCleanupTimeout)
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
		if count < appInboxCleanupBatch {
			return total, false, nil
		}
	}
}

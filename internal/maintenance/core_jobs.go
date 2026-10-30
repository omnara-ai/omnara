package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
)

const (
	runtimeLockReapBatchSize             int32 = 100
	daemonRuntimeExpiryBatchSize         int32 = 100
	eventWebhookDeliveryCleanupBatchSize int32 = 500
	eventWebhookDeliveryCleanupTimeout         = 5 * time.Second
)

func reapRuntimeLocks(
	log *slog.Logger,
	reap func(context.Context, int32) (int64, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		count, err := reap(ctx, runtimeLockReapBatchSize)
		return reportMaintenanceCount(ctx, log, "reap expired runtime locks", "reaped expired runtime locks", count, err)
	}
}

func expireDaemonRuntimes(
	log *slog.Logger,
	expire func(context.Context, int32) ([]executionstore.DaemonRuntimeRecord, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		records, err := expire(ctx, daemonRuntimeExpiryBatchSize)
		return reportMaintenanceCount(
			ctx, log, "expire daemon runtimes", "expired daemon runtimes", int64(len(records)), err,
		)
	}
}

func expireProcessTools(
	log *slog.Logger,
	expire func(context.Context, time.Duration) (int64, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		count, err := expire(ctx, executionstore.ProcessToolMachineUnreachableGrace)
		return reportMaintenanceCount(ctx, log, "expire process tool calls", "expired process tool calls", count, err)
	}
}

func deleteExpiredWebhookDeliveries(
	log *slog.Logger,
	deleteExpired func(context.Context, int32) (int64, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		cleanupCtx, cancel := context.WithTimeout(ctx, eventWebhookDeliveryCleanupTimeout)
		defer cancel()
		count, err := deleteExpired(cleanupCtx, eventWebhookDeliveryCleanupBatchSize)
		result := reportMaintenanceCount(
			ctx, log, "cleanup expired event webhook deliveries", "cleaned expired event webhook deliveries", count, err,
		)
		result.more = count >= int64(eventWebhookDeliveryCleanupBatchSize)
		return result
	}
}

func cleanupAuthState(
	log *slog.Logger,
	cleanup func(context.Context) (identitystore.AuthStateCleanupResult, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		result, err := cleanup(ctx)
		outcome := completedMaintenanceOutcome(ctx, err)
		worked := result != (identitystore.AuthStateCleanupResult{})
		if outcome.err != nil {
			log.Error("cleanup inactive auth state", "error", outcome.err)
		} else if !outcome.interrupted && worked {
			log.Info(
				"cleaned inactive auth state",
				"deleted_inactive_tokens", result.DeletedInactiveTokens,
				"deleted_browser_sessions", result.DeletedBrowserSessions,
				"deleted_abandoned_users", result.DeletedAbandonedUsers,
				"deleted_device_flows", result.DeletedDeviceFlows,
				"deleted_oauth_codes", result.DeletedOAuthCodes,
				"deleted_oauth_tokens", result.DeletedOAuthTokens,
			)
		}
		return maintenanceResult{worked: worked, more: result.FilledBatch(), err: outcome.err}
	}
}

func cleanupIntegrationStates(
	log *slog.Logger,
	cleanup func(context.Context, time.Duration, int) (int64, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		count, err := cleanup(ctx, integrationInboxRetention, integrationInboxCleanupBatch)
		result := reportMaintenanceCount(ctx, log, "cleanup integration states", "cleaned integration states", count, err)
		result.more = count >= integrationInboxCleanupBatch
		return result
	}
}

func cleanupCompletedInbox(
	log *slog.Logger,
	cleanup func(context.Context, time.Duration, int) (int64, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		count, exhausted, err := drainIntegrationInboxCleanup(
			ctx, func(cleanupCtx context.Context) (int64, error) {
				return cleanup(cleanupCtx, integrationInboxRetention, integrationInboxCleanupBatch)
			},
		)
		outcome := completedMaintenanceOutcome(ctx, err)
		worked := count > 0 || exhausted
		if outcome.err != nil {
			log.Error("cleanup completed integration inbox", "count", count, "error", outcome.err)
		} else if !outcome.interrupted && worked {
			log.Info("cleaned completed integration inbox", "count", count,
				"retention", integrationInboxRetention, "budget_exhausted", exhausted)
		}
		return maintenanceResult{worked: worked, more: exhausted, err: outcome.err}
	}
}

func cleanupDeletedInbox(
	log *slog.Logger,
	cleanup func(context.Context, int) (int64, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		count, exhausted, err := drainIntegrationInboxCleanup(
			ctx, func(cleanupCtx context.Context) (int64, error) {
				return cleanup(cleanupCtx, integrationInboxCleanupBatch)
			},
		)
		outcome := completedMaintenanceOutcome(ctx, err)
		worked := count > 0 || exhausted
		if outcome.err != nil {
			log.Error("cleanup deleted integration inbox", "count", count, "error", outcome.err)
		} else if !outcome.interrupted && worked {
			log.Info("cleaned deleted integration inbox", "count", count, "budget_exhausted", exhausted)
		}
		return maintenanceResult{worked: worked, more: exhausted, err: outcome.err}
	}
}

func reportMaintenanceCount(
	ctx context.Context,
	log *slog.Logger,
	failure, success string,
	count int64,
	err error,
) maintenanceResult {
	logent.MaintenanceJobCount(ctx, count)
	outcome := completedMaintenanceOutcome(ctx, err)
	if outcome.err != nil {
		log.Error(failure, "error", outcome.err)
	} else if !outcome.interrupted && count > 0 {
		log.Info(success, "count", count)
	}
	return maintenanceResult{worked: count > 0, err: outcome.err}
}

func cleanupMemoryFiles(
	log *slog.Logger,
	cleanup func(context.Context) (memorystore.FileCleanupResult, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		result, err := cleanup(ctx)
		outcome := completedMaintenanceOutcome(ctx, err)
		worked := result != (memorystore.FileCleanupResult{})
		if outcome.err != nil {
			log.Error(
				"cleanup memory files",
				"removed_stores", result.RemovedStores,
				"discarded_staged_files", result.DiscardedStagedFiles,
				"error", outcome.err,
			)
		} else if !outcome.interrupted && worked {
			log.Info(
				"cleaned memory files",
				"removed_stores", result.RemovedStores,
				"discarded_staged_files", result.DiscardedStagedFiles,
			)
		}
		return maintenanceResult{worked: worked, err: outcome.err}
	}
}

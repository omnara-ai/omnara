package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

func reconcileProviderRuntimes(
	log *slog.Logger,
	recorder *metrics.ProviderRuntimeRecorder,
	operation metrics.ProviderRuntimeOperation,
	run func(context.Context) (machinepool.RuntimeReconciliationStats, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		started := time.Now()
		stats, err := runProviderRuntimeMaintenanceTick(ctx, log, operation, run)
		outcome := completedMaintenanceOutcome(ctx, err)
		recordProviderRuntimePass(recorder, operation, stats, outcome, time.Since(started))
		worked := stats.Targets > 0 || stats.MarkersSet > 0 || stats.DeletionClaims > 0
		if outcome.err != nil {
			log.Error("provider runtime reconciliation", "operation", operation, "error", outcome.err, "stats", stats)
		} else if !outcome.interrupted && worked {
			log.Info("provider runtime reconciliation", "operation", operation, "stats", stats)
		}
		return maintenanceResult{worked: worked, err: outcome.err, retryScheduled: stats.OnlyCooldownFailures()}
	}
}

func runProviderRuntimeMaintenanceTick(
	ctx context.Context,
	log *slog.Logger,
	operation metrics.ProviderRuntimeOperation,
	run func(context.Context) (machinepool.RuntimeReconciliationStats, error),
) (stats machinepool.RuntimeReconciliationStats, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("provider runtime %s reconciliation panicked: %v", operation, recovered)
			log.Error(
				"provider runtime reconciliation tick panicked",
				"operation", operation,
				"error", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()
	return run(ctx)
}

func recordProviderRuntimePass(
	recorder *metrics.ProviderRuntimeRecorder,
	operation metrics.ProviderRuntimeOperation,
	stats machinepool.RuntimeReconciliationStats,
	outcome maintenanceOutcome,
	duration time.Duration,
) {
	result := providerRuntimeResult(outcome)
	recorder.RecordPass(operation, result, duration)
	for event, count := range map[metrics.ProviderRuntimeEvent]int{
		metrics.ProviderRuntimeEventPages:                 stats.Pages,
		metrics.ProviderRuntimeEventScopes:                stats.Scopes,
		metrics.ProviderRuntimeEventScopeCooldownSkips:    stats.ScopesSkipped,
		metrics.ProviderRuntimeEventTargets:               stats.Targets,
		metrics.ProviderRuntimeEventObservations:          stats.Observed,
		metrics.ProviderRuntimeEventRunning:               stats.Running,
		metrics.ProviderRuntimeEventInactive:              stats.Inactive,
		metrics.ProviderRuntimeEventTransitional:          stats.Transitional,
		metrics.ProviderRuntimeEventTerminated:            stats.Terminated,
		metrics.ProviderRuntimeEventUnknown:               stats.Unknown,
		metrics.ProviderRuntimeEventProviderErrors:        stats.ProviderErrors,
		metrics.ProviderRuntimeEventMarkersSet:            stats.MarkersSet,
		metrics.ProviderRuntimeEventMarkersCleared:        stats.MarkersCleared,
		metrics.ProviderRuntimeEventWakeAttemptsCleared:   stats.WakeAttemptsCleared,
		metrics.ProviderRuntimeEventConfirmations:         stats.Confirmations,
		metrics.ProviderRuntimeEventDeletionClaims:        stats.DeletionClaims,
		metrics.ProviderRuntimeEventDeletionClaimsSkipped: stats.DeletionClaimsSkipped,
	} {
		recorder.RecordEvents(operation, event, count)
	}
}

func providerRuntimeResult(outcome maintenanceOutcome) metrics.ProviderRuntimeResult {
	if outcome.err != nil {
		return metrics.ProviderRuntimeResultError
	}
	if outcome.interrupted {
		return metrics.ProviderRuntimeResultCanceled
	}
	return metrics.ProviderRuntimeResultSuccess
}

func provisionPoolMachines(
	log *slog.Logger,
	machinePoolManager *machinepool.Manager,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		provisioned, err := machinePoolManager.ReconcileProvisioning(ctx, machinepool.DefaultReconcileBatchSize)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("reconcile machine provisioning", "attempted_count", provisioned, "error", outcome.err)
		} else if !outcome.interrupted && provisioned > 0 {
			log.Info("attempted machine provisioning reconcile", "attempted_count", provisioned)
		}
		return maintenanceResult{worked: provisioned > 0, err: outcome.err}
	}
}

func cleanupPoolMachines(
	log *slog.Logger,
	machinePoolManager *machinepool.Manager,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		cleaned, err := machinePoolManager.ReconcileCleanup(ctx, machinepool.DefaultReconcileBatchSize)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("reconcile machine cleanup", "attempted_count", cleaned, "error", outcome.err)
		} else if !outcome.interrupted && cleaned > 0 {
			log.Info("attempted machine cleanup reconcile", "attempted_count", cleaned)
		}
		return maintenanceResult{worked: cleaned > 0, err: outcome.err}
	}
}

func deleteIdlePoolMachines(
	log *slog.Logger,
	reconcile func(context.Context, int32) (int, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		candidateCount, err := reconcile(ctx, machinepool.DefaultReconcileBatchSize)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("reconcile idle machine deletion", "candidate_count", candidateCount, "error", outcome.err)
		} else if !outcome.interrupted && candidateCount > 0 {
			log.Info("reconciled idle machine deletion", "candidate_count", candidateCount)
		}
		return maintenanceResult{
			worked: candidateCount > 0,
			more:   candidateCount >= machinepool.DefaultReconcileBatchSize,
			err:    outcome.err,
		}
	}
}

func deleteDeniedPoolMachines(
	log *slog.Logger,
	machinePoolManager *machinepool.Manager,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		candidateCount, err := machinePoolManager.ReconcileManagedWorkDeniedDeletion(
			ctx,
			log,
			machinepool.DefaultReconcileBatchSize,
		)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("reconcile managed work denied machine deletion", "candidate_count", candidateCount, "error", outcome.err)
		} else if !outcome.interrupted && candidateCount > 0 {
			log.Info("reconciled managed work denied machine deletion", "candidate_count", candidateCount)
		}
		return maintenanceResult{
			worked: candidateCount > 0,
			more:   candidateCount >= machinepool.DefaultReconcileBatchSize,
			err:    outcome.err,
		}
	}
}

func archiveIdleAgents(
	log *slog.Logger,
	archive func(context.Context, int) ([]executionstore.MachineRecord, int, bool, error),
	deleteMachines func(context.Context, []executionstore.MachineRecord) (int, error),
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		machines, archived, full, err := archive(ctx, machinepool.DefaultReconcileBatchSize)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("archive idle agents", "archived_count", archived, "error", outcome.err)
		} else if !outcome.interrupted && archived > 0 {
			log.Info("archived idle agents", "archived_count", archived)
		}
		result := maintenanceResult{worked: archived > 0, more: full, err: outcome.err}
		if len(machines) > 0 {
			_, err = deleteMachines(ctx, machines)
			if err = completedMaintenanceOutcome(ctx, err).err; err != nil {
				log.Error("delete idle agent machines", "machine_count", len(machines), "error", err)
				result.retryScheduled = result.err == nil
				result.err = errors.Join(result.err, err)
			}
		}
		return result
	}
}

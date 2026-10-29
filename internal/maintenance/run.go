package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/defaultprovider"
	"github.com/omnara-ai/omnara/internal/errutil"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	providerRuntimeDiscoveryInterval = 5 * time.Minute
	providerRuntimeRecheckInterval   = 30 * time.Second
	idleMachineReconcileInterval     = time.Minute
	deniedMachineReconcileInterval   = time.Hour
	memoryCleanupInterval            = time.Hour
	expiredDataCleanupInterval       = 5 * time.Minute
	webhookDeliveryCleanupInterval   = time.Minute
	idleAgentArchiveInterval         = time.Minute
)

type Options struct {
	Log                  *slog.Logger
	Store                *storage.Store
	MachinePools         *machinepool.Manager
	Metrics              *metrics.Set
	Interval             time.Duration
	DefaultModelProvider *defaultprovider.Runner
}

// Run runs every maintenance job on its own schedule until ctx ends.
func Run(ctx context.Context, options Options) {
	log, interval, machinePools := options.Log, options.Interval, options.MachinePools
	execution := options.Store.Execution()
	integrations := options.Store.Integrations()
	runtimeRecorder := metrics.NewProviderRuntimeRecorder(options.Metrics)
	jobs := []maintenanceJob{
		{"reap_runtime_locks", interval, reapRuntimeLocks(log, execution.ReapExpiredAgentRuntimeLocks)},
		{"expire_daemon_runtimes", interval, expireDaemonRuntimes(log, execution.EndExpiredDaemonRuntimes)},
		{"expire_process_tools", interval, expireProcessTools(log, execution.ExpireProcessToolCallsForAllProjects)},
		{"delete_expired_webhook_deliveries", webhookDeliveryCleanupInterval,
			deleteExpiredWebhookDeliveries(log, execution.DeleteExpiredEventWebhookDeliveries)},
		{"cleanup_auth_state", expiredDataCleanupInterval,
			cleanupAuthState(log, options.Store.Identity().CleanupInactiveAuthState)},
		{"cleanup_integration_states", expiredDataCleanupInterval,
			cleanupIntegrationStates(log, integrations.CleanupIntegrationStates)},
		{"cleanup_completed_inbox", expiredDataCleanupInterval,
			cleanupCompletedInbox(log, integrations.CleanupTerminalIntegrationInbox)},
		{"cleanup_deleted_inbox", expiredDataCleanupInterval,
			cleanupDeletedInbox(log, integrations.CleanupDeletedIntegrationInbox)},
		{"cleanup_memory_files", memoryCleanupInterval, cleanupMemoryFiles(log, options.Store.Memories().CleanupFiles)},
		{"provision_pool_machines", interval, provisionPoolMachines(log, machinePools)},
		{"cleanup_pool_machines", interval, cleanupPoolMachines(log, machinePools)},
		{"delete_idle_pool_machines", idleMachineReconcileInterval,
			deleteIdlePoolMachines(log, machinePools.ReconcileIdleDeletion)},
		{"delete_denied_pool_machines", deniedMachineReconcileInterval, deleteDeniedPoolMachines(log, machinePools)},
		{"archive_idle_agents", idleAgentArchiveInterval, archiveIdleAgents(log, execution, machinePools)},
		{"discover_provider_runtimes", providerRuntimeDiscoveryInterval, reconcileProviderRuntimes(
			log, runtimeRecorder, metrics.ProviderRuntimeOperationDiscovery,
			func(ctx context.Context) (machinepool.RuntimeReconciliationStats, error) {
				return machinePools.DiscoverProviderRuntimeMismatches(ctx, machinepool.RuntimeReconciliationConfig{})
			},
		)},
		{"confirm_provider_runtimes", providerRuntimeRecheckInterval, reconcileProviderRuntimes(
			log, runtimeRecorder, metrics.ProviderRuntimeOperationConfirmation,
			func(ctx context.Context) (machinepool.RuntimeReconciliationStats, error) {
				return machinePools.ConfirmProviderRuntimeMismatches(ctx, machinepool.RuntimeReconciliationConfig{})
			},
		)},
		{"sample_execution_heads", executionHeadInterval, sampleExecutionHeads(
			log, metrics.NewJobRecorder(options.Metrics, metrics.SubsystemMaintenance), execution.SampleExecutionHeads,
		)},
		{"sample_discord_demand", discordRuntimeMetricsInterval, recordDiscordRuntimeDemand(
			log, integrations.CountUnclaimedDiscordIntegrations, metrics.NewDiscordRuntimeDemandRecorder(options.Metrics),
		)},
		{"sample_agent_backlog", agentBacklogInterval, recordAgentBacklog(
			log, execution.SampleReadyAgentBacklog, metrics.NewAgentBacklogRecorder(options.Metrics),
		)},
	}
	if options.DefaultModelProvider != nil {
		jobs = append(jobs, maintenanceJob{
			"provision_default_model_provider", interval,
			provisionDefaultModelProviders(log, options.DefaultModelProvider),
		})
	}
	runMaintenanceJobs(ctx, log, interval, jobs)
}

type maintenanceJob struct {
	name     string
	interval time.Duration
	run      func(context.Context) (more bool)
}

func runMaintenanceJobs(ctx context.Context, log *slog.Logger, busyInterval time.Duration, jobs []maintenanceJob) {
	var running sync.WaitGroup
	for _, job := range jobs {
		running.Go(func() { runMaintenanceJob(ctx, log, busyInterval, job) })
	}
	running.Wait()
}

func runMaintenanceJob(ctx context.Context, log *slog.Logger, busyInterval time.Duration, job maintenanceJob) {
	for ctx.Err() == nil {
		delay := jitteredMaintenanceDelay(job.interval)
		if runMaintenanceJobOnce(ctx, log, job) {
			delay = min(delay, busyInterval)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func runMaintenanceJobOnce(ctx context.Context, log *slog.Logger, job maintenanceJob) (more bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Error("maintenance job panicked", "job", job.name, "error", recovered, "stack", string(debug.Stack()))
		}
	}()
	return job.run(ctx)
}

func jitteredMaintenanceDelay(interval time.Duration) time.Duration {
	spread := interval / 10
	if spread <= 0 {
		return interval
	}
	return interval - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}

type maintenanceOutcome struct {
	interrupted bool
	err         error
}

func completedMaintenanceOutcome(ctx context.Context, err error) maintenanceOutcome {
	outcome := maintenanceOutcome{
		interrupted: errors.Is(ctx.Err(), context.Canceled),
		err:         err,
	}
	if outcome.interrupted && errutil.OnlyMatches(err, context.Canceled) {
		outcome.err = nil
	}
	return outcome
}

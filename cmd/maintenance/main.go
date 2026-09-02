package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/omnara-ai/omnara/internal/config"
	"github.com/omnara-ai/omnara/internal/defaultprovider"
	"github.com/omnara-ai/omnara/internal/errutil"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	runtimeLockReapBatchSize         int32 = 100
	providerRuntimeDiscoveryInterval       = 5 * time.Minute
	providerRuntimeRecheckInterval         = 30 * time.Second
	idleMachineReconcileInterval           = time.Minute
)

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

func main() {
	logger := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	if err := cfg.ValidateMaintenance(); err != nil {
		logger.Error("validate maintenance config", "error", err)
		os.Exit(1)
	}

	metricSet := metrics.New()
	db, err := storage.Open(
		context.Background(),
		cfg.DatabaseURL,
		storage.WithDefaultApplicationName("omnara-maintenance"),
		storage.WithQueryTracer(metrics.NewDBRecorder(metricSet, metrics.SubsystemDB)),
	)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	signalCtx = logpkg.WithLogger(signalCtx, logger)
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	redisClient, err := redistore.Connect(cfg.RedisURL)
	if err != nil {
		logger.Error("connect redis", "error", err)
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()
	redisBus, err := notifications.NewRedisBus(redisClient, logger)
	if err != nil {
		logger.Error("create redis bus", "error", err)
		os.Exit(1)
	}
	presenceStore, err := notifications.NewRedisPresenceStore(redisClient)
	if err != nil {
		logger.Error("create redis presence store", "error", err)
		os.Exit(1)
	}
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     redisBus,
			AgentEventWakeups: redisBus,
			ToolCallUpdates:   redisBus,
			WorkerControls:    redisBus,
		},
		presenceStore,
		logger,
		metrics.NewNotificationRecorder(metricSet),
	)
	if err != nil {
		logger.Error("create notification publisher", "error", err)
		os.Exit(1)
	}
	defer publisher.Close()
	secretKeyWrapper, err := cfg.SecretKeyWrapper()
	if err != nil {
		logger.Error("configure secret encryption", "error", err)
		os.Exit(1)
	}
	store := storage.NewStore(
		db,
		storage.WithPostCommitPublisher(publisher),
		storage.WithSecretKeyWrapper(secretKeyWrapper),
		storage.WithMachinePoolProviders(machinepool.DefaultCatalog()),
	)
	healthErr := metrics.Serve(
		ctx,
		logger,
		cfg.MaintenanceMetricsAddr,
		metricSet,
		metrics.ReadyAll(db.Ping, redisClient.Ping),
	)
	machinePoolManager := machinepool.NewManager(store.Execution(), store.Identity(), cfg.PublicAPIURL)
	runtimeRecorder := metrics.NewProviderRuntimeRecorder(metricSet)

	machineLoopDone := make(chan struct{})
	go func() {
		defer close(machineLoopDone)
		runMachinePoolMaintenanceLoop(ctx, logger, machinePoolManager, cfg.MaintenanceInterval)
	}()
	subagentLoopDone := make(chan struct{})
	go func() {
		defer close(subagentLoopDone)
		runSubagentMaintenanceLoop(ctx, logger, store, machinePoolManager, cfg.MaintenanceInterval)
	}()
	runtimeDiscoveryDone := make(chan struct{})
	go func() {
		defer close(runtimeDiscoveryDone)
		runProviderRuntimeMaintenanceLoop(
			ctx,
			logger,
			runtimeRecorder,
			providerRuntimeDiscoveryInterval,
			metrics.ProviderRuntimeOperationDiscovery,
			func(ctx context.Context) (machinepool.RuntimeReconciliationStats, error) {
				return machinePoolManager.DiscoverProviderRuntimeMismatches(
					ctx,
					machinepool.RuntimeReconciliationConfig{},
				)
			},
		)
	}()
	runtimeRecheckDone := make(chan struct{})
	go func() {
		defer close(runtimeRecheckDone)
		runProviderRuntimeMaintenanceLoop(
			ctx,
			logger,
			runtimeRecorder,
			providerRuntimeRecheckInterval,
			metrics.ProviderRuntimeOperationConfirmation,
			func(ctx context.Context) (machinepool.RuntimeReconciliationStats, error) {
				return machinePoolManager.ConfirmProviderRuntimeMismatches(
					ctx,
					machinepool.RuntimeReconciliationConfig{},
				)
			},
		)
	}()
	defaultModelProviderDone := make(chan struct{})
	if cfg.DefaultModelProvider == nil {
		close(defaultModelProviderDone)
	} else {
		hostedHTTPClient := metrics.NewObservedHTTPClient(
			&http.Client{Timeout: modelprovider.HostedCredentialProvisionTimeout},
			metrics.NewHTTPClientRecorder(metricSet, metrics.SubsystemHTTPClient),
			metrics.WithHTTPClientPathLabel(modelprovider.HostedCredentialPath),
		)
		runner := defaultprovider.NewRunner(
			store.Organizations(),
			modelprovider.HTTPHostedCredentialProvisioner{
				BaseURL:    cfg.HostedAPIURL,
				Token:      cfg.HostedAPIToken,
				HTTPClient: hostedHTTPClient,
			},
			*cfg.DefaultModelProvider,
		)
		go func() {
			defer close(defaultModelProviderDone)
			runDefaultModelProviderProvisioningLoop(
				ctx,
				logger,
				runner,
				cfg.MaintenanceInterval,
			)
		}()
	}

	exitCode := runCoreMaintenanceLoop(
		ctx,
		cancel,
		logger,
		store,
		cfg.MaintenanceInterval,
		healthErr,
	)
	cancel()
	<-machineLoopDone
	<-subagentLoopDone
	<-runtimeDiscoveryDone
	<-runtimeRecheckDone
	<-defaultModelProviderDone
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func runProviderRuntimeMaintenanceLoop(
	ctx context.Context,
	log *slog.Logger,
	recorder *metrics.ProviderRuntimeRecorder,
	interval time.Duration,
	operation metrics.ProviderRuntimeOperation,
	run func(context.Context) (machinepool.RuntimeReconciliationStats, error),
) {
	for {
		started := time.Now()
		stats, err := runProviderRuntimeMaintenanceTick(ctx, log, operation, run)
		outcome := completedMaintenanceOutcome(ctx, err)
		recordProviderRuntimePass(recorder, operation, stats, outcome, time.Since(started))
		if outcome.err != nil {
			log.Error("provider runtime reconciliation", "operation", operation, "error", outcome.err, "stats", stats)
		} else if !outcome.interrupted &&
			(stats.Targets > 0 || stats.MarkersSet > 0 || stats.DeletionClaims > 0) {
			log.Info("provider runtime reconciliation", "operation", operation, "stats", stats)
		}
		timer := time.NewTimer(jitteredMaintenanceDelay(interval))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
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

func jitteredMaintenanceDelay(interval time.Duration) time.Duration {
	spread := interval / 10
	if spread <= 0 {
		return interval
	}
	return interval - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}

func runCoreMaintenanceLoop(
	ctx context.Context,
	cancel context.CancelFunc,
	log *slog.Logger,
	store *storage.Store,
	interval time.Duration,
	healthErr <-chan error,
) int {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		loopCtx, event := logent.MaintenanceLoop(ctx, interval, now)
		runCoreMaintenanceTick(loopCtx, log, store)
		event.Done(loopCtx)
		select {
		case <-ctx.Done():
			<-healthErr
			return 0
		case err := <-healthErr:
			cancel()
			if err != nil {
				log.Error("maintenance health and metrics server failed", "error", err)
				return 1
			}
			return 0
		case <-ticker.C:
		}
	}
}

func runCoreMaintenanceTick(
	ctx context.Context,
	log *slog.Logger,
	store *storage.Store,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logpkg.Error(ctx, fmt.Errorf("core maintenance tick panicked: %v", recovered))
			logpkg.Attach(ctx, logpkg.Fields{"error.stack": string(debug.Stack())})
		}
	}()
	reapedRuntimeLocks, reapRuntimeLocksErr := store.Execution().ReapExpiredAgentRuntimeLocks(
		ctx,
		runtimeLockReapBatchSize,
	)
	reapRuntimeLocksOutcome := completedMaintenanceOutcome(ctx, reapRuntimeLocksErr)
	records, expireDaemonRuntimesErr := store.Execution().EndExpiredDaemonRuntimes(ctx, 100)
	expireDaemonRuntimesOutcome := completedMaintenanceOutcome(ctx, expireDaemonRuntimesErr)
	var expiredDaemonRuntimes int
	if expireDaemonRuntimesErr == nil {
		expiredDaemonRuntimes = len(records)
	}
	expiredUnreachableTools, expireUnreachableToolsErr :=
		store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
			ctx,
			executionstore.ProcessToolMachineUnreachableGrace,
		)
	expireUnreachableToolsOutcome := completedMaintenanceOutcome(ctx, expireUnreachableToolsErr)
	authCleanup, authCleanupErr := store.Identity().CleanupInactiveAuthState(ctx)
	authCleanupOutcome := completedMaintenanceOutcome(ctx, authCleanupErr)
	authCleanupDeleted := authCleanup.DeletedInactiveTokens > 0 ||
		authCleanup.DeletedBrowserSessions > 0 ||
		authCleanup.DeletedAbandonedUsers > 0 ||
		authCleanup.DeletedDeviceFlows > 0
	worked := reapedRuntimeLocks > 0 ||
		expiredDaemonRuntimes > 0 ||
		expiredUnreachableTools > 0 ||
		authCleanupDeleted
	logent.MaintenanceLoopResult(
		ctx,
		reapedRuntimeLocks,
		reapRuntimeLocksOutcome.err,
		worked,
		errors.Join(
			reapRuntimeLocksOutcome.err,
			expireDaemonRuntimesOutcome.err,
			expireUnreachableToolsOutcome.err,
			authCleanupOutcome.err,
		),
	)
	if expireDaemonRuntimesOutcome.err != nil {
		log.Error("expire daemon runtimes", "error", expireDaemonRuntimesOutcome.err)
	} else if !expireDaemonRuntimesOutcome.interrupted && expiredDaemonRuntimes > 0 {
		log.Info("expired daemon runtimes", "count", expiredDaemonRuntimes)
	}
	if expireUnreachableToolsOutcome.err != nil {
		log.Error("expire machine-unreachable process tool calls", "error", expireUnreachableToolsOutcome.err)
	} else if !expireUnreachableToolsOutcome.interrupted && expiredUnreachableTools > 0 {
		log.Info("expired machine-unreachable process tool calls", "count", expiredUnreachableTools)
	}
	if authCleanupOutcome.err != nil {
		log.Error("cleanup inactive auth state", "error", authCleanupOutcome.err)
	} else if !authCleanupOutcome.interrupted && authCleanupDeleted {
		log.Info(
			"cleaned inactive auth state",
			"deleted_inactive_tokens",
			authCleanup.DeletedInactiveTokens,
			"deleted_browser_sessions",
			authCleanup.DeletedBrowserSessions,
			"deleted_abandoned_users",
			authCleanup.DeletedAbandonedUsers,
			"deleted_device_flows",
			authCleanup.DeletedDeviceFlows,
		)
	}
}

func runMachinePoolMaintenanceLoop(
	ctx context.Context,
	log *slog.Logger,
	machinePoolManager *machinepool.Manager,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var nextIdleReconcileAt time.Time
	for {
		runMachinePoolMaintenanceTick(ctx, log, machinePoolManager)
		if now := time.Now(); !now.Before(nextIdleReconcileAt) {
			candidateCount, err := runIdleMachineDeletionMaintenanceTick(
				ctx,
				log,
				machinePoolManager.ReconcileIdleDeletion,
			)
			outcome := completedMaintenanceOutcome(ctx, err)
			if outcome.err != nil {
				log.Error("reconcile idle machine deletion", "candidate_count", candidateCount, "error", outcome.err)
			} else if !outcome.interrupted && candidateCount > 0 {
				log.Info("reconciled idle machine deletion", "candidate_count", candidateCount)
			}
			if candidateCount < machinepool.DefaultReconcileBatchSize {
				nextIdleReconcileAt = now.Add(idleMachineReconcileInterval)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runIdleMachineDeletionMaintenanceTick(
	ctx context.Context,
	log *slog.Logger,
	reconcile func(context.Context, int32) (int, error),
) (candidateCount int, err error) {
	defer recoverMachinePoolMaintenancePanic(log)
	return reconcile(ctx, machinepool.DefaultReconcileBatchSize)
}

func recoverMachinePoolMaintenancePanic(log *slog.Logger) {
	if recovered := recover(); recovered != nil {
		log.Error("machine pool maintenance tick panicked", "error", recovered, "stack", string(debug.Stack()))
	}
}

func runMachinePoolMaintenanceTick(
	ctx context.Context,
	log *slog.Logger,
	machinePoolManager *machinepool.Manager,
) {
	defer recoverMachinePoolMaintenancePanic(log)
	provisioned, provisionErr := machinePoolManager.ReconcileProvisioning(
		ctx,
		machinepool.DefaultReconcileBatchSize,
	)
	provisionOutcome := completedMaintenanceOutcome(ctx, provisionErr)
	if provisionOutcome.err != nil {
		log.Error("reconcile machine provisioning", "attempted_count", provisioned, "error", provisionOutcome.err)
	} else if !provisionOutcome.interrupted && provisioned > 0 {
		log.Info("attempted machine provisioning reconcile", "attempted_count", provisioned)
	}
	cleaned, cleanupErr := machinePoolManager.ReconcileCleanup(
		ctx,
		machinepool.DefaultReconcileBatchSize,
	)
	cleanupOutcome := completedMaintenanceOutcome(ctx, cleanupErr)
	if cleanupOutcome.err != nil {
		log.Error("reconcile machine cleanup", "attempted_count", cleaned, "error", cleanupOutcome.err)
	} else if !cleanupOutcome.interrupted && cleaned > 0 {
		log.Info("attempted machine cleanup reconcile", "attempted_count", cleaned)
	}
}

func runSubagentMaintenanceLoop(
	ctx context.Context,
	log *slog.Logger,
	store *storage.Store,
	machinePoolManager *machinepool.Manager,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		runSubagentMaintenanceTick(ctx, log, store, machinePoolManager)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runSubagentMaintenanceTick(
	ctx context.Context,
	log *slog.Logger,
	store *storage.Store,
	machinePoolManager *machinepool.Manager,
) {
	defer recoverMachinePoolMaintenancePanic(log)
	expired, err := store.Execution().ExpireAgentWaits(ctx, machinepool.DefaultReconcileBatchSize)
	outcome := completedMaintenanceOutcome(ctx, err)
	if outcome.err != nil {
		log.Error("expire agent waits", "expired_count", expired, "error", outcome.err)
	} else if !outcome.interrupted && expired > 0 {
		log.Info("expired agent waits", "expired_count", expired)
	}
	machines, archived, err := store.Execution().ArchiveIdleSubagents(ctx, machinepool.DefaultReconcileBatchSize)
	outcome = completedMaintenanceOutcome(ctx, err)
	if outcome.err != nil {
		log.Error("archive idle subagents", "archived_count", archived, "error", outcome.err)
	} else if !outcome.interrupted && archived > 0 {
		log.Info("archived idle subagents", "archived_count", archived)
	}
	if len(machines) == 0 {
		return
	}
	if _, err := machinePoolManager.DeleteMachines(ctx, machines); err != nil {
		log.Error("delete idle subagent machines", "machine_count", len(machines), "error", err)
	}
}

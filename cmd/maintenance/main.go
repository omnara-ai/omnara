package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/omnara-ai/omnara/internal/config"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const (
	runtimeLockReapBatchSize         int32 = 100
	providerRuntimeDiscoveryInterval       = 5 * time.Minute
	providerRuntimeRecheckInterval         = 30 * time.Second
)

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
		metrics.ReadyAll(store.Ping, redisClient.Ping),
	)
	machinePoolManager := machinepool.NewManager(store.Execution(), store.Identity(), cfg.PublicURL)
	runtimeRecorder := metrics.NewProviderRuntimeRecorder(metricSet)

	machineLoopDone := make(chan struct{})
	go func() {
		defer close(machineLoopDone)
		runMachinePoolMaintenanceLoop(ctx, logger, machinePoolManager, cfg.MaintenanceInterval)
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
	<-runtimeDiscoveryDone
	<-runtimeRecheckDone
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
		recordProviderRuntimePass(recorder, operation, stats, err, ctx.Err(), time.Since(started))
		if err != nil && ctx.Err() == nil {
			log.Error("provider runtime reconciliation", "operation", operation, "error", err, "stats", stats)
		} else if stats.Targets > 0 || stats.MarkersSet > 0 || stats.DeletionClaims > 0 {
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
	err error,
	shutdownErr error,
	duration time.Duration,
) {
	result := providerRuntimeResult(err, shutdownErr)
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

func providerRuntimeResult(err, shutdownErr error) metrics.ProviderRuntimeResult {
	if err == nil {
		return metrics.ProviderRuntimeResultSuccess
	}
	if errors.Is(err, context.Canceled) && shutdownErr != nil {
		return metrics.ProviderRuntimeResultCanceled
	}
	return metrics.ProviderRuntimeResultError
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
	var reapedRuntimeLocks int64
	var expiredUnreachableTools int64
	var expiredDaemonRuntimes int
	var rebuilt int64
	var authCleanup identitystore.AuthStateCleanupResult
	var reapRuntimeLocksErr error
	var expireUnreachableToolsErr error
	var expireDaemonRuntimesErr error
	var rebuildErr error
	var authCleanupErr error
	reapedRuntimeLocks, reapRuntimeLocksErr = store.Execution().ReapExpiredAgentRuntimeLocks(
		ctx,
		runtimeLockReapBatchSize,
	)
	if records, err := store.Execution().EndExpiredDaemonRuntimes(ctx, 100); err != nil {
		expireDaemonRuntimesErr = err
	} else {
		expiredDaemonRuntimes = len(records)
	}
	expiredUnreachableTools, expireUnreachableToolsErr =
		store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
			ctx,
			executionstore.ProcessToolMachineUnreachableGrace,
		)
	rebuilt, rebuildErr = store.Execution().RebuildMissingAgentWakeupsForAllProjects(ctx)
	authCleanup, authCleanupErr = store.Identity().CleanupInactiveAuthState(ctx)
	logent.MaintenanceLoopResult(
		ctx,
		reapedRuntimeLocks,
		reapRuntimeLocksErr,
		rebuilt,
		rebuildErr,
	)
	if expireDaemonRuntimesErr != nil {
		log.Error("expire daemon runtimes", "error", expireDaemonRuntimesErr)
	} else if expiredDaemonRuntimes > 0 {
		log.Info("expired daemon runtimes", "count", expiredDaemonRuntimes)
	}
	if expireUnreachableToolsErr != nil {
		log.Error("expire machine-unreachable process tool calls", "error", expireUnreachableToolsErr)
	} else if expiredUnreachableTools > 0 {
		log.Info("expired machine-unreachable process tool calls", "count", expiredUnreachableTools)
	}
	authCleanupDeleted := authCleanup.DeletedInactiveTokens > 0 ||
		authCleanup.DeletedBrowserSessions > 0 ||
		authCleanup.DeletedAbandonedUsers > 0 ||
		authCleanup.DeletedDeviceFlows > 0
	if authCleanupErr != nil {
		log.Error("cleanup inactive auth state", "error", authCleanupErr)
	} else if authCleanupDeleted {
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
	for {
		runMachinePoolMaintenanceTick(ctx, log, machinePoolManager)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runMachinePoolMaintenanceTick(
	ctx context.Context,
	log *slog.Logger,
	machinePoolManager *machinepool.Manager,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Error("machine pool maintenance tick panicked", "error", recovered, "stack", string(debug.Stack()))
		}
	}()
	if attempted, err := machinePoolManager.ReconcileProvisioning(
		ctx,
		machinepool.DefaultReconcileBatchSize,
	); err != nil {
		log.Error("reconcile machine provisioning", "attempted_count", attempted, "error", err)
	} else if attempted > 0 {
		log.Info("attempted machine provisioning reconcile", "attempted_count", attempted)
	}
	if attempted, err := machinePoolManager.ReconcileCleanup(
		ctx,
		machinepool.DefaultReconcileBatchSize,
	); err != nil {
		log.Error("reconcile machine cleanup", "attempted_count", attempted, "error", err)
	} else if attempted > 0 {
		log.Info("attempted machine cleanup reconcile", "attempted_count", attempted)
	}
}

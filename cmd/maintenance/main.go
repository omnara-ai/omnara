package main

import (
	"context"
	"fmt"
	"log/slog"
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

const runtimeLockReapBatchSize int32 = 100

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

	machineLoopDone := make(chan struct{})
	go func() {
		defer close(machineLoopDone)
		runMachinePoolMaintenanceLoop(ctx, logger, machinePoolManager, cfg.MaintenanceInterval)
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
	if exitCode != 0 {
		os.Exit(exitCode)
	}
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

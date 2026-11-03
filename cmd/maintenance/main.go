package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/omnara-ai/omnara/internal/config"
	"github.com/omnara-ai/omnara/internal/defaultprovider"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/maintenance"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/observability/metrics"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
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
	memoryFS, err := memorystore.OpenUnpreparedFilesystem(cfg.MemoryDir)
	if err != nil {
		logger.Error("configure memory storage", "error", err)
		os.Exit(1)
	}
	defer func() { _ = memoryFS.Close() }()
	store := storage.NewStore(
		db,
		storage.WithPostCommitPublisher(publisher),
		storage.WithSecretKeyWrapper(secretKeyWrapper),
		storage.WithMemoryFilesystem(memoryFS),
		storage.WithMemoryRecorder(metrics.NewMemoryRecorder(metricSet)),
		storage.WithMachinePoolProviders(machinepool.DefaultCatalog()),
	)
	healthErr := metrics.Serve(
		ctx,
		logger,
		cfg.MaintenanceMetricsAddr,
		metricSet,
		metrics.ReadyAll(db.Ping, redisClient.Ping),
	)
	var defaultModelProvider *defaultprovider.Runner
	if cfg.DefaultModelProvider != nil {
		hostedHTTPClient := metrics.NewObservedHTTPClient(
			&http.Client{Timeout: modelprovider.HostedCredentialProvisionTimeout},
			metrics.NewHTTPClientRecorder(metricSet, metrics.SubsystemHTTPClient),
			metrics.WithHTTPClientPathLabel(modelprovider.HostedCredentialPath),
		)
		defaultModelProvider = defaultprovider.NewRunner(
			store.Organizations(),
			modelprovider.HTTPHostedCredentialProvisioner{
				BaseURL:    cfg.HostedAPIURL,
				Token:      cfg.HostedAPIToken,
				HTTPClient: hostedHTTPClient,
			},
			*cfg.DefaultModelProvider,
		)
	}

	jobsDone := make(chan struct{})
	go func() {
		defer close(jobsDone)
		maintenance.Run(ctx, maintenance.Options{
			Log:                  logger,
			Store:                store,
			MachinePools:         machinepool.NewManager(store.Execution(), store.Identity(), cfg.PublicAPIURL),
			Metrics:              metricSet,
			Interval:             cfg.MaintenanceInterval,
			DefaultModelProvider: defaultModelProvider,
		})
	}()
	exitCode := 0
	select {
	case <-ctx.Done():
		<-healthErr
	case err := <-healthErr:
		if err != nil {
			logger.Error("maintenance health and metrics server failed", "error", err)
			exitCode = 1
		}
	}
	cancel()
	<-jobsDone
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

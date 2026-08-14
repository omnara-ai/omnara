package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/config"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	workerpkg "github.com/omnara-ai/omnara/internal/harness/worker"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

const (
	integrationHTTPClientTimeout = 5 * time.Minute
	cronTriggerFireInterval      = 30 * time.Second
)

func main() {
	log := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("load config", "error", err)
		os.Exit(1)
	}
	if err := cfg.ValidateWorker(); err != nil {
		log.Error("validate worker config", "error", err)
		os.Exit(1)
	}
	if err := tools.ValidateToolImplementationRegistry(); err != nil {
		log.Error("validate tool implementation registry", "error", err)
		os.Exit(1)
	}

	metricSet := metrics.New()
	db, err := storage.Open(
		context.Background(),
		cfg.DatabaseURL,
		storage.WithDefaultApplicationName("omnara-worker"),
		storage.WithQueryTracer(metrics.NewDBRecorder(metricSet, metrics.SubsystemDB)),
	)
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	redisClient, err := redistore.Connect(cfg.RedisURL)
	if err != nil {
		log.Error("connect redis", "error", err)
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()
	redisBus, err := notifications.NewRedisBus(redisClient, log)
	if err != nil {
		log.Error("create redis bus", "error", err)
		os.Exit(1)
	}
	presenceStore, err := notifications.NewRedisPresenceStore(redisClient)
	if err != nil {
		log.Error("create redis presence store", "error", err)
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
		log,
		metrics.NewNotificationRecorder(metricSet),
	)
	if err != nil {
		log.Error("create notification publisher", "error", err)
		os.Exit(1)
	}
	defer publisher.Close()
	secretKeyWrapper, err := cfg.SecretKeyWrapper()
	if err != nil {
		log.Error("configure secret encryption", "error", err)
		os.Exit(1)
	}
	storeOpts := []storage.Option{
		storage.WithPostCommitPublisher(publisher),
		storage.WithSecretKeyWrapper(secretKeyWrapper),
		storage.WithMachinePoolProviders(machinepool.DefaultCatalog()),
	}
	if cfg.BlobS3Bucket != "" {
		blobs, err := blobstore.NewS3Store(context.Background(), cfg.BlobStoreS3Config())
		if err != nil {
			log.Error("configure blob store", "error", err)
			os.Exit(1)
		}
		storeOpts = append(storeOpts, storage.WithBlobStore(blobs))
	}
	store := storage.NewStore(db, storeOpts...)
	healthErr := metrics.Serve(
		ctx,
		log,
		cfg.WorkerMetricsAddr,
		metricSet,
		metrics.ReadyAll(db.Ping, redisClient.Ping),
	)
	httpRecorder := metrics.NewHTTPClientRecorder(metricSet, metrics.SubsystemHTTPClient)
	integrationHTTPClient := metrics.NewObservedHTTPClient(
		outboundhttp.NewPublicClient(
			outboundhttp.PublicClientOptions{Timeout: integrationHTTPClientTimeout},
		),
		httpRecorder,
	)
	mcpHTTPClient := outboundhttp.NewPublicClient(
		outboundhttp.PublicClientOptions{AllowLoopback: cfg.AllowInsecureDev},
	)
	searchHTTPClient := metrics.NewObservedHTTPClient(
		outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{}),
		httpRecorder,
		metrics.WithHTTPClientPathLabel("/search"),
	)
	searchProvider := webaccess.ExaProvider{APIKey: cfg.ExaAPIKey, HTTPClient: searchHTTPClient}
	webFetcher := webaccess.NewFetcher(webaccess.FetcherOptions{AllowLoopback: cfg.AllowInsecureDev})
	machinePoolManager := machinepool.NewManager(store.Execution(), store.Identity(), cfg.PublicURL)
	backgroundRunner, err := tools.NewBackgroundExecutionRunner(
		ctx,
		log,
		cfg.WorkerBackgroundToolCapacity,
	)
	if err != nil {
		log.Error("configure background tool runner", "error", err)
		os.Exit(1)
	}
	skillSigningKey, err := cfg.SkillDownloadSigningKeyBytes()
	if err != nil {
		log.Error("decode skill download signing key", "error", err)
		os.Exit(1)
	}
	daemonInbox := notifications.NewDaemonInbox(redisBus, presenceStore)
	skillBroadcaster, err := skills.NewBroadcaster(
		daemonInbox,
		redisBus,
		machinePoolManager,
		skillSigningKey,
	)
	if err != nil {
		log.Error("configure skill broadcaster", "error", err)
		os.Exit(1)
	}
	sigV4CredentialCache, err := mcp.NewSigV4CredentialCache()
	if err != nil {
		log.Error("configure SigV4 credential cache", "error", err)
		os.Exit(1)
	}
	executor := workerpkg.AgentWorkExecutor(kernel.AgentExecutor{
		Store: store,
		ContextBuilder: modelcontext.Builder{
			Store:  modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations()),
			Skills: store.Skills(),
		},
		ModelResolver: modelprovider.Resolver{
			Models:        store.Models(),
			Secrets:       store.Secrets(),
			HTTPRecorder:  httpRecorder,
			AllowLoopback: cfg.AllowInsecureDev,
			OpenRouterAttribution: modelprovider.OpenRouterAttribution{
				SiteURL:       cfg.OpenRouterSiteURL,
				AppTitle:      cfg.OpenRouterAppTitle,
				AppCategories: cfg.OpenRouterAppCategories,
			},
		},
		MCP:                  mcp.New(mcp.Options{HTTPClient: mcpHTTPClient}),
		MCPAuthHTTPClient:    mcpHTTPClient,
		SigV4CredentialCache: sigV4CredentialCache,
		ToolExecutor: tools.Executor{
			Store:                 store,
			Skills:                store.Skills(),
			IntegrationHTTPClient: integrationHTTPClient,
			WebSearch:             searchProvider,
			WebFetcher:            webFetcher,
			MachinePoolManager:    machinePoolManager,
			BackgroundRunner:      backgroundRunner,
			SkillBroadcaster:      skillBroadcaster,
		},
		StreamPublisher: redisBus,
		StreamLog:       log,
	})
	kernelWorker := workerpkg.NewWorker(store.Execution(), executor, workerpkg.Options{
		Log:               log,
		Capacity:          cfg.WorkerCapacity,
		AsyncToolCapacity: cfg.WorkerAsyncToolCapacity,
		ControlSubscriber: redisBus,
	})
	workerErr := make(chan error, 1)
	go func() {
		workerErr <- kernelWorker.Run(ctx)
	}()
	cronTriggerService := crontrigger.NewService(store.Execution(), machinePoolManager, log)
	cronTriggerDone := make(chan struct{})
	go func() {
		defer close(cronTriggerDone)
		runCronTriggerFireLoop(ctx, log, cronTriggerService, cronTriggerFireInterval)
	}()

	exitCode := 0
	select {
	case err := <-workerErr:
		cancel()
		<-healthErr
		if err != nil && signalCtx.Err() == nil {
			log.Error("kernel worker failed", "error", err)
			exitCode = 1
		}
	case err := <-healthErr:
		cancel()
		workerRunErr := <-workerErr
		if err != nil {
			log.Error("worker health and metrics server failed", "error", err)
			exitCode = 1
		} else if workerRunErr != nil && signalCtx.Err() == nil {
			log.Error("kernel worker failed", "error", workerRunErr)
			exitCode = 1
		}
	case <-signalCtx.Done():
		cancel()
		<-healthErr
		<-workerErr
	}
	<-cronTriggerDone
	backgroundRunner.Shutdown()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func runCronTriggerFireLoop(
	ctx context.Context,
	log *slog.Logger,
	service *crontrigger.Service,
	interval time.Duration,
) {
	for {
		stats, err := runCronTriggerFireTick(ctx, log, service)
		if err != nil && ctx.Err() == nil {
			log.Error("fire due cron triggers", "error", err)
		} else if stats.Claimed > 0 || stats.Disabled > 0 {
			log.Info(
				"fired due cron triggers",
				"claimed", stats.Claimed,
				"launched", stats.Launched,
				"inputs", stats.Inputs,
				"disabled", stats.Disabled,
				"failures", stats.Failures,
			)
		}
		timer := time.NewTimer(jitteredFireDelay(interval))
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

func runCronTriggerFireTick(
	ctx context.Context,
	log *slog.Logger,
	service *crontrigger.Service,
) (stats crontrigger.FireStats, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("cron trigger fire tick panicked: %v", recovered)
			log.Error(
				"cron trigger fire tick panicked",
				"error", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()
	return service.FireDueTriggers(ctx)
}

func jitteredFireDelay(interval time.Duration) time.Duration {
	spread := interval / 10
	if spread <= 0 {
		return interval
	}
	return interval - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}

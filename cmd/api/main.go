package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/frontend/apps/web"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/config"
	"github.com/omnara-ai/omnara/internal/email"
	"github.com/omnara-ai/omnara/internal/httpapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	outboundHTTPClientTimeout  = 30 * time.Second
	defaultReconciliationPlan  = "plan"
	defaultReconciliationApply = "apply"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))
		log.Error("load config", "error", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 {
		if err := runDefaultReconciliation(context.Background(), cfg, os.Args[1:], os.Stdout); err != nil {
			log := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))
			log.Error("run command", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := cfg.ValidateAPI(); err != nil {
		log := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))
		log.Error("validate api config", "error", err)
		os.Exit(1)
	}

	level, err := parseLogLevel(cfg.LogLevel)
	if err != nil {
		log := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))
		log.Error("parse log level", "error", err)
		os.Exit(1)
	}
	log := slog.New(logpkg.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if _, err := toolcatalog.Default(); err != nil {
		log.Error("validate tool catalog", "error", err)
		os.Exit(1)
	}

	metricSet := metrics.New()
	db, err := storage.Open(
		context.Background(),
		cfg.DatabaseURL,
		storage.WithQueryTracer(metrics.NewDBRecorder(metricSet, metrics.SubsystemDB)),
	)
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

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
	daemonRecorder := metrics.NewDaemonRecorder(metricSet)
	publisher, err := notifications.NewRoutedPublisher(
		notifications.RoutedPublisherPorts{
			DaemonWakeups:     redisBus,
			AgentEventWakeups: redisBus,
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
	blobs, err := blobstore.NewS3Store(context.Background(), cfg.BlobStoreS3Config())
	if err != nil {
		log.Error("configure blob store", "error", err)
		os.Exit(1)
	}
	storeOpts = append(storeOpts, storage.WithBlobStore(blobs))
	store := storage.NewStore(db, storeOpts...)
	if err := bootstrapAuthConnectors(context.Background(), store, cfg); err != nil {
		log.Error("bootstrap auth connectors", "error", err)
		os.Exit(1)
	}
	replicaID := uuid.New()

	machinePoolManager := machinepool.NewManager(store.Execution(), store.Identity(), cfg.PublicURL)
	for _, defaultPoolTemplate := range cfg.DefaultMachinePools {
		if err := machinePoolManager.ValidateDefaultMachinePool(defaultPoolTemplate); err != nil {
			log.Error("validate default machine pool", "error", err)
			os.Exit(1)
		}
	}
	apiOpts, err := apiOptions(
		cfg,
		metricSet,
		daemonRecorder,
		machinePoolManager,
		redisClient,
		redisBus,
		presenceStore,
		replicaID,
		secretKeyWrapper,
	)
	if err != nil {
		log.Error("configure api options", "error", err)
		os.Exit(1)
	}
	apiServer, err := httpapi.New(log, store, apiOpts...)
	if err != nil {
		log.Error("create api server", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	metricsErr := metrics.Serve(ctx, log, cfg.APIMetricsAddr, metricSet, metrics.ReadyAll(store.Ping, redisClient.Ping))

	serverErr := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.APIAddr)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()

	exitCode := 0
	metricsDrained := false
	select {
	case <-signalCtx.Done():
	case err := <-serverErr:
		if err != nil {
			log.Error("api failed", "error", err)
			exitCode = 1
		}
	case err := <-metricsErr:
		metricsDrained = true
		if err != nil {
			log.Error("api metrics server failed", "error", err)
			exitCode = 1
		}
	}
	cancel()
	apiServer.CloseDaemonSockets()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("api shutdown failed", "error", err)
		exitCode = 1
	}
	if !metricsDrained {
		<-metricsErr
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func parseDefaultReconciliationMode(args []string) (string, error) {
	if len(args) != 2 || args[0] != "reconcile-defaults" {
		return "", errors.New("usage: omnara-api reconcile-defaults plan|apply")
	}
	switch args[1] {
	case defaultReconciliationPlan, defaultReconciliationApply:
		return args[1], nil
	default:
		return "", errors.New("usage: omnara-api reconcile-defaults plan|apply")
	}
}

func runDefaultReconciliation(
	ctx context.Context,
	cfg config.Config,
	args []string,
	output io.Writer,
) error {
	mode, err := parseDefaultReconciliationMode(args)
	if err != nil {
		return err
	}
	if cfg.DatabaseURL == "" {
		return errors.New("OMNARA_DATABASE_URL is required")
	}
	if len(cfg.DefaultMachinePools) == 0 && cfg.DefaultModelProvider == nil {
		return errors.New("no default templates are configured")
	}
	db, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	store := storage.NewStore(db, storage.WithMachinePoolProviders(machinepool.DefaultCatalog()))
	result, reconcileErr := store.Organizations().ReconcileDefaults(ctx, orglifecycle.ReconcileDefaultsInput{
		Apply:                mode == defaultReconciliationApply,
		DefaultMachinePools:  cfg.DefaultMachinePools,
		DefaultModelProvider: cfg.DefaultModelProvider,
	})
	for _, change := range result.Changes {
		if _, err := fmt.Fprintf(output, "%s: %s\n", mode, change); err != nil {
			return err
		}
	}
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(output, "%s: warning: %s\n", mode, warning); err != nil {
			return err
		}
	}
	if reconcileErr != nil {
		return reconcileErr
	}
	if len(result.Changes) == 0 {
		_, err = fmt.Fprintf(output, "%s: no changes\n", mode)
		return err
	}
	return nil
}

func bootstrapAuthConnectors(ctx context.Context, store *storage.Store, cfg config.Config) error {
	activeSlugs := make([]string, 0, len(cfg.AuthConnectors))
	for _, connector := range cfg.AuthConnectors {
		connector = connector.Normalized()
		input := identitystore.CreateAuthConnectorInput{
			Slug:             connector.Slug,
			Kind:             connector.Kind,
			DisplayName:      connector.DisplayName,
			Issuer:           connector.Issuer,
			AuthorizationURL: connector.AuthorizationURL,
			TokenURL:         connector.TokenURL,
			UserinfoURL:      connector.UserinfoURL,
			ClientID:         connector.ClientID,
			ClientSecret:     connector.ClientSecret,
			Scopes:           connector.Scopes,
			EmailTrustPolicy: connector.EmailTrustPolicy,
			Enabled:          connector.EnabledValue(),
		}
		if _, err := store.Identity().UpsertAuthConnector(ctx, input); err != nil {
			return err
		}
		activeSlugs = append(activeSlugs, input.Slug)
	}
	if _, err := store.Identity().DisableUnlistedAuthConnectors(ctx, activeSlugs); err != nil {
		return err
	}
	return nil
}

func webAssets(cfg config.Config) (fs.FS, bool, error) {
	switch cfg.EffectiveWebServing() {
	case config.WebServingDisabled:
		return nil, false, nil
	case config.WebServingEmbedded:
		assets := web.Dist()
		if err := requireWebIndex(assets, "embedded web assets"); err != nil {
			return nil, false, err
		}
		return assets, true, nil
	default:
		return nil, false, fmt.Errorf("OMNARA_WEB_SERVING must be embedded or disabled")
	}
}

func requireWebIndex(assets fs.FS, label string) error {
	info, err := fs.Stat(assets, "index.html")
	if err != nil {
		return fmt.Errorf("%s must contain index.html: %w", label, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s index.html must be a file", label)
	}
	return nil
}

func apiOptions(
	cfg config.Config,
	metricSet *metrics.Set,
	daemonRecorder *metrics.DaemonRecorder,
	machinePoolManager *machinepool.Manager,
	redisClient *redistore.Client,
	redisBus *notifications.RedisBus,
	presence notifications.DaemonPresenceStore,
	replicaID uuid.UUID,
	secretKeyWrapper secrets.KeyWrapper,
) ([]httpapi.Option, error) {
	httpRecorder := metrics.NewHTTPClientRecorder(metricSet, metrics.SubsystemHTTPClient)
	operatorHTTPClient := metrics.NewObservedHTTPClient(
		&http.Client{Timeout: outboundHTTPClientTimeout},
		httpRecorder,
	)
	publicServiceHTTPClient := metrics.NewObservedHTTPClient(
		outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: outboundHTTPClientTimeout}),
		httpRecorder,
	)
	assets, serveWeb, err := webAssets(cfg)
	if err != nil {
		return nil, err
	}
	opts := []httpapi.Option{
		httpapi.WithHTTPRecorder(metrics.NewHTTPRecorder(metricSet, metrics.SubsystemAPI)),
		httpapi.WithDaemonRecorder(daemonRecorder),
		httpapi.WithDaemonNotifications(redisBus, presence, replicaID),
		httpapi.WithDaemonReplyPublisher(redisBus),
		httpapi.WithAgentEventWakeupSubscriber(redisBus),
		httpapi.WithAgentStreamDeltaSubscriber(redisBus),
		httpapi.WithSecretKeyWrapper(secretKeyWrapper),
		httpapi.WithDefaultMachinePools(cfg.DefaultMachinePools),
		httpapi.WithDefaultModelProvider(cfg.DefaultModelProvider),
		httpapi.WithModelDiscoverer(modelprovider.NewDiscoverer(modelprovider.NewLimitsCatalog())),
		httpapi.WithHostedCredentialProvisioner(modelprovider.HTTPHostedCredentialProvisioner{
			BaseURL:    cfg.HostedAPIURL,
			Token:      cfg.HostedAPIToken,
			HTTPClient: operatorHTTPClient,
		}),
		httpapi.WithMachinePoolManager(machinePoolManager),
		httpapi.WithDaemonReleaseURL(cfg.DaemonReleaseURL),
		httpapi.WithDaemonSocketFallbackDrainTiming(
			cfg.DaemonSocketFallbackDrainInterval,
			cfg.DaemonSocketFallbackDrainJitter,
		),
	}
	if serveWeb {
		opts = append(opts, httpapi.WithWebAssets(assets))
	}
	if cfg.AllowInsecureDev {
		opts = append(opts,
			httpapi.WithAgentConfigOptions(agentconfig.CompileOptions{AllowInsecureLocalMCPHTTP: true}),
			httpapi.WithAllowInsecureModelProviderEndpoints(),
			httpapi.WithAllowInsecureLocalHostBypass(),
		)
	}
	switch cfg.EmailDriver {
	case "console":
		opts = append(opts, httpapi.WithEmailSender(email.ConsoleSender{}))
	case "smtp":
		opts = append(
			opts,
			httpapi.WithEmailSender(
				email.SMTPSender{
					Addr:       cfg.SMTPAddr,
					Username:   cfg.SMTPUsername,
					Password:   cfg.SMTPPassword,
					From:       cfg.EmailFrom,
					PublicURL:  cfg.PublicURL,
					RequireTLS: cfg.SMTPRequireTLS,
				},
			),
		)
	case "sendgrid":
		opts = append(
			opts,
			httpapi.WithEmailSender(
				email.SendGridSender{
					APIKey:     cfg.SendGridAPIKey,
					From:       cfg.EmailFrom,
					PublicURL:  cfg.PublicURL,
					HTTPClient: publicServiceHTTPClient,
				},
			),
		)
	}
	opts = append(opts, httpapi.WithPasswordAuthEnabled(cfg.AuthSignupEnabled, cfg.AuthPasswordResetEnabled))
	if cfg.PublicURL != "" {
		opts = append(opts, httpapi.WithPublicURL(cfg.PublicURL))
	}
	if cfg.BillingURL != "" {
		opts = append(opts, httpapi.WithBillingURL(cfg.BillingURL))
	}
	skillSigningKey, err := cfg.SkillDownloadSigningKeyBytes()
	if err != nil {
		return nil, err
	}
	opts = append(opts, httpapi.WithSkillDownloadSigningKey(skillSigningKey))
	opts = append(opts, httpapi.WithAuthHTTPClient(operatorHTTPClient))
	opts = append(opts, httpapi.WithSlackOAuth(httpapi.SlackOAuthConfig{HTTPClient: publicServiceHTTPClient}))
	opts = append(opts, httpapi.WithRedisBackedAuth(redisClient))
	opts = append(opts, httpapi.WithTrustedProxyCIDRs(cfg.TrustedProxyCIDRs))
	return opts, nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New("OMNARA_LOG_LEVEL must be debug, info, warn, or error")
	}
}

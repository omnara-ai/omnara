package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/omnara-ai/omnara/internal/config"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/mcpregistry"
)

const usage = "usage: omnara-mcp-registry sync|serve"

func main() {
	log := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("load config", "error", err)
		os.Exit(1)
	}
	if err := cfg.ValidateMCPRegistry(); err != nil {
		log.Error("validate mcp registry config", "error", err)
		os.Exit(1)
	}
	if len(os.Args) != 2 {
		log.Error(usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "sync":
		err = runSync(ctx, log, cfg)
	case "serve":
		err = runServe(ctx, log, cfg)
	default:
		err = errors.New(usage)
	}
	if err != nil {
		log.Error("mcp registry failed", "command", os.Args[1], "error", err)
		os.Exit(1)
	}
}

func runSync(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	started := time.Now()
	syncer := mcpregistry.Syncer{
		UpstreamURL: cfg.MCPRegistryUpstreamURL,
		HTTPClient:  &http.Client{Timeout: 2 * time.Minute},
	}
	servers, err := syncer.Fetch(ctx)
	if err != nil {
		return err
	}
	log.Info("fetched upstream registry", "servers", len(servers), "elapsed", time.Since(started).String())
	if err := os.MkdirAll(filepath.Dir(cfg.MCPRegistryDBPath), 0o755); err != nil {
		return fmt.Errorf("prepare registry database directory: %w", err)
	}
	stagingPath := cfg.MCPRegistryDBPath + ".staging"
	for _, stale := range []string{stagingPath, stagingPath + "-wal", stagingPath + "-shm"} {
		if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale staging database: %w", err)
		}
	}
	store, err := mcpregistry.OpenStore(ctx, stagingPath, false)
	if err != nil {
		return err
	}
	if err := store.Replace(ctx, servers); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Finalize(ctx); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close staging database: %w", err)
	}
	if err := os.Rename(stagingPath, cfg.MCPRegistryDBPath); err != nil {
		return fmt.Errorf("publish registry database: %w", err)
	}
	log.Info(
		"wrote registry database",
		"path", cfg.MCPRegistryDBPath,
		"servers", len(servers),
		"elapsed", time.Since(started).String(),
	)
	return nil
}

func runServe(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	if _, err := os.Stat(cfg.MCPRegistryDBPath); errors.Is(err, os.ErrNotExist) {
		log.Info("no registry snapshot found, syncing from upstream before serving", "path", cfg.MCPRegistryDBPath)
		if err := runSync(ctx, log, cfg); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("stat registry database: %w", err)
	}
	store, err := mcpregistry.OpenStore(ctx, cfg.MCPRegistryDBPath, true)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	count, err := store.Count(ctx)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.MCPRegistryAddr,
		Handler:           mcpregistry.NewHandler(store),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		log.Info("mcp registry listening", "addr", cfg.MCPRegistryAddr, "servers", count)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("mcp registry shutdown: %w", err)
	}
	return <-serverErr
}

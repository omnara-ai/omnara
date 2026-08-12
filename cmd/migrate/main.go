package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/omnara-ai/omnara/internal/config"
	"github.com/omnara-ai/omnara/internal/dbmigrate"
	logpkg "github.com/omnara-ai/omnara/internal/log"
)

func main() {
	logger := slog.New(logpkg.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.LoadMigrate()
	if err != nil {
		logger.Error("load migrate config", "error", err)
		os.Exit(1)
	}
	if err := cfg.ValidateMigrate(); err != nil {
		logger.Error("validate migrate config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := dbmigrate.RunPostgres(
		ctx,
		cfg.DatabaseURL,
		os.DirFS(cfg.MigrationsDir),
		cfg.MigrationTimeout,
	); err != nil {
		logger.Error("apply migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("database migrations applied")
}

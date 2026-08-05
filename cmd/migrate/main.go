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

	cfg := config.LoadMigrate()
	if err := cfg.ValidateMigrate(); err != nil {
		logger.Error("validate migrate config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := dbmigrate.OpenPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	if err := dbmigrate.ApplyPostgres(
		ctx,
		db,
		os.DirFS(cfg.MigrationsDir),
	); err != nil {
		logger.Error("apply migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("database migrations applied")
}

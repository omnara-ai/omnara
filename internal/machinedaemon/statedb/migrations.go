package statedb

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var stateMigrations embed.FS

func applyEmbeddedMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := fs.Sub(stateMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("open state migrations: %w", err)
	}
	return applyMigrations(ctx, db, migrations)
}

func applyMigrations(
	ctx context.Context,
	db *sql.DB,
	migrations fs.FS,
) error {
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return fmt.Errorf("create state migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return dbError("apply state migrations", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return dbError("read state migration versions", err)
	}
	if current > target {
		return fmt.Errorf(
			"state database migration version %d is newer than binary target %d",
			current,
			target,
		)
	}
	return nil
}

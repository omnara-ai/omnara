package dbmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const postgresMigrationLockID int64 = 0x4f4d4e415241

func OpenPostgres(ctx context.Context, databaseURL string) (*sql.DB, error) {
	// Parse with pgxpool to remove pool_* settings before adapting the shared URL for Goose.
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	db := stdlib.OpenDB(*config.ConnConfig)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

func ApplyPostgres(
	ctx context.Context,
	db *sql.DB,
	migrations fs.FS,
) error {
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockID(postgresMigrationLockID),
	)
	if err != nil {
		return fmt.Errorf("create migration lock: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations,
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return fmt.Errorf("create PostgreSQL migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("read PostgreSQL migration versions: %w", err)
	}
	if current > target {
		return fmt.Errorf(
			"postgresql migration version %d is newer than binary target %d",
			current,
			target,
		)
	}
	return nil
}

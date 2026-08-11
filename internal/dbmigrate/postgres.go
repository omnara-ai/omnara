package dbmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const (
	postgresMigrationLockID int64 = 0x4f4d4e415241
	minimumPostgresVersion  int   = 180000

	defaultLockTimeout      = "30s"
	defaultStatementTimeout = "15min"
	migrationUnlockTimeout  = 5 * time.Second
)

// RunPostgres opens a direct PostgreSQL connection and applies migrations
// within one bounded run. The database URL must not point at a transaction
// pooler because migration correctness relies on session state and a
// session-level advisory lock.
func RunPostgres(
	ctx context.Context,
	databaseURL string,
	migrations fs.FS,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return fmt.Errorf("migration timeout must be positive")
	}
	migrationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	db, err := OpenPostgres(migrationCtx, databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL migration database with %s timeout: %w", timeout, err)
	}
	defer func() { _ = db.Close() }()

	if err := ApplyPostgres(migrationCtx, db, migrations); err != nil {
		return fmt.Errorf("run PostgreSQL migrations with %s timeout: %w", timeout, err)
	}
	return nil
}

func OpenPostgres(ctx context.Context, databaseURL string) (*sql.DB, error) {
	config, err := parsePostgresConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	db := stdlib.OpenDB(*config)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

func parsePostgresConfig(databaseURL string) (*pgx.ConnConfig, error) {
	// Parse with pgxpool to remove pool_* settings before adapting the shared URL for Goose.
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if config.ConnConfig.Config.RuntimeParams == nil {
		config.ConnConfig.Config.RuntimeParams = make(map[string]string)
	}
	setDefaultRuntimeParam(config.ConnConfig.Config.RuntimeParams, "application_name", "omnara-migrate")
	setDefaultRuntimeParam(config.ConnConfig.Config.RuntimeParams, "lock_timeout", defaultLockTimeout)
	setDefaultRuntimeParam(config.ConnConfig.Config.RuntimeParams, "statement_timeout", defaultStatementTimeout)
	return config.ConnConfig, nil
}

func ApplyPostgres(
	ctx context.Context,
	db *sql.DB,
	migrations fs.FS,
) error {
	if err := requirePostgresVersion(ctx, db); err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockID(postgresMigrationLockID),
		lock.WithUnlockTimeout(1, 5),
	)
	if err != nil {
		return fmt.Errorf("create migration lock: %w", err)
	}
	boundedLocker := deadlineSessionLocker{
		delegate: locker,
		timeout:  migrationUnlockTimeout,
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations,
		goose.WithSessionLocker(boundedLocker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return fmt.Errorf("create PostgreSQL migration provider: %w", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("read PostgreSQL migration versions before apply: %w", err)
	}
	if current > target {
		return newerDatabaseVersionError(current, target)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	current, target, err = provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("read PostgreSQL migration versions after apply: %w", err)
	}
	if current > target {
		return newerDatabaseVersionError(current, target)
	}
	if current != target {
		return fmt.Errorf(
			"postgresql migration version %d did not reach binary target %d",
			current,
			target,
		)
	}
	return nil
}

// deadlineSessionLocker restores a finite deadline around SessionUnlock.
// Goose deliberately detaches the migration context before cleanup so a
// canceled migration can attempt to release its advisory lock. Without this
// wrapper, one unlock query on a dead connection could block forever.
type deadlineSessionLocker struct {
	delegate lock.SessionLocker
	timeout  time.Duration
}

func (locker deadlineSessionLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	return locker.delegate.SessionLock(ctx, conn)
}

func (locker deadlineSessionLocker) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	unlockCtx, cancel := context.WithTimeout(ctx, locker.timeout)
	defer cancel()
	return locker.delegate.SessionUnlock(unlockCtx, conn)
}

func requirePostgresVersion(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(
		ctx,
		`SELECT current_setting('server_version_num')::integer`,
	).Scan(&version); err != nil {
		return fmt.Errorf("read PostgreSQL server version: %w", err)
	}
	return validatePostgresVersion(version)
}

func validatePostgresVersion(version int) error {
	if version < minimumPostgresVersion {
		return fmt.Errorf(
			"PostgreSQL 18 or newer is required (server_version_num=%d)",
			version,
		)
	}
	return nil
}

func newerDatabaseVersionError(current, target int64) error {
	return fmt.Errorf(
		"postgresql migration version %d is newer than binary target %d",
		current,
		target,
	)
}

func setDefaultRuntimeParam(params map[string]string, name, value string) {
	if _, ok := params[name]; !ok {
		params[name] = value
	}
}

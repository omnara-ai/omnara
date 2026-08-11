package dbschema

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type QueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RequireVersion verifies that a runtime can safely use the current PostgreSQL
// schema. It is intentionally read-only: migrations remain the responsibility
// of the dedicated omnara-migrate process.
func RequireVersion(ctx context.Context, db QueryRower, minimum int64) error {
	if minimum <= 0 {
		return fmt.Errorf("minimum PostgreSQL schema version must be positive")
	}
	var current int64
	if err := db.QueryRow(
		ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version`,
	).Scan(&current); err != nil {
		return fmt.Errorf(
			"read PostgreSQL schema version: %w; run omnara-migrate before starting this process",
			err,
		)
	}
	if current < minimum {
		return fmt.Errorf(
			"PostgreSQL schema version %d is older than required minimum %d; "+
				"run omnara-migrate before starting this process",
			current,
			minimum,
		)
	}
	return nil
}

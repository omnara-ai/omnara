package dbconn

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SchemaVersionMismatchError struct {
	Expected int64
	Actual   int64
}

func (err *SchemaVersionMismatchError) Error() string {
	return fmt.Sprintf("database schema version %d does not match binary version %d", err.Actual, err.Expected)
}

type SchemaGuard struct {
	pool     *pgxpool.Pool
	expected int64
	done     chan struct{}
	mismatch atomic.Pointer[SchemaVersionMismatchError]
}

func NewSchemaGuard(pool *pgxpool.Pool, expected int64) *SchemaGuard {
	return &SchemaGuard{pool: pool, expected: expected, done: make(chan struct{})}
}

func (guard *SchemaGuard) Done() <-chan struct{} {
	return guard.done
}

func (guard *SchemaGuard) Mismatch() *SchemaVersionMismatchError {
	return guard.mismatch.Load()
}

func (guard *SchemaGuard) Ready(context.Context) error {
	if mismatch := guard.Mismatch(); mismatch != nil {
		return mismatch
	}
	return nil
}

func (guard *SchemaGuard) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	tx, err := guard.begin(ctx, pgx.TxOptions{})
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := tx.Exec(ctx, sql, arguments...)
	if err != nil {
		return pgconn.CommandTag{}, tx.abort(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	return tag, nil
}

func (guard *SchemaGuard) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx, err := guard.begin(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, tx.abort(ctx, err)
	}
	return &guardedRows{
		Rows:   rows,
		commit: func() error { return tx.Commit(ctx) },
		abort:  func(cause error) error { return tx.abort(ctx, cause) },
	}, nil
}

func (guard *SchemaGuard) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx, err := guard.begin(ctx, pgx.TxOptions{})
	if err != nil {
		return &guardedRow{err: err}
	}
	return &guardedRow{
		row:    tx.QueryRow(ctx, sql, args...),
		commit: func() error { return tx.Commit(ctx) },
		abort:  func(cause error) error { return tx.abort(ctx, cause) },
	}
}

func (guard *SchemaGuard) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return guard.begin(ctx, opts)
}

func (guard *SchemaGuard) begin(ctx context.Context, opts pgx.TxOptions) (*guardedTx, error) {
	if mismatch := guard.Mismatch(); mismatch != nil {
		return nil, mismatch
	}
	if opts != (pgx.TxOptions{}) {
		return nil, fmt.Errorf("schema guard requires default transaction options")
	}
	opts.IsoLevel = pgx.ReadCommitted
	tx, err := guard.pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &guardedTx{Tx: tx, guard: guard}, nil
}

func (guard *SchemaGuard) observe(actual int64) *SchemaVersionMismatchError {
	if actual != guard.expected {
		mismatch := &SchemaVersionMismatchError{Expected: guard.expected, Actual: actual}
		if guard.mismatch.CompareAndSwap(nil, mismatch) {
			close(guard.done)
		}
	}
	return guard.Mismatch()
}

func readSchemaVersion(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (int64, error) {
	var version int64
	if err := db.QueryRow(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read database schema version: %w", err)
	}
	return version, nil
}

type guardedTx struct {
	pgx.Tx
	guard *SchemaGuard
}

func (tx *guardedTx) Commit(ctx context.Context) error {
	if mismatch := tx.guard.Mismatch(); mismatch != nil {
		return tx.abort(ctx, mismatch)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE goose_db_version IN SHARE MODE`); err != nil {
		return tx.abort(ctx, err)
	}
	actual, err := readSchemaVersion(ctx, tx.Tx)
	if err != nil {
		return tx.abort(ctx, err)
	}
	if mismatch := tx.guard.observe(actual); mismatch != nil {
		return tx.abort(ctx, mismatch)
	}
	return tx.Tx.Commit(ctx)
}

func (tx *guardedTx) Rollback(ctx context.Context) error {
	if err := tx.Tx.Rollback(ctx); err != nil {
		return err
	}
	if tx.guard.Mismatch() == nil {
		actual, err := readSchemaVersion(ctx, tx.guard.pool)
		if err == nil {
			_ = tx.guard.observe(actual)
		}
	}
	return nil
}

func (tx *guardedTx) abort(ctx context.Context, cause error) error {
	_ = tx.Rollback(ctx)
	if mismatch := tx.guard.Mismatch(); mismatch != nil {
		return mismatch
	}
	return cause
}

type guardedRow struct {
	row    pgx.Row
	err    error
	commit func() error
	abort  func(error) error
}

func (row *guardedRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if err := row.row.Scan(dest...); err != nil {
		return row.abort(err)
	}
	return row.commit()
}

type guardedRows struct {
	pgx.Rows
	commit   func() error
	abort    func(error) error
	err      error
	finished bool
}

func (rows *guardedRows) Close() {
	rows.Rows.Close()
	rows.finish()
}

func (rows *guardedRows) finish() {
	if rows.finished {
		return
	}
	rows.finished = true
	if err := rows.Rows.Err(); err != nil {
		rows.err = rows.abort(err)
		return
	}
	rows.err = rows.commit()
}

func (rows *guardedRows) Next() bool {
	if rows.finished {
		return false
	}
	if rows.Rows.Next() {
		return true
	}
	rows.finish()
	return false
}

func (rows *guardedRows) Err() error {
	if rows.Rows.Err() != nil {
		rows.finish()
	}
	return rows.err
}

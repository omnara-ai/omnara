package agentexecution

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type unitDB struct{ unit *Unit }

func (db unitDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if err := db.unit.active(); err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := db.unit.tx.Exec(ctx, query, args...)
	db.unit.recordError(err)
	return tag, err
}

func (db unitDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if err := db.unit.active(); err != nil {
		return nil, err
	}
	rows, err := db.unit.tx.Query(ctx, query, args...)
	db.unit.recordError(err)
	if err != nil {
		return nil, err
	}
	return unitRows{Rows: rows, unit: db.unit}, nil
}

func (db unitDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if err := db.unit.active(); err != nil {
		return errorRow{err: err}
	}
	return unitRow{row: db.unit.tx.QueryRow(ctx, query, args...), unit: db.unit}
}

func (u *Unit) recordError(err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		u.aborted = true
	}
}

type errorRow struct{ err error }

func (row errorRow) Scan(...any) error { return row.err }

type unitRow struct {
	row  pgx.Row
	unit *Unit
}

func (row unitRow) Scan(dest ...any) error {
	err := row.row.Scan(dest...)
	row.unit.recordError(err)
	return err
}

type unitRows struct {
	pgx.Rows
	unit *Unit
}

func (rows unitRows) Conn() *pgx.Conn { return nil }

func (rows unitRows) Next() bool {
	next := rows.Rows.Next()
	if !next {
		rows.unit.recordError(rows.Rows.Err())
	}
	return next
}

func (rows unitRows) Err() error {
	err := rows.Rows.Err()
	rows.unit.recordError(err)
	return err
}

func (rows unitRows) Scan(dest ...any) error {
	err := rows.Rows.Scan(dest...)
	rows.unit.recordError(err)
	return err
}

func (rows unitRows) Close() {
	rows.Rows.Close()
	rows.unit.recordError(rows.Rows.Err())
}

func (db unitDB) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	if err := db.unit.active(); err != nil {
		return failedBatch{err: err}
	}
	return unitBatch{BatchResults: db.unit.tx.SendBatch(ctx, batch), unit: db.unit}
}

type failedBatch struct{ err error }

func (b failedBatch) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }
func (b failedBatch) Query() (pgx.Rows, error)         { return nil, b.err }
func (b failedBatch) QueryRow() pgx.Row                { return errorRow(b) }
func (b failedBatch) Close() error                     { return b.err }

type unitBatch struct {
	pgx.BatchResults
	unit *Unit
}

func (b unitBatch) Exec() (pgconn.CommandTag, error) {
	tag, err := b.BatchResults.Exec()
	b.unit.recordError(err)
	return tag, err
}

func (b unitBatch) Query() (pgx.Rows, error) {
	rows, err := b.BatchResults.Query()
	b.unit.recordError(err)
	if err != nil {
		return nil, err
	}
	return unitRows{Rows: rows, unit: b.unit}, nil
}

func (b unitBatch) QueryRow() pgx.Row {
	return unitRow{row: b.BatchResults.QueryRow(), unit: b.unit}
}

func (b unitBatch) Close() error {
	err := b.BatchResults.Close()
	b.unit.recordError(err)
	return err
}

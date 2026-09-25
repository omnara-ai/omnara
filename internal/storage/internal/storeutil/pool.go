package storeutil

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Pool struct {
	queryDB
	pool *pgxpool.Pool
}

func WrapPool(pool *pgxpool.Pool) *Pool {
	return &Pool{queryDB: queryDB{db: pool}, pool: pool}
}

func (p *Pool) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := p.pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, storeerr.WrapQuery("begin", err)
	}
	return queryTx{Tx: tx}, nil
}

type queryDB struct {
	db dbsqlc.DBTX
}

func (db queryDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := db.db.Exec(ctx, sql, args...)
	return tag, wrapQueryError(sql, err)
}

func (db queryDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := db.db.Query(ctx, sql, args...)
	return queryRows{Rows: rows, sql: sql}, wrapQueryError(sql, err)
}

func (db queryDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return queryRow{Row: db.db.QueryRow(ctx, sql, args...), sql: sql}
}

type queryTx struct {
	pgx.Tx
}

func (tx queryTx) Begin(ctx context.Context) (pgx.Tx, error) {
	nested, err := tx.Tx.Begin(ctx)
	if err != nil {
		return nil, storeerr.WrapQuery("begin", err)
	}
	return queryTx{Tx: nested}, nil
}

func (tx queryTx) Commit(ctx context.Context) error {
	return storeerr.WrapQuery("commit", tx.Tx.Commit(ctx))
}

func (tx queryTx) Rollback(ctx context.Context) error {
	return storeerr.WrapQuery("rollback", tx.Tx.Rollback(ctx))
}

func (tx queryTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return (queryDB{db: tx.Tx}).Exec(ctx, sql, args...)
}

func (tx queryTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return (queryDB{db: tx.Tx}).Query(ctx, sql, args...)
}

func (tx queryTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return (queryDB{db: tx.Tx}).QueryRow(ctx, sql, args...)
}

type queryRow struct {
	pgx.Row
	sql string
}

func (row queryRow) Scan(dest ...any) error {
	return wrapQueryError(row.sql, row.Row.Scan(dest...))
}

type queryRows struct {
	pgx.Rows
	sql string
}

func (rows queryRows) Scan(dest ...any) error {
	return wrapQueryError(rows.sql, rows.Rows.Scan(dest...))
}

func (rows queryRows) Err() error {
	return wrapQueryError(rows.sql, rows.Rows.Err())
}

func wrapQueryError(sql string, err error) error {
	if err == nil {
		return nil
	}
	return storeerr.WrapQuery(log.DBQueryName(sql), err)
}

//go:build integration

package integrationdb

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type CountingTx struct {
	pgx.Tx
	Statements int
	Batches    int
	Queries    int
	FactLoads  int
	Trace      []Statement
}

func (tx *CountingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.Statements++
	tx.Queries++
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	tx.Trace = append(tx.Trace, Statement{
		Query: pgx.QueuedQuery{SQL: sql, Arguments: args}, Affected: tag.RowsAffected(),
	})
	return tag, err
}

func (tx *CountingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.Statements++
	tx.Queries++
	tx.Trace = append(tx.Trace, Statement{Query: pgx.QueuedQuery{SQL: sql, Arguments: args}})
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return rows, nil
}

func (tx *CountingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.Statements++
	tx.Queries++
	tx.Trace = append(tx.Trace, Statement{Query: pgx.QueuedQuery{SQL: sql, Arguments: args}})
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (tx *CountingTx) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	tx.Statements += batch.Len()
	for _, query := range batch.QueuedQueries {
		tx.Trace = append(tx.Trace, Statement{Query: *query})
		if strings.HasPrefix(query.SQL, "-- name: LoadExecutionFacts ") {
			tx.FactLoads++
		}
	}
	tx.Batches++
	return tx.Tx.SendBatch(ctx, batch)
}

type Statement struct {
	Query    pgx.QueuedQuery
	Affected int64
}

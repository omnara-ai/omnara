//go:build integration

package agentexecution

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type countingLoadTx struct {
	pgx.Tx
	statements int
	batches    int
	queries    int
}

func (tx *countingLoadTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.statements++
	tx.queries++
	return tx.Tx.Exec(ctx, sql, args...)
}

func (tx *countingLoadTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.statements++
	tx.queries++
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

func (tx *countingLoadTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.statements++
	tx.queries++
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func CountOrdinaryLoad(ctx context.Context, h *Handle) (ExecutionSnapshot, int, error) {
	tx := &countingLoadTx{Tx: h.unit.tx}
	h.unit.tx = tx
	defer func() { h.unit.tx = tx.Tx }()
	snapshot, err := h.LoadExecution(ctx)
	return snapshot, tx.statements, err
}

func (tx *countingLoadTx) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	tx.statements += batch.Len()
	tx.batches++
	return tx.Tx.SendBatch(ctx, batch)
}

func CountCommit(ctx context.Context, u *Unit) (int, int, error) {
	tx := &countingLoadTx{Tx: u.tx}
	u.tx = tx
	defer func() { u.tx = tx.Tx }()
	err := u.Commit(ctx, "count publication")
	return tx.statements, tx.batches + tx.queries, err
}

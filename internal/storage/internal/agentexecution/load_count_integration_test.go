//go:build integration

package agentexecution

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type countingLoadTx struct {
	pgx.Tx
	statements int
	batches    int
	queries    int
	facts      *pgx.QueuedQuery
	factLoads  int
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
	for _, query := range batch.QueuedQueries {
		if strings.HasPrefix(query.SQL, "-- name: LoadExecutionFacts ") {
			tx.facts = query
			tx.factLoads++
		}
	}
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

func ReloadExecution(ctx context.Context, h *Handle) (ExecutionSnapshot, error) {
	route, err := h.Route()
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	var head *ExecutionHead
	if h.mutation != nil {
		head = &h.mutation.head
	}
	return loadExecution(ctx, h.unit.DB(), route, head)
}

func ExplainOrdinaryLoad(ctx context.Context, h *Handle) (string, error) {
	tx := &countingLoadTx{Tx: h.unit.tx}
	h.unit.tx = tx
	_, err := ReloadExecution(ctx, h)
	h.unit.tx = tx.Tx
	if err != nil {
		return "", err
	}
	if _, err := tx.Tx.Exec(ctx, "SET LOCAL plan_cache_mode=force_generic_plan"); err != nil {
		return "", err
	}
	var plan strings.Builder
	for range 10 {
		plan.Reset()
		rows, err := tx.Tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, TIMING OFF) "+tx.facts.SQL, tx.facts.Arguments...)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				return "", err
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return plan.String(), nil
}

func CountExecutionTransition(ctx context.Context, u *Unit, run func()) (int, int, error) {
	tx := &countingLoadTx{Tx: u.tx}
	u.tx = tx
	defer func() { u.tx = tx.Tx }()
	run()
	err := u.Commit(ctx, "count transition")
	return tx.statements, tx.factLoads, err
}

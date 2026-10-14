//go:build integration

package agentexecution

import (
	"context"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func CountOrdinaryLoad(ctx context.Context, h *Handle) (ExecutionSnapshot, int, int, error) {
	tx := &integrationdb.CountingTx{Tx: h.unit.tx}
	h.unit.tx = tx
	defer func() { h.unit.tx = tx.Tx }()
	snapshot, err := h.LoadExecution(ctx)
	return snapshot, tx.Statements, tx.Batches + tx.Queries, err
}

func CountCommit(ctx context.Context, u *Unit) (int, int, error) {
	tx := &integrationdb.CountingTx{Tx: u.tx}
	u.tx = tx
	defer func() { u.tx = tx.Tx }()
	err := u.Commit(ctx, "count publication")
	return tx.Statements, tx.Batches + tx.Queries, err
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

func CountExecutionTransition(ctx context.Context, u *Unit, run func()) (int, int, error) {
	tx := &integrationdb.CountingTx{Tx: u.tx}
	u.tx = tx
	defer func() { u.tx = tx.Tx }()
	run()
	err := u.Commit(ctx, "count transition")
	return tx.Statements, tx.FactLoads, err
}

func TraceExecutionCommand(u *Unit, run func() error) ([]integrationdb.Statement, error) {
	tx := &integrationdb.CountingTx{Tx: u.tx}
	u.tx = tx
	defer func() { u.tx = tx.Tx }()
	err := run()
	return tx.Trace, err
}

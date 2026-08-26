package dbconn

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type recordingRollbackTx struct {
	pgx.Tx
	rolledBack bool
}

func (tx *recordingRollbackTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

func TestSchemaGuardRejectsNonDefaultTransactionOptions(t *testing.T) {
	guard := NewSchemaGuard(nil, 1)
	if _, err := guard.BeginTx(
		context.Background(),
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted},
	); err == nil {
		t.Fatal("expected non-default transaction options error")
	}
}

func TestSchemaGuardLatchesFirstMismatch(t *testing.T) {
	guard := NewSchemaGuard(nil, 26)
	if err := guard.observe(26); err != nil {
		t.Fatalf("matching version: %v", err)
	}
	if err := guard.Ready(context.Background()); err != nil {
		t.Fatalf("ready before mismatch: %v", err)
	}
	if err := guard.observe(27); err == nil {
		t.Fatal("expected schema version mismatch")
	}
	if err := guard.observe(28); err == nil {
		t.Fatal("expected latched schema version mismatch")
	}
	if err := guard.observe(26); err == nil {
		t.Fatal("expected matching observation to preserve latched mismatch")
	}
	mismatch := guard.Mismatch()
	if mismatch == nil || mismatch.Expected != 26 || mismatch.Actual != 27 {
		t.Fatalf("latched mismatch = %+v", mismatch)
	}
	select {
	case <-guard.Done():
	default:
		t.Fatal("mismatch channel is open")
	}
	if err := guard.Ready(context.Background()); !errors.Is(err, mismatch) {
		t.Fatalf("readiness error = %v, want %v", err, mismatch)
	}
}

func TestRollbackSkipsVersionReadAfterMismatch(t *testing.T) {
	guard := NewSchemaGuard(nil, 26)
	_ = guard.observe(27)
	rawTx := &recordingRollbackTx{}
	tx := &guardedTx{Tx: rawTx, guard: guard}

	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !rawTx.rolledBack {
		t.Fatal("underlying transaction was not rolled back")
	}
}

package agentexecution

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type batchTxResult struct {
	pgx.BatchResults
	tx pgx.Tx
}

func (b batchTxResult) Tx() pgx.Tx { return b.tx }

type batchTx struct{ pgx.Tx }

func TestBatchResultTransactions(t *testing.T) {
	if tx := (factsResults{batch: &factsBatch{}}).Tx(); tx != nil {
		t.Fatal("unsent facts batch returned a transaction")
	}
	for _, tx := range []pgx.Tx{nil, &batchTx{}} {
		results := batchTxResult{tx: tx}
		facts := factsResults{batch: &factsBatch{results: results}}
		if facts.Tx() != tx {
			t.Fatal("facts batch did not preserve the underlying transaction")
		}
		if (unitBatch{BatchResults: results}).Tx() != tx {
			t.Fatal("unit batch did not preserve the underlying transaction")
		}
	}
}

func TestInactiveUnitBatchResults(t *testing.T) {
	for _, test := range []struct {
		name string
		unit Unit
		err  error
	}{
		{name: "closed", unit: Unit{closed: true}, err: ErrUnitClosed},
		{name: "aborted", unit: Unit{aborted: true}, err: ErrUnitAborted},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, begin := range []bool{false, true} {
				batch := &pgx.Batch{}
				if begin {
					batch.BeginTx(pgx.TxOptions{})
				}
				results := test.unit.DB().SendBatch(t.Context(), batch)
				if _, err := results.Exec(); !errors.Is(err, test.err) {
					t.Fatalf("batch exec: %v", err)
				}
				rows, err := results.Query()
				if rows != nil {
					rows.Close()
					t.Fatalf("inactive batch returned rows: %v", rows.Err())
				}
				if !errors.Is(err, test.err) {
					t.Fatalf("batch query: %v", err)
				}
				if err := results.QueryRow().Scan(); !errors.Is(err, test.err) {
					t.Fatalf("batch query row: %v", err)
				}
				if err := results.Close(); !errors.Is(err, test.err) {
					t.Fatalf("batch close: %v", err)
				}
				if tx := results.Tx(); begin {
					if tx == nil || !errors.Is(tx.Rollback(t.Context()), pgx.ErrTxClosed) {
						t.Fatal("failed transaction batch did not return a closed transaction")
					}
				} else if tx != nil {
					t.Fatal("failed batch returned an unrequested transaction")
				}
			}
		})
	}
}

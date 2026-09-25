package storeutil

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestQueryDBWrapsErrors(t *testing.T) {
	ctx := t.Context()
	cause := &pgconn.PgError{Code: "23505", Message: "customer_content", ConstraintName: "users_name_key"}
	fake := &queryTestTx{err: cause}
	for _, db := range []dbsqlc.DBTX{queryDB{db: fake}, queryTx{Tx: fake}} {
		for _, query := range []string{"-- name: GetAgent :one\nSELECT $1", "SELECT 'customer_content'"} {
			_, err := db.Exec(ctx, query, "customer_content")
			assertQueryError(t, err, cause)
			rows, err := db.Query(ctx, query, "customer_content")
			assertQueryError(t, err, cause)
			require.NotNil(t, rows)
			rows.Close()
			assertQueryError(t, rows.Err(), cause)
			err = db.QueryRow(ctx, query, "customer_content").Scan()
			assertQueryError(t, err, cause)
		}
		fake.err = nil
		fake.rowsErr = cause
		rows, err := db.Query(ctx, "-- name: GetAgent :many\nSELECT $1", "customer_content")
		require.NoError(t, err)
		assertQueryError(t, rows.Scan(), cause)
		assertQueryError(t, rows.Err(), cause)
		rows.Close()
		fake.rowsErr = nil
		_, err = db.Exec(ctx, "SELECT 1")
		require.NoError(t, err)
		require.NoError(t, db.QueryRow(ctx, "SELECT 1").Scan())
		fake.err = cause
	}
}

func TestQueryTxWrapsNestedTransactions(t *testing.T) {
	cause := errors.New("customer_content")
	fake := &queryTestTx{}
	tx := queryTx{Tx: fake}
	nested, err := tx.Begin(t.Context())
	require.NoError(t, err)
	fake.err = cause
	_, err = nested.Exec(t.Context(), "-- name: GetAgent :exec\nSELECT 1")
	assertQueryError(t, err, cause)
	assertQueryError(t, nested.Commit(t.Context()), cause)
	assertQueryError(t, nested.Rollback(t.Context()), cause)
	_, err = tx.Begin(t.Context())
	assertQueryError(t, err, cause)
}

func assertQueryError(t *testing.T, err, cause error) {
	t.Helper()
	var queryErr *storeerr.QueryError
	require.ErrorAs(t, err, &queryErr)
	require.ErrorIs(t, err, cause)
	require.NotEmpty(t, queryErr.Query)
	require.NotContains(t, err.Error(), "customer_content")
}

type queryTestTx struct {
	pgx.Tx
	err     error
	rowsErr error
}

func (tx *queryTestTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("INSERT 0 1"), tx.err
}

func (tx *queryTestTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if tx.err != nil {
		return queryTestRows{err: tx.err}, tx.err
	}
	return queryTestRows{err: tx.rowsErr}, tx.err
}

func (tx *queryTestTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return queryTestRows{err: tx.err}
}

func (tx *queryTestTx) Begin(context.Context) (pgx.Tx, error) {
	return tx, tx.err
}

func (tx *queryTestTx) Commit(context.Context) error {
	return tx.err
}

func (tx *queryTestTx) Rollback(context.Context) error {
	return tx.err
}

type queryTestRows struct {
	pgx.Rows
	err error
}

func (rows queryTestRows) Scan(...any) error {
	return rows.err
}

func (rows queryTestRows) Err() error {
	return rows.err
}

func (queryTestRows) Close() {}

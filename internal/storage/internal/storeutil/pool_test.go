package storeutil

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/errutil"
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

func TestQueryDBReportsCanceledDriverTimeoutsAsCancellation(t *testing.T) {
	timeout := &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	query := "-- name: GetAgent :one\nSELECT 1"
	assertCanceled := func(err error, detail string) {
		t.Helper()
		require.True(t, errutil.OnlyMatches(err, context.Canceled), err)
		require.ErrorContains(t, err, detail)
	}
	for _, db := range []dbsqlc.DBTX{queryDB{db: &queryTestTx{err: timeout}}, queryTx{Tx: &queryTestTx{err: timeout}}} {
		_, err := db.Exec(canceled, query)
		assertCanceled(err, "db query GetAgent: timeout")
		rows, err := db.Query(canceled, query)
		assertCanceled(err, "db query GetAgent: timeout")
		assertCanceled(rows.Err(), "db query GetAgent: timeout")
		assertCanceled(db.QueryRow(canceled, query).Scan(), "db query GetAgent: timeout")
		_, err = db.Exec(t.Context(), query)
		assertQueryError(t, err, timeout)
	}
	rows, err := queryDB{db: &queryTestTx{rowsErr: timeout}}.Query(canceled, query)
	require.NoError(t, err)
	assertCanceled(rows.Err(), "db query GetAgent: timeout")
	_, err = queryTx{Tx: &queryTestTx{err: timeout}}.Begin(canceled)
	assertCanceled(err, "db query begin: timeout")
	assertQueryError(t, queryTx{Tx: &queryTestTx{err: timeout}}.Commit(canceled), timeout)
}

func TestQueryDBKeepsOtherFailuresOfCanceledRequests(t *testing.T) {
	cfg, err := pgconn.ParseConfig("postgres://user:pass@127.0.0.1:1,127.0.0.2:1/db?sslmode=disable")
	require.NoError(t, err)
	dials := 0
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return nil, &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}
		}
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	}
	_, connectErr := pgconn.ConnectConfig(t.Context(), cfg)
	var connectTimeout net.Error
	require.True(t, errors.As(connectErr, &connectTimeout) && connectTimeout.Timeout(), connectErr)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for name, tt := range map[string]struct {
		err     error
		expired bool
	}{
		"mixed connect failure": {err: connectErr},
		"non-timeout write":     {err: &net.OpError{Op: "write", Err: os.ErrClosed}},
		"postgres error":        {err: &pgconn.PgError{Code: "42601"}},
		"deadline exceeded":     {err: &net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}, expired: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := canceled
			if tt.expired {
				ctx = expired
			}
			_, err := queryDB{db: &queryTestTx{err: tt.err}}.Exec(ctx, "-- name: GetAgent :one\nSELECT 1")
			require.NotErrorIs(t, err, context.Canceled)
			assertQueryError(t, err, tt.err)
		})
	}
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

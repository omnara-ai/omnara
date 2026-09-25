package storeerr_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omnara-ai/omnara/internal/errutil"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestQueryErrorSafeAcrossLogs(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "users_name_key", Message: "customer_content"}
	types := pgtype.NewMap()
	var value int64
	scanErr := pgx.ScanRow(types,
		[]pgconn.FieldDescription{{Name: "value", DataTypeOID: pgtype.Int8OID, Format: pgtype.TextFormatCode}},
		[][]byte{[]byte("customer_content")}, &value)
	_, encodeErr := types.Encode(pgtype.Int8OID, pgtype.BinaryFormatCode, "customer_content", nil)
	for _, tt := range []struct {
		cause error
		text  string
	}{
		{pgErr, "SQLSTATE 23505 (constraint users_name_key)"},
		{fmt.Errorf("customer_content: %w", pgErr), "SQLSTATE 23505 (constraint users_name_key)"},
		{scanErr, "pgx.ScanArgError"},
		{encodeErr, "*fmt.wrapError"},
		{fmt.Errorf("customer_content: %w", context.Canceled), "canceled"},
		{fmt.Errorf("customer_content: %w", context.DeadlineExceeded), "deadline exceeded"},
		{fmt.Errorf("customer_content: %w", &net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}), "timeout"},
		{errors.Join(errors.New("customer_content"), context.Canceled), "*errors.joinError"},
		{errors.Join(errors.New("customer_content"), context.DeadlineExceeded), "timeout"},
	} {
		cause := tt.cause
		t.Run(fmt.Sprintf("%T", cause), func(t *testing.T) {
			require.ErrorContains(t, cause, "customer_content")
			safe := storeerr.WrapQuery("GetAgent", cause)
			require.ErrorIs(t, safe, cause)
			require.EqualError(t, safe, "db query GetAgent: "+tt.text)
			require.Equal(t, errutil.OnlyMatches(cause, context.DeadlineExceeded),
				errutil.OnlyMatches(safe, context.DeadlineExceeded))
			var got *pgconn.PgError
			if errors.As(cause, &got) {
				require.ErrorAs(t, safe, &got)
				require.Same(t, pgErr, got)
			}

			var buf bytes.Buffer
			logger := slog.New(log.NewJSONHandler(&buf, nil))
			ctx := log.WithLogger(t.Context(), logger)
			original := log.NewEvent(ctx, "original")
			log.AttachDBQuery(log.WithEvent(ctx, original), log.DBQueryTraceRecord{Cause: cause, ErrorKind: "other"})
			original.Done(ctx)
			event := log.NewEvent(ctx, "another.event")
			ctx = log.WithEvent(ctx, event)
			err := fmt.Errorf("load agent: %w", safe)
			event.Error(err)
			event.Done(ctx)
			logger.WarnContext(ctx, "direct", "error", err)
			require.NotContains(t, buf.String(), "customer_content")
			require.Contains(t, buf.String(), "load agent: "+safe.Error())
		})
	}
}

func TestQueryErrorPreservesExactSentinels(t *testing.T) {
	for _, err := range []error{
		nil, pgx.ErrNoRows, pgx.ErrTxClosed, pgx.ErrTxCommitRollback, context.Canceled, context.DeadlineExceeded,
	} {
		require.Equal(t, err, storeerr.WrapQuery("GetAgent", err))
		if err != nil {
			wrapped := storeerr.WrapQuery("GetAgent", fmt.Errorf("customer_content: %w", err))
			require.ErrorIs(t, wrapped, err)
			require.NotContains(t, wrapped.Error(), "customer_content")
		}
	}
}

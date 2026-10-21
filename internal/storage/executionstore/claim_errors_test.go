package executionstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestClaimDeferralErrors(t *testing.T) {
	for _, err := range []error{
		agentexecution.ErrInvalidState, errors.New("decode failed"),
		&pgconn.PgError{Code: "23514"}, &pgconn.PgError{Code: "57014"},
	} {
		require.True(t, shouldDeferClaim(fmt.Errorf("claim: %w", err)), "%v", err)
	}
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded, storeerr.ErrCommitOutcomeUnknown,
		agentexecution.ErrLockPlan, storeutil.ErrRetryTransaction,
		&pgconn.PgError{Code: "40001"}, &pgconn.PgError{Code: "40P01"},
		&pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "57P01"},
		&pgconn.PgError{Code: "57P02"}, &pgconn.PgError{Code: "57P03"},
		&pgconn.PgError{Code: "53300"}, &pgconn.ConnectError{},
		&net.OpError{Op: "read", Err: io.EOF}, io.EOF, io.ErrUnexpectedEOF,
	} {
		require.False(t, shouldDeferClaim(fmt.Errorf("claim: %w", err)), "%T", err)
	}
	require.False(t, shouldDeferClaim(nil))
}

func TestDatabaseToolFallbackErrors(t *testing.T) {
	require.NoError(t, databaseToolFallbackError(nil))
	for _, err := range []error{storeerr.ErrInvalidRequest, storeerr.ErrNotFound, errors.New("decode failed")} {
		require.NoError(t, databaseToolFallbackError(fmt.Errorf("plan: %w", err)))
	}
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded, storeerr.ErrCommitOutcomeUnknown,
		agentexecution.ErrLockPlan, agentexecution.ErrUnitAborted, agentexecution.ErrUnitClosed,
		storeutil.ErrRetryTransaction, &pgconn.PgError{Code: "23514"}, &pgconn.PgError{Code: "40001"},
		&pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "08006"}, &pgconn.ConnectError{},
		&net.OpError{Op: "read", Err: io.EOF}, io.EOF, io.ErrUnexpectedEOF,
	} {
		require.ErrorIs(t, databaseToolFallbackError(fmt.Errorf("plan: %w", err)), err)
	}
}

package executionstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ClaimDeferredError struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	Cause     error
}

func (e *ClaimDeferredError) Error() string { return fmt.Sprintf("agent claim deferred: %v", e.Cause) }
func (e *ClaimDeferredError) Unwrap() error { return e.Cause }

func shouldDeferClaim(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, storeerr.ErrCommitOutcomeUnknown) || errors.Is(err, agentexecution.ErrLockPlan) ||
		storeutil.RetryableTransactionSQLState(err) != "" || pgconn.SafeToRetry(err) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	var connection *pgconn.ConnectError
	var network net.Error
	if errors.As(err, &connection) || errors.As(err, &network) {
		return false
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		return !strings.HasPrefix(postgres.Code, "08") && postgres.Code != "57P01" &&
			postgres.Code != "57P02" && postgres.Code != "57P03" && postgres.Code != "53300"
	}
	return true
}

package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// IntegrationInboxLeaseTx fences receipt mutations and execution admission in a
// caller-owned transaction. CheckLease must succeed after admission writes and
// before commit. Roll back on any error.
type IntegrationInboxLeaseTx struct {
	tx     pgx.Tx
	q      *dbsqlc.Queries
	lease  IntegrationInboxLease
	record IntegrationInboxRecord
}

func (s *Store) LockIntegrationInboxLeaseTx(
	ctx context.Context, tx pgx.Tx, lease IntegrationInboxLease, additional ...uuid.UUID,
) (*IntegrationInboxLeaseTx, error) {
	// Include every integration from the frozen admission plan so no new integration gate is
	// acquired beneath the receipt lock.
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil {
		return nil, inboxInvalid("project, receipt, and claim token are required")
	}
	q := dbsqlc.New(tx)
	row, err := q.GetIntegrationInboxReceipt(ctx, dbsqlc.GetIntegrationInboxReceiptParams{
		ProjectID: lease.ProjectID, ID: lease.ReceiptID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrIntegrationInboxLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("load inbox lease scope: %w", err)
	}
	if err := s.enterInboxIntegration(ctx, tx, lease.ProjectID, row.IntegrationID, additional...); err != nil {
		return nil, err
	}
	if _, err := q.LockIntegrationInboxReceipt(ctx, dbsqlc.LockIntegrationInboxReceiptParams{
		ProjectID: lease.ProjectID, ID: lease.ReceiptID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIntegrationInboxLeaseLost
		}
		return nil, fmt.Errorf("lock inbox lease: %w", err)
	}
	// A separate statement observes time after the lock wait; the transaction's
	// start time could incorrectly authorize an expired lease.
	row, err = q.ReadIntegrationInboxLease(ctx, dbsqlc.ReadIntegrationInboxLeaseParams{
		ProjectID: lease.ProjectID, ID: lease.ReceiptID, ClaimToken: lease.Token,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrIntegrationInboxLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("read inbox lease: %w", err)
	}
	return &IntegrationInboxLeaseTx{tx: tx, q: q, lease: lease, record: inboxRecord(row)}, nil
}

func (w *IntegrationInboxLeaseTx) Receipt() IntegrationInboxRecord {
	r := w.record
	r.Payload = bytes.Clone(r.Payload)
	r.Events = bytes.Clone(r.Events)
	r.Plan = bytes.Clone(r.Plan)
	if r.ClaimExpiresAt != nil {
		v := *r.ClaimExpiresAt
		r.ClaimExpiresAt = &v
	}
	if r.CompletedAt != nil {
		v := *r.CompletedAt
		r.CompletedAt = &v
	}
	return r
}

func (w *IntegrationInboxLeaseTx) FreezePlan(ctx context.Context, plan json.RawMessage) error {
	if err := w.CheckLease(ctx); err != nil {
		return err
	}
	if _, err := inboxSlots(plan); err != nil {
		return err
	}
	if len(w.record.Plan) != 0 {
		if !jsoncanonical.Equal(w.record.Plan, plan) {
			return storeerr.ErrConflict
		}
		return nil
	}
	if err := w.reserveIntegrationSelections(ctx, plan); err != nil {
		return err
	}
	rows, err := w.q.FreezeIntegrationInboxPlan(ctx, dbsqlc.FreezeIntegrationInboxPlanParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token, Plan: plan,
	})
	if err := inboxLeaseMutation("freeze inbox plan", rows, err); err != nil {
		return err
	}
	w.record.Plan = bytes.Clone(plan)
	return nil
}

// Complete marks a planned receipt terminal under its current lease. The caller
// must verify every planned outcome in this transaction first; executionstore's
// CompleteIntegrationInbox owns that settlement invariant.
func (w *IntegrationInboxLeaseTx) Complete(ctx context.Context) error {
	if err := w.CheckLease(ctx); err != nil {
		return err
	}
	if _, err := inboxSlots(w.record.Plan); err != nil {
		return err
	}
	rows, err := w.q.CompleteIntegrationInboxReceipt(ctx, dbsqlc.CompleteIntegrationInboxReceiptParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token,
	})
	return inboxLeaseMutation("complete inbox", rows, err)
}

func (w *IntegrationInboxLeaseTx) Retry(ctx context.Context, delay time.Duration, reason string) error {
	if delay < time.Second || delay > IntegrationInboxMaxRetryDelay {
		return inboxInvalid("retry delay must be between one second and 24 hours")
	}
	reason = boundedInboxError(reason)
	rows, err := w.q.RetryIntegrationInboxReceipt(ctx, dbsqlc.RetryIntegrationInboxReceiptParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token,
		DelayMilliseconds: delay.Milliseconds(), LastError: &reason,
	})
	return inboxLeaseMutation("retry inbox", rows, err)
}

func (w *IntegrationInboxLeaseTx) Fail(ctx context.Context, reason string) error {
	reason = boundedInboxError(reason)
	rows, err := w.q.FailIntegrationInboxReceipt(ctx, dbsqlc.FailIntegrationInboxReceiptParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token, LastError: &reason,
	})
	return inboxLeaseMutation("fail inbox", rows, err)
}

// CheckLease refreshes the receipt and validates its lease using a fresh database
// statement. Call it after domain writes to fence expiry during admission lock waits.
func (w *IntegrationInboxLeaseTx) CheckLease(ctx context.Context) error {
	row, err := w.q.ReadIntegrationInboxLease(ctx, dbsqlc.ReadIntegrationInboxLeaseParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIntegrationInboxLeaseLost
	}
	if err != nil {
		return fmt.Errorf("validate inbox lease: %w", err)
	}
	// Refresh even under an already-held row lock: another handle in the same
	// caller-owned transaction may have frozen the plan since this snapshot.
	w.record = inboxRecord(row)
	return nil
}

func (s *Store) WithIntegrationInboxLease(
	ctx context.Context, lease IntegrationInboxLease, apply func(*IntegrationInboxLeaseTx) error,
) error {
	if apply == nil {
		return inboxInvalid("inbox transaction callback is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin inbox lease transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	work, err := s.LockIntegrationInboxLeaseTx(ctx, tx, lease)
	if err != nil {
		return err
	}
	if err := apply(work); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit inbox lease transaction: %w", err)
	}
	return nil
}

func inboxLeaseMutation(operation string, rows int64, err error) error {
	if err != nil {
		// jsonb expansion can exceed the durable bound even when raw-byte validation passed.
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23514" &&
			databaseError.ConstraintName == "integration_inbox_plan_check" {
			return storeerr.InvalidRequest(fmt.Errorf("%s: normalized inbox JSON exceeds durable bounds: %w", operation, err))
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	if rows != 1 {
		return ErrIntegrationInboxLeaseLost
	}
	return nil
}

func inboxObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > IntegrationInboxMaxPlanBytes || !utf8.Valid(raw) {
		return nil, inboxInvalid("inbox JSON exceeds bounds or is invalid UTF-8")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, inboxInvalid("inbox JSON must be an object")
	}
	return value, nil
}

func inboxSlots(raw json.RawMessage) (map[string]json.RawMessage, error) {
	slots, err := inboxObject(raw)
	if err != nil {
		return nil, err
	}
	for slot, value := range slots {
		if strings.TrimSpace(slot) == "" || len(slot) > IntegrationInboxMaxReceiptKeyBytes {
			return nil, inboxInvalid("invalid inbox slot key")
		}
		if _, err := inboxObject(value); err != nil {
			return nil, err
		}
	}
	return slots, nil
}

func boundedInboxError(reason string) string {
	// Reasons are persisted; callers must redact credentials.
	reason = strings.ToValidUTF8(strings.ReplaceAll(reason, "\x00", ""), "\uFFFD")
	if strings.TrimSpace(reason) == "" {
		reason = "inbox processing failed"
	}
	if len(reason) <= IntegrationInboxMaxErrorBytes {
		return reason
	}
	reason = reason[:IntegrationInboxMaxErrorBytes]
	for !utf8.ValidString(reason) {
		reason = reason[:len(reason)-1]
	}
	return reason
}

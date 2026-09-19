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

// IntegrationInboxLeaseTx is a fenced receipt transaction. The caller owns the
// transaction and MUST roll back if any method fails. Acquire it before any
// conversation/agent locks: organization, project, connection, receipt, then
// conversation/agent state. Never call provider I/O while holding this handle.
// Use the same transaction for launch/input admission and CommitSlot so either
// both commit or neither does. A failed lease check must roll back admission.
type IntegrationInboxLeaseTx struct {
	tx     pgx.Tx
	q      *dbsqlc.Queries
	lease  IntegrationInboxLease
	record IntegrationInboxRecord
}

// LockIntegrationInboxLeaseTx fences a receipt and its frozen admission's
// connections. Additional identities must include every resource used by the
// admission, read from the immutable plan before entering this method. They are
// locked in one sorted union with the receipt connection.
func (s *Store) LockIntegrationInboxLeaseTx(
	ctx context.Context, tx pgx.Tx, lease IntegrationInboxLease, additional ...uuid.UUID,
) (*IntegrationInboxLeaseTx, error) {
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
	if err := s.enterInboxConnection(ctx, tx, lease.ProjectID, row.ConnectionID, additional...); err != nil {
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
	// Separate statement evaluates wall time after any lock wait. Every mutation
	// also checks a new statement_timestamp(), never transaction_timestamp().
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

// Receipt returns a detached snapshot so callers cannot mutate frozen state by
// retaining or modifying JSON buffers obtained from this transaction.
func (w *IntegrationInboxLeaseTx) Receipt() IntegrationInboxRecord {
	r := w.record
	r.Payload = bytes.Clone(r.Payload)
	r.Events = bytes.Clone(r.Events)
	r.Plan = bytes.Clone(r.Plan)
	r.Progress = bytes.Clone(r.Progress)
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
	if err := w.checkLease(ctx); err != nil {
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
	if err := w.reserveAppSelections(ctx, plan); err != nil {
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

// PrepareSlot persists once-only blob metadata/digests after uploads outside the
// transaction and before admission. Agent/artifact UUIDs and recipient identity
// MUST already be pinned in Plan. Preparation is evidence for those identities,
// never a replacement source of admission identity. Stages can only be added;
// replay of a different preparation fails and cannot overwrite an uploaded blob.
func (w *IntegrationInboxLeaseTx) PrepareSlot(ctx context.Context, slot string, prepared json.RawMessage) error {
	return w.writeSlotStage(ctx, slot, "prepared", prepared)
}

// CommitSlot records a successful admission in the same transaction as launch or
// input. Media-free slots need no prepared stage. Admission code is responsible
// for requiring preparation when its frozen plan contains media.
func (w *IntegrationInboxLeaseTx) CommitSlot(ctx context.Context, slot string, result json.RawMessage) error {
	return w.writeSlotStage(ctx, slot, "committed", result)
}

func (w *IntegrationInboxLeaseTx) writeSlotStage(ctx context.Context, slot, stage string, value json.RawMessage) error {
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	plan, err := inboxSlots(w.record.Plan)
	if err != nil {
		return err
	}
	if _, ok := plan[slot]; !ok {
		return inboxInvalid("slot is not in the frozen inbox plan")
	}
	if _, err := inboxObject(value); err != nil {
		return err
	}
	var progress map[string]map[string]json.RawMessage
	if err := json.Unmarshal(w.record.Progress, &progress); err != nil {
		return fmt.Errorf("decode inbox progress: %w", err)
	}
	stages := progress[slot]
	if stages == nil {
		stages = make(map[string]json.RawMessage)
		progress[slot] = stages
	}
	if existing, ok := stages[stage]; ok {
		if !jsoncanonical.Equal(existing, value) {
			return storeerr.ErrConflict
		}
		return nil
	}
	if stage == "prepared" && stages["committed"] != nil {
		return storeerr.ErrStateTransitionConflict
	}
	stages[stage] = bytes.Clone(value)
	encoded, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("encode inbox progress: %w", err)
	}
	if _, err := inboxObject(encoded); err != nil {
		return err
	}
	rows, err := w.q.UpdateIntegrationInboxProgress(ctx, dbsqlc.UpdateIntegrationInboxProgressParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token, Progress: encoded,
	})
	if err := inboxLeaseMutation("update inbox progress", rows, err); err != nil {
		return err
	}
	w.record.Progress = encoded
	return nil
}

func (w *IntegrationInboxLeaseTx) Complete(ctx context.Context) error {
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	plan, err := inboxSlots(w.record.Plan)
	if err != nil {
		return err
	}
	var progress map[string]map[string]json.RawMessage
	if err := json.Unmarshal(w.record.Progress, &progress); err != nil {
		return fmt.Errorf("decode inbox progress: %w", err)
	}
	for slot := range plan {
		if progress[slot]["committed"] == nil {
			return storeerr.ErrStateTransitionConflict
		}
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

func (w *IntegrationInboxLeaseTx) checkLease(ctx context.Context) error {
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
	// caller-owned transaction may have appended a stage since this snapshot.
	w.record = inboxRecord(row)
	return nil
}

// WithIntegrationInboxLease commits standalone planning/progress transitions.
// Atomic execution admission instead uses LockIntegrationInboxLeaseTx with its
// own transaction and calls CommitSlot before committing that transaction.
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
		// PostgreSQL jsonb expands spaces and numeric exponents. Raw-byte validation
		// bounds parsing work; the durable CHECK is authoritative for stored size.
		// Preserve its cause and return a permanent validation failure, never a lost
		// lease or silent truncation. The caller must roll back the aborted transaction.
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23514" &&
			(databaseError.ConstraintName == "integration_inbox_plan_check" ||
				databaseError.ConstraintName == "integration_inbox_progress_check") {
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

// Do not let oversized or invalid diagnostics prevent retry/failure settlement.
// Callers must redact credentials before supplying an error message.
func boundedInboxError(reason string) string {
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

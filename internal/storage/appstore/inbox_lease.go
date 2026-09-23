package appstore

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

// AppInboxLeaseTx must contain both admission and CommitSlot so lease expiry
// cannot leave admitted input without committed progress. Roll back on any error.
type AppInboxLeaseTx struct {
	tx     pgx.Tx
	q      *dbsqlc.Queries
	lease  AppInboxLease
	record AppInboxRecord
}

func (s *Store) LockAppInboxLeaseTx(
	ctx context.Context, tx pgx.Tx, lease AppInboxLease, additional ...uuid.UUID,
) (*AppInboxLeaseTx, error) {
	// Include every app from the frozen admission plan so no new app gate is
	// acquired beneath the receipt lock.
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil {
		return nil, inboxInvalid("project, receipt, and claim token are required")
	}
	q := dbsqlc.New(tx)
	row, err := q.GetAppInboxReceipt(ctx, dbsqlc.GetAppInboxReceiptParams{
		ProjectID: lease.ProjectID, ID: lease.ReceiptID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAppInboxLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("load inbox lease scope: %w", err)
	}
	if err := s.enterInboxApp(ctx, tx, lease.ProjectID, row.AppID, additional...); err != nil {
		return nil, err
	}
	if _, err := q.LockAppInboxReceipt(ctx, dbsqlc.LockAppInboxReceiptParams{
		ProjectID: lease.ProjectID, ID: lease.ReceiptID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAppInboxLeaseLost
		}
		return nil, fmt.Errorf("lock inbox lease: %w", err)
	}
	// A separate statement observes time after the lock wait; the transaction's
	// start time could incorrectly authorize an expired lease.
	row, err = q.ReadAppInboxLease(ctx, dbsqlc.ReadAppInboxLeaseParams{
		ProjectID: lease.ProjectID, ID: lease.ReceiptID, ClaimToken: lease.Token,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAppInboxLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("read inbox lease: %w", err)
	}
	return &AppInboxLeaseTx{tx: tx, q: q, lease: lease, record: inboxRecord(row)}, nil
}

func (w *AppInboxLeaseTx) Receipt() AppInboxRecord {
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

func (w *AppInboxLeaseTx) FreezePlan(ctx context.Context, plan json.RawMessage) error {
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
	rows, err := w.q.FreezeAppInboxPlan(ctx, dbsqlc.FreezeAppInboxPlanParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token, Plan: plan,
	})
	if err := inboxLeaseMutation("freeze inbox plan", rows, err); err != nil {
		return err
	}
	w.record.Plan = bytes.Clone(plan)
	return nil
}

func (w *AppInboxLeaseTx) PrepareSlot(ctx context.Context, slot string, prepared json.RawMessage) error {
	return w.writeSlotStage(ctx, slot, "prepared", prepared)
}

func (w *AppInboxLeaseTx) CommitSlot(ctx context.Context, slot string, result json.RawMessage) error {
	return w.writeSlotStage(ctx, slot, "committed", result)
}

func (w *AppInboxLeaseTx) writeSlotStage(ctx context.Context, slot, stage string, value json.RawMessage) error {
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
	rows, err := w.q.UpdateAppInboxProgress(ctx, dbsqlc.UpdateAppInboxProgressParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token, Progress: encoded,
	})
	if err := inboxLeaseMutation("update inbox progress", rows, err); err != nil {
		return err
	}
	w.record.Progress = encoded
	return nil
}

func (w *AppInboxLeaseTx) Complete(ctx context.Context) error {
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
	rows, err := w.q.CompleteAppInboxReceipt(ctx, dbsqlc.CompleteAppInboxReceiptParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token,
	})
	return inboxLeaseMutation("complete inbox", rows, err)
}

func (w *AppInboxLeaseTx) Retry(ctx context.Context, delay time.Duration, reason string) error {
	if delay < time.Second || delay > AppInboxMaxRetryDelay {
		return inboxInvalid("retry delay must be between one second and 24 hours")
	}
	reason = boundedInboxError(reason)
	rows, err := w.q.RetryAppInboxReceipt(ctx, dbsqlc.RetryAppInboxReceiptParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token,
		DelayMilliseconds: delay.Milliseconds(), LastError: &reason,
	})
	return inboxLeaseMutation("retry inbox", rows, err)
}

func (w *AppInboxLeaseTx) Fail(ctx context.Context, reason string) error {
	reason = boundedInboxError(reason)
	rows, err := w.q.FailAppInboxReceipt(ctx, dbsqlc.FailAppInboxReceiptParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token, LastError: &reason,
	})
	return inboxLeaseMutation("fail inbox", rows, err)
}

func (w *AppInboxLeaseTx) checkLease(ctx context.Context) error {
	row, err := w.q.ReadAppInboxLease(ctx, dbsqlc.ReadAppInboxLeaseParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAppInboxLeaseLost
	}
	if err != nil {
		return fmt.Errorf("validate inbox lease: %w", err)
	}
	// Refresh even under an already-held row lock: another handle in the same
	// caller-owned transaction may have appended a stage since this snapshot.
	w.record = inboxRecord(row)
	return nil
}

func (s *Store) WithAppInboxLease(
	ctx context.Context, lease AppInboxLease, apply func(*AppInboxLeaseTx) error,
) error {
	if apply == nil {
		return inboxInvalid("inbox transaction callback is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin inbox lease transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	work, err := s.LockAppInboxLeaseTx(ctx, tx, lease)
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
			(databaseError.ConstraintName == "app_inbox_plan_check" ||
				databaseError.ConstraintName == "app_inbox_progress_check") {
			return storeerr.InvalidRequest(fmt.Errorf("%s: normalized inbox JSON exceeds durable bounds: %w", operation, err))
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	if rows != 1 {
		return ErrAppInboxLeaseLost
	}
	return nil
}

func inboxObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > AppInboxMaxPlanBytes || !utf8.Valid(raw) {
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
		if strings.TrimSpace(slot) == "" || len(slot) > AppInboxMaxReceiptKeyBytes {
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
	if len(reason) <= AppInboxMaxErrorBytes {
		return reason
	}
	reason = reason[:AppInboxMaxErrorBytes]
	for !utf8.ValidString(reason) {
		reason = reason[:len(reason)-1]
	}
	return reason
}

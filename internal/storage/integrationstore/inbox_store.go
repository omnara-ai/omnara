package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) AcceptIntegrationReceipt(
	ctx context.Context, input VerifiedIntegrationReceipt,
) (IntegrationInboxRecord, bool, error) {
	if err := validateIntegrationReceipt(input); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("begin accept inbox receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.enterInboxConnection(ctx, tx, input.ProjectID, input.ConnectionID); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	q := dbsqlc.New(tx)
	row, err := q.InsertIntegrationInboxReceipt(ctx, dbsqlc.InsertIntegrationInboxReceiptParams{
		ProjectID:    input.ProjectID,
		ConnectionID: input.ConnectionID,
		ReceiptKey:   input.ReceiptKey,
		Payload:      input.Payload,
	})
	created := !errors.Is(err, pgx.ErrNoRows)
	if !created {
		// Separate statement sees a concurrent receipt committed while INSERT waited.
		row, err = q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
			ProjectID: input.ProjectID, ConnectionID: input.ConnectionID, ReceiptKey: input.ReceiptKey,
		})
	}
	if err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("accept inbox receipt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("commit accept inbox receipt: %w", err)
	}
	return inboxRecord(row), created, nil
}

func (s *Store) enterInboxConnection(
	ctx context.Context, tx pgx.Tx, projectID, connectionID uuid.UUID, additional ...uuid.UUID,
) error {
	connection, err := getIntegrationConnection(ctx, dbsqlc.New(tx), projectID, connectionID)
	if err != nil {
		return err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, connection.OrgID, projectID); err != nil {
		return err
	}
	// Lock the sorted union before the receipt. An admission may use resources
	// on other connections; taking those gates beneath the receipt can deadlock
	// with another receipt and concurrent connection revocation.
	ids := append([]uuid.UUID{connectionID}, additional...)
	return LockAppConnectionsTx(ctx, tx, projectID, nil, ids...)
}

func (s *Store) ClaimIntegrationInbox(
	ctx context.Context, input ClaimIntegrationInboxInput,
) (IntegrationInboxRecord, bool, error) {
	if input.ProjectID == uuid.Nil || input.ConnectionID == uuid.Nil {
		return IntegrationInboxRecord{}, false, inboxInvalid("project and connection are required")
	}
	if input.LeaseDuration < time.Second || input.LeaseDuration > IntegrationInboxMaxLease {
		return IntegrationInboxRecord{}, false, inboxInvalid("lease must be between one second and five minutes")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("begin claim inbox: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.enterInboxConnection(ctx, tx, input.ProjectID, input.ConnectionID); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	row, err := dbsqlc.New(tx).ClaimIntegrationInboxReceipt(ctx, dbsqlc.ClaimIntegrationInboxReceiptParams{
		ProjectID: input.ProjectID, ConnectionID: input.ConnectionID,
		ClaimToken: uuid.New(), LeaseMilliseconds: input.LeaseDuration.Milliseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationInboxRecord{}, false, nil
	}
	if err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("claim inbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("commit claim inbox: %w", err)
	}
	return inboxRecord(row), true, nil
}

// ListReadyIntegrationInboxConnections discovers work without acquiring child
// locks before lifecycle gates. Claim revalidates scope and skips busy receipts.
// Workers also call RecoverIntegrationInbox periodically, even when this is empty.
func (s *Store) ListReadyIntegrationInboxConnections(
	ctx context.Context, limit int,
) ([]IntegrationInboxConnection, error) {
	if err := validateInboxBatch(limit); err != nil {
		return nil, err
	}
	rows, err := s.q.ListReadyIntegrationInboxConnections(ctx, dbsqlc.ListReadyIntegrationInboxConnectionsParams{
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list ready inbox connections: %w", err)
	}
	result := make([]IntegrationInboxConnection, 0, len(rows))
	for _, row := range rows {
		result = append(result, IntegrationInboxConnection{ProjectID: row.ProjectID, ConnectionID: row.ConnectionID})
	}
	return result, nil
}

// GetIntegrationInbox is an operator diagnostic read. Worker decisions use
// LockIntegrationInboxLeaseTx and its fenced snapshot instead.
func (s *Store) GetIntegrationInbox(
	ctx context.Context, projectID, receiptID uuid.UUID,
) (IntegrationInboxRecord, error) {
	if projectID == uuid.Nil || receiptID == uuid.Nil {
		return IntegrationInboxRecord{}, inboxInvalid("project and receipt are required")
	}
	row, err := s.q.GetIntegrationInboxReceipt(ctx, dbsqlc.GetIntegrationInboxReceiptParams{
		ProjectID: projectID, ID: receiptID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationInboxRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationInboxRecord{}, fmt.Errorf("get inbox receipt: %w", err)
	}
	return inboxRecord(row), nil
}

// ListIntegrationInbox returns bounded metadata pages, not batches of raw bodies.
// Authorization to the project is the caller's responsibility, as for other
// capability-store operator reads. Cursor order is stable newest-first.
func (s *Store) ListIntegrationInbox(
	ctx context.Context, input ListIntegrationInboxInput,
) (ListIntegrationInboxResult, error) {
	if input.ProjectID == uuid.Nil {
		return ListIntegrationInboxResult{}, inboxInvalid("project is required")
	}
	if err := validateInboxBatch(input.Limit); err != nil {
		return ListIntegrationInboxResult{}, err
	}
	switch input.State {
	case "", IntegrationInboxPending, IntegrationInboxProcessing, IntegrationInboxCompleted, IntegrationInboxFailed:
	case IntegrationInboxDiscarded:
	default:
		return ListIntegrationInboxResult{}, inboxInvalid("invalid inbox state")
	}
	if input.After.Set && (input.After.CreatedAt.IsZero() || input.After.ID == uuid.Nil) {
		return ListIntegrationInboxResult{}, inboxInvalid("invalid inbox cursor")
	}
	rows, err := s.q.ListIntegrationInboxReceipts(ctx, dbsqlc.ListIntegrationInboxReceiptsParams{
		ProjectID: input.ProjectID, ConnectionID: storeutil.IDFromNil(input.ConnectionID), State: string(input.State),
		CursorSet: input.After.Set, CursorCreatedAt: input.After.CreatedAt,
		CursorID: input.After.ID, RowLimit: int32(input.Limit + 1),
	})
	if err != nil {
		return ListIntegrationInboxResult{}, fmt.Errorf("list inbox: %w", err)
	}
	result := ListIntegrationInboxResult{Receipts: make([]IntegrationInboxSummary, 0, input.Limit)}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	for _, row := range rows {
		result.Receipts = append(result.Receipts, IntegrationInboxSummary{
			ID: row.ID, ProjectID: row.ProjectID, ConnectionID: row.ConnectionID, ReceiptKey: row.ReceiptKey,
			State: IntegrationInboxState(row.State), AttemptCount: int(row.AttemptCount), AvailableAt: row.AvailableAt,
			ClaimExpiresAt: row.ClaimExpiresAt, LastError: inboxErrorText(row.LastError),
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: row.CompletedAt,
		})
	}
	if result.HasMore {
		last := rows[len(rows)-1]
		result.Next = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return result, nil
}

// RecoverIntegrationInbox also terminates pending/claimed work belonging to an
// inactive scope. Each call locks at most limit rows and never waits on a worker.
func (s *Store) RecoverIntegrationInbox(ctx context.Context, limit int) (int64, error) {
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	// Revoke inactive scopes first. Once disabled, a scope admits no new receipts,
	// so its finite backlog drains without scanning healthy pending work. The two
	// statements together settle at most limit rows; each independently commits.
	count, err := s.q.FailInactiveIntegrationInboxReceipts(ctx, dbsqlc.FailInactiveIntegrationInboxReceiptsParams{
		RowLimit: int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("fail inactive inbox: %w", err)
	}
	if count == int64(limit) {
		return count, nil
	}
	recovered, err := s.q.RecoverExpiredIntegrationInboxReceipts(
		ctx,
		dbsqlc.RecoverExpiredIntegrationInboxReceiptsParams{
			RowLimit: int32(int64(limit) - count),
		},
	)
	if err != nil {
		return count, fmt.Errorf("recover expired inbox: %w", err)
	}
	return count + recovered, nil
}

// CleanupTerminalIntegrationInbox bounds the replay-deduplication window as well
// as payload retention. The database owns the cutoff clock. This deletes only
// completed receipts, never agent history or failed plans needed for recovery.
func (s *Store) CleanupTerminalIntegrationInbox(
	ctx context.Context, retention time.Duration, limit int,
) (int64, error) {
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	if retention < time.Second {
		return 0, inboxInvalid("completed retention must be at least one second")
	}
	count, err := s.q.CleanupTerminalIntegrationInboxReceipts(ctx, dbsqlc.CleanupTerminalIntegrationInboxReceiptsParams{
		RetentionMilliseconds: retention.Milliseconds(), RowLimit: int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("clean completed inbox: %w", err)
	}
	return count, nil
}

func (s *Store) CleanupDeletedIntegrationInbox(ctx context.Context, limit int) (int64, error) {
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	count, err := s.q.CleanupDeletedIntegrationInboxReceipts(ctx, dbsqlc.CleanupDeletedIntegrationInboxReceiptsParams{
		RowLimit: int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("clean deleted inbox: %w", err)
	}
	return count, nil
}

func validateIntegrationReceipt(input VerifiedIntegrationReceipt) error {
	if input.ProjectID == uuid.Nil || input.ConnectionID == uuid.Nil {
		return inboxInvalid("project and connection are required")
	}
	if strings.TrimSpace(input.ReceiptKey) == "" || len(input.ReceiptKey) > IntegrationInboxMaxReceiptKeyBytes ||
		!utf8.ValidString(input.ReceiptKey) || strings.ContainsRune(input.ReceiptKey, 0) {
		return inboxInvalid("invalid receipt key")
	}
	if len(input.Payload) == 0 || len(input.Payload) > IntegrationInboxMaxPayloadBytes {
		return inboxInvalid("receipt payload exceeds bounds")
	}
	return nil
}

func validateInboxBatch(limit int) error {
	if limit < 1 || limit > IntegrationInboxMaxBatch {
		return inboxInvalid("inbox batch must be between 1 and 100")
	}
	return nil
}

func inboxInvalid(message string) error { return storeerr.InvalidRequest(errors.New(message)) }
func inboxErrorText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func inboxRecord(row dbsqlc.IntegrationInbox) IntegrationInboxRecord {
	var plan, events json.RawMessage
	if row.Events != nil {
		events = *row.Events
	}
	if row.Plan != nil {
		plan = *row.Plan
	}
	return IntegrationInboxRecord{
		IntegrationInboxSummary: IntegrationInboxSummary{
			ID: row.ID, ProjectID: row.ProjectID, ConnectionID: row.ConnectionID, ReceiptKey: row.ReceiptKey,
			State: IntegrationInboxState(row.State), AttemptCount: int(row.AttemptCount), AvailableAt: row.AvailableAt,
			ClaimExpiresAt: row.ClaimExpiresAt, LastError: inboxErrorText(row.LastError),
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: row.CompletedAt,
		}, Payload: row.Payload, Events: events, Plan: plan, Progress: row.Progress,
		ClaimToken: storeutil.IDFromPtr(row.ClaimToken),
	}
}

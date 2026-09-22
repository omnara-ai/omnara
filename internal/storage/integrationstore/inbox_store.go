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
	if err := s.enterInboxApp(ctx, tx, input.ProjectID, input.AppID); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	q := dbsqlc.New(tx)
	row, err := q.InsertIntegrationInboxReceipt(ctx, dbsqlc.InsertIntegrationInboxReceiptParams{
		ProjectID:  input.ProjectID,
		AppID:      input.AppID,
		ReceiptKey: input.ReceiptKey,
		Payload:    input.Payload,
	})
	created := !errors.Is(err, pgx.ErrNoRows)
	if !created {
		// Separate statement sees a concurrent receipt committed while INSERT waited.
		row, err = q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
			ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: input.ReceiptKey,
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

func (s *Store) enterInboxApp(
	ctx context.Context, tx pgx.Tx, projectID, appID uuid.UUID, additional ...uuid.UUID,
) error {
	app, err := getProjectApp(ctx, dbsqlc.New(tx), projectID, appID)
	if err != nil {
		return err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, app.OrgID, projectID); err != nil {
		return err
	}
	// Lock the sorted union before the receipt. An admission may use capabilities
	// on other apps; taking those gates beneath the receipt can deadlock
	// with another receipt and concurrent app revocation.
	// Compiled references only need project ownership. The receipt's owning
	// app supplies the required live authority for this admission.
	return lockProjectAppsTx(ctx, tx, projectID, additional, []uuid.UUID{appID})
}

func (s *Store) ClaimIntegrationInbox(
	ctx context.Context, input ClaimIntegrationInboxInput,
) (IntegrationInboxRecord, bool, error) {
	if input.ProjectID == uuid.Nil || input.AppID == uuid.Nil {
		return IntegrationInboxRecord{}, false, inboxInvalid("project and app are required")
	}
	if input.LeaseDuration < time.Second || input.LeaseDuration > IntegrationInboxMaxLease {
		return IntegrationInboxRecord{}, false, inboxInvalid("lease must be between one second and five minutes")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("begin claim inbox: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.enterInboxApp(ctx, tx, input.ProjectID, input.AppID); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	row, err := dbsqlc.New(tx).ClaimIntegrationInboxReceipt(ctx, dbsqlc.ClaimIntegrationInboxReceiptParams{
		ProjectID: input.ProjectID, AppID: input.AppID,
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

// ListReadyIntegrationInboxApps discovers work without acquiring child
// locks before lifecycle gates. Claim revalidates scope and skips busy receipts.
// Workers also call RecoverIntegrationInbox periodically, even when this is empty.
func (s *Store) ListReadyIntegrationInboxApps(
	ctx context.Context, limit int,
) ([]IntegrationInboxApp, error) {
	if err := validateInboxBatch(limit); err != nil {
		return nil, err
	}
	rows, err := s.q.ListReadyIntegrationInboxApps(ctx, dbsqlc.ListReadyIntegrationInboxAppsParams{
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list ready inbox apps: %w", err)
	}
	result := make([]IntegrationInboxApp, 0, len(rows))
	for _, row := range rows {
		result = append(result, IntegrationInboxApp{ProjectID: row.ProjectID, AppID: row.AppID})
	}
	return result, nil
}

// GetIntegrationInbox reads retained receipt state. Mutations require
// LockIntegrationInboxLeaseTx and its fenced snapshot.
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

// OldestReadyIntegrationInboxLag samples the oldest due pending receipt, including
// inactive scopes awaiting recovery. Empty is zero; a failed sample is an error.
// The database clock and available_at exclude scheduled retry backoff from lag.
func (s *Store) OldestReadyIntegrationInboxLag(ctx context.Context) (time.Duration, error) {
	seconds, err := s.q.OldestReadyIntegrationInboxLag(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sample oldest ready inbox lag: %w", err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// RecoverIntegrationInbox gives expired claims and inactive pending receipts
// independent allowances of limit rows, at most 2*limit total per call. Each
// statement commits separately and skips busy rows. Neither backlog can spend
// the other's allowance; a full allowance warrants another bounded pass.
func (s *Store) RecoverIntegrationInbox(ctx context.Context, limit int) (int64, error) {
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	recovered, err := s.q.RecoverExpiredIntegrationInboxReceipts(
		ctx, dbsqlc.RecoverExpiredIntegrationInboxReceiptsParams{RowLimit: int32(limit)},
	)
	if err != nil {
		return 0, fmt.Errorf("recover expired inbox: %w", err)
	}
	inactive, err := s.q.FailInactiveIntegrationInboxReceipts(
		ctx, dbsqlc.FailInactiveIntegrationInboxReceiptsParams{RowLimit: int32(limit)},
	)
	if err != nil {
		return recovered, fmt.Errorf("fail inactive inbox: %w", err)
	}
	return recovered + inactive, nil
}

// CleanupTerminalIntegrationInbox bounds the replay-deduplication window as well
// as payload retention. The database owns the cutoff clock. This deletes only
// completed/failed receipts, never committed agent history or other product state.
func (s *Store) CleanupTerminalIntegrationInbox(
	ctx context.Context, retention time.Duration, limit int,
) (int64, error) {
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	if retention < time.Second {
		return 0, inboxInvalid("terminal retention must be at least one second")
	}
	count, err := s.q.CleanupTerminalIntegrationInboxReceipts(ctx, dbsqlc.CleanupTerminalIntegrationInboxReceiptsParams{
		RetentionMilliseconds: retention.Milliseconds(), RowLimit: int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("clean terminal inbox: %w", err)
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
	if input.ProjectID == uuid.Nil || input.AppID == uuid.Nil {
		return inboxInvalid("project and app are required")
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
		ID: row.ID, ProjectID: row.ProjectID, AppID: row.AppID, ReceiptKey: row.ReceiptKey,
		State: IntegrationInboxState(row.State), AttemptCount: int(row.AttemptCount), AvailableAt: row.AvailableAt,
		ClaimExpiresAt: row.ClaimExpiresAt, LastError: inboxErrorText(row.LastError),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: row.CompletedAt,
		Source:     IntegrationInboxSource(row.Source),
		Payload:    row.Payload,
		Events:     events,
		Plan:       plan,
		Progress:   row.Progress,
		ClaimToken: storeutil.IDFromPtr(row.ClaimToken),
	}
}

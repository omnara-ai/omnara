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
	if err := s.enterInboxIntegration(ctx, tx, input.ProjectID, input.IntegrationID); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	q := dbsqlc.New(tx)
	row, err := q.InsertIntegrationInboxReceipt(ctx, dbsqlc.InsertIntegrationInboxReceiptParams{
		ProjectID:     input.ProjectID,
		IntegrationID: input.IntegrationID,
		ReceiptKey:    input.ReceiptKey,
		Payload:       input.Payload,
	})
	created := !errors.Is(err, pgx.ErrNoRows)
	if !created {
		// Separate statement sees a concurrent receipt committed while INSERT waited.
		row, err = q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
			ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, ReceiptKey: input.ReceiptKey,
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

func (s *Store) enterInboxIntegration(
	ctx context.Context, tx pgx.Tx, projectID, integrationID uuid.UUID, additional ...uuid.UUID,
) error {
	integration, err := getProjectIntegration(ctx, dbsqlc.New(tx), projectID, integrationID)
	if err != nil {
		return err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, integration.OrgID, projectID); err != nil {
		return err
	}
	// Acquire every integration gate before the receipt; adding one later can deadlock
	// with another receipt and concurrent integration revocation.
	return lockProjectIntegrationsTx(ctx, tx, projectID, additional, []uuid.UUID{integrationID})
}

func (s *Store) ClaimIntegrationInbox(
	ctx context.Context, input ClaimIntegrationInboxInput,
) (IntegrationInboxRecord, bool, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationID == uuid.Nil {
		return IntegrationInboxRecord{}, false, inboxInvalid("project and integration are required")
	}
	if input.LeaseDuration < time.Second || input.LeaseDuration > IntegrationInboxMaxLease {
		return IntegrationInboxRecord{}, false, inboxInvalid("lease must be between one second and five minutes")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("begin claim inbox: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.enterInboxIntegration(ctx, tx, input.ProjectID, input.IntegrationID); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	row, err := dbsqlc.New(tx).ClaimIntegrationInboxReceipt(ctx, dbsqlc.ClaimIntegrationInboxReceiptParams{
		ProjectID: input.ProjectID, IntegrationID: input.IntegrationID,
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

func (s *Store) ListReadyIntegrationInboxIntegrations(
	ctx context.Context, after IntegrationInboxIntegration, limit int,
) (IntegrationInboxIntegrationPage, error) {
	// Expired processing receipts are absent here; recovery must also run on empty polls.
	if err := validateInboxBatch(limit); err != nil {
		return IntegrationInboxIntegrationPage{}, err
	}
	rows, err := s.q.ListReadyIntegrationInboxIntegrations(ctx, dbsqlc.ListReadyIntegrationInboxIntegrationsParams{
		AfterProjectID: after.ProjectID, AfterIntegrationID: after.IntegrationID, RowLimit: int32(limit + 1),
	})
	if err != nil {
		return IntegrationInboxIntegrationPage{}, fmt.Errorf("list ready inbox integrations: %w", err)
	}
	result := IntegrationInboxIntegrationPage{Integrations: make([]IntegrationInboxIntegration, 0, len(rows))}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		result.NextCursor = IntegrationInboxIntegration{ProjectID: last.ProjectID, IntegrationID: last.IntegrationID}
	}
	for _, row := range rows {
		if row.Ready {
			result.Integrations = append(result.Integrations, IntegrationInboxIntegration{
				ProjectID: row.ProjectID, IntegrationID: row.IntegrationID,
			})
		}
	}
	return result, nil
}

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

func (s *Store) RecoverIntegrationInbox(ctx context.Context, limit int) (int64, error) {
	// Separate budgets prevent either backlog from starving the other.
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
	if input.ProjectID == uuid.Nil || input.IntegrationID == uuid.Nil {
		return inboxInvalid("project and integration are required")
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
		ID: row.ID, ProjectID: row.ProjectID, IntegrationID: row.IntegrationID, ReceiptKey: row.ReceiptKey,
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

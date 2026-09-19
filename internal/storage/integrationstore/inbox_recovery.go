package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// RetryFailedIntegrationInbox grants another bounded attempt budget without
// changing frozen recipients, preparation or committed admission results.
func (s *Store) RetryFailedIntegrationInbox(ctx context.Context, projectID, receiptID uuid.UUID) error {
	return s.recoverFailedInbox(ctx, projectID, receiptID, false)
}

// DiscardFailedIntegrationInbox releases only a wholly uncommitted launch selection.
// Its receipt and plan remain retained for diagnosis and transport deduplication.
func (s *Store) DiscardFailedIntegrationInbox(ctx context.Context, projectID, receiptID uuid.UUID) error {
	return s.recoverFailedInbox(ctx, projectID, receiptID, true)
}

func (s *Store) recoverFailedInbox(ctx context.Context, projectID, receiptID uuid.UUID, discard bool) error {
	if projectID == uuid.Nil || receiptID == uuid.Nil {
		return inboxInvalid("project and receipt are required")
	}
	receipt, err := s.GetIntegrationInbox(ctx, projectID, receiptID)
	if err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin inbox recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	connection, err := getIntegrationConnection(ctx, q, projectID, receipt.ConnectionID)
	if err != nil {
		return err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, connection.OrgID, projectID); err != nil {
		return err
	}
	if err := q.LockIntegrationConnectionLifecycleShared(ctx,
		dbsqlc.LockIntegrationConnectionLifecycleSharedParams{ConnectionID: receipt.ConnectionID}); err != nil {
		return err
	}
	// Recheck after waiting for revocation. Discard also works on disabled live
	// connections; retry cannot grant authority to an inactive connection.
	connection, err = getIntegrationConnection(ctx, q, projectID, receipt.ConnectionID)
	if err != nil {
		return err
	}
	if !discard && connection.State != IntegrationConnectionStateActive {
		return storeerr.ErrUnauthorized
	}
	if _, err := q.LockIntegrationInboxReceipt(ctx, dbsqlc.LockIntegrationInboxReceiptParams{
		ProjectID: projectID, ID: receiptID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock inbox recovery: %w", err)
	}
	row, err := q.GetIntegrationInboxReceipt(
		ctx,
		dbsqlc.GetIntegrationInboxReceiptParams{ProjectID: projectID, ID: receiptID},
	)
	if err != nil {
		return err
	}
	receipt = inboxRecord(row)
	if receipt.State != IntegrationInboxFailed {
		return storeerr.ErrStateTransitionConflict
	}
	identities, err := inboxSelectionIdentities(receipt.Plan, receipt.ConnectionID)
	if err != nil {
		return err
	}
	// Sharing this gate with empty-plan decisions makes operator retry the
	// precise boundary at which plain follow-ups begin waiting for a listener again.
	if err := lockInboxSelectionConversations(ctx, tx, projectID, receipt.ConnectionID, identities); err != nil {
		return err
	}
	var rows int64
	if discard {
		var plan map[string]struct {
			Launch *json.RawMessage `json:"launch"`
		}
		if len(receipt.Plan) != 0 {
			if err := json.Unmarshal(receipt.Plan, &plan); err != nil {
				return fmt.Errorf("decode inbox launch slots: %w", err)
			}
		}
		var progress map[string]map[string]json.RawMessage
		if err := json.Unmarshal(receipt.Progress, &progress); err != nil {
			return fmt.Errorf("decode inbox progress: %w", err)
		}
		for slot, stages := range progress {
			if _, committed := stages["committed"]; committed && plan[slot].Launch != nil {
				return fmt.Errorf("cannot discard committed inbox admission: %w", storeerr.ErrStateTransitionConflict)
			}
		}
		for identity := range identities {
			targets, err := q.ListConversationSelections(ctx, dbsqlc.ListConversationSelectionsParams{
				ProjectID:    projectID,
				ConnectionID: receipt.ConnectionID,
				Kind:         identity.Address.Kind,
				Ref:          identity.Address.Ref,
			})
			if err != nil {
				return err
			}
			for _, target := range targets {
				if target.AppID != nil && *target.AppID == identity.AppID {
					return fmt.Errorf("cannot discard retained app selection: %w", storeerr.ErrStateTransitionConflict)
				}
			}
		}
		rows, err = q.DiscardFailedIntegrationInboxReceipt(ctx, dbsqlc.DiscardFailedIntegrationInboxReceiptParams{
			ProjectID: projectID, ID: receiptID,
		})
	} else {
		rows, err = q.RetryFailedIntegrationInboxReceipt(ctx, dbsqlc.RetryFailedIntegrationInboxReceiptParams{
			ProjectID: projectID, ID: receiptID,
		})
	}
	if err != nil {
		return fmt.Errorf("recover failed inbox: %w", err)
	}
	if rows != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return tx.Commit(ctx)
}

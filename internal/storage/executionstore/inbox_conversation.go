package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ErrInboxRecipientSettled means this slot needs no further provider preparation.
// It does not grant authority for other recipients in the same conversation.
var ErrInboxRecipientSettled = errors.New("inbox recipient is already settled")

// CheckInboxConversationAuthority also durably settles archived input recipients
// before provider preparation, returning ErrInboxRecipientSettled on success.
func (s *Store) CheckInboxConversationAuthority(
	ctx context.Context,
	lease appstore.AppInboxLease,
	key string,
	address appstore.ConversationAddress,
) error {
	snapshot, err := s.apps.GetAppInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return err
	}
	var slots map[string]struct {
		Launch *InboxLaunchPlan `json:"launch"`
	}
	if json.Unmarshal(snapshot.Plan, &slots) != nil {
		return storeerr.ErrInvalidRequest
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	var resources []uuid.UUID
	if slots[key].Launch != nil {
		slot, progress, err := decodeInboxLaunchSlot(snapshot, key)
		if err != nil {
			return err
		}
		if slot.Selection.Address != address {
			return storeerr.ErrUnauthorized
		}
		if progress.Committed != nil {
			return ErrInboxRecipientSettled
		}
		resources, err = launchAppIDsTx(ctx, q, slot.Launch.launchInput(lease.ProjectID))
		if err != nil {
			return err
		}
	}
	work, err := s.apps.LockAppInboxLeaseTx(ctx, tx, lease, resources...)
	if err != nil {
		return err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return storeerr.ErrIdempotencyConflict
	}
	if err := appstore.LockAppsTx(
		ctx,
		tx,
		lease.ProjectID,
		resources,
		locked.AppID,
	); err != nil {
		return err
	}
	if err := lockAppConversationsTx(
		ctx,
		tx,
		lease.ProjectID,
		AgentInputOrigin{AppID: locked.AppID, Address: address},
	); err != nil {
		return err
	}
	if slots[key].Launch != nil {
		slot, progress, err := decodeInboxLaunchSlot(locked, key)
		if err != nil {
			return err
		}
		if progress.Committed != nil {
			return ErrInboxRecipientSettled
		}
		if _, err := lockAgentProfileTx(ctx, q, lease.ProjectID, slot.Launch.ProfileID); err != nil {
			return err
		}
		config, err := loadAgentConfigTx(ctx, q, lease.ProjectID, slot.Launch.AgentConfigID)
		if err != nil {
			return err
		}
		if err := validateSavedAgentConfigModelContractTx(ctx, q, config); err != nil {
			return err
		}
	} else {
		slot, progress, _, err := decodeInboxInputSlot(locked, key)
		if err != nil {
			return err
		}
		if slot.Input.Origin.Address != address {
			return storeerr.ErrUnauthorized
		}
		if progress.Committed != nil {
			return ErrInboxRecipientSettled
		}
		settled, err := s.settleArchivedInboxInputTx(ctx, tx, work, key, slot)
		if err != nil {
			return err
		}
		if settled != nil {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return ErrInboxRecipientSettled
		}
		if slot.Subscription != nil {
			if err := validateInboxSubscriptionTx(ctx, tx, slot); err != nil {
				return err
			}
		}
	}
	_, err = q.ReadAppInboxLease(
		ctx,
		dbsqlc.ReadAppInboxLeaseParams{
			ProjectID:  lease.ProjectID,
			ID:         lease.ReceiptID,
			ClaimToken: lease.Token,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return appstore.ErrAppInboxLeaseLost
	}
	return err
}

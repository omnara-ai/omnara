package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ErrInboxRecipientSettled means this slot needs no further provider preparation.
// It does not grant authority for other recipients in the same conversation.
var ErrInboxRecipientSettled = errors.New("inbox recipient is already settled")

// CheckInboxConversationAuthority recognizes historical delivery and terminal
// archival before provider preparation, without persisting a separate outcome.
func (s *Store) CheckInboxConversationAuthority(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	key string,
	address integrationstore.ConversationAddress,
) error {
	snapshot, err := s.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return err
	}
	raw, err := inboxPlanSlot(snapshot, key)
	if err != nil {
		return err
	}
	var envelope struct {
		Launch *InboxLaunchPlan `json:"launch"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return storeerr.ErrInvalidRequest
	}
	var launch InboxLaunchSlot
	var input InboxInputSlot
	if envelope.Launch != nil {
		launch, err = decodeInboxLaunchSlot(snapshot, raw)
		if err != nil {
			return err
		}
		if launch.Selection.Address != address {
			return storeerr.ErrUnauthorized
		}
	} else {
		input, _, err = decodeInboxInputSlot(snapshot, raw)
		if err != nil {
			return err
		}
		if input.Input.Origin.Address != address {
			return storeerr.ErrUnauthorized
		}
	}
	if result, err := resolveInboxSlotOutcome(
		ctx, s.q, snapshot, raw,
	); err != nil || result.Outcome != InboxSlotPending {
		if err != nil {
			return err
		}
		return ErrInboxRecipientSettled
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	var resources []uuid.UUID
	if envelope.Launch != nil {
		resources, err = launchIntegrationIDsTx(ctx, q, launch.Launch.launchInput(lease.ProjectID))
		if err != nil {
			return err
		}
	}
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease, resources...)
	if err != nil {
		return err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return storeerr.ErrIdempotencyConflict
	}
	if result, err := resolveInboxSlotOutcome(ctx, q, locked, raw); err != nil || result.Outcome != InboxSlotPending {
		if err != nil {
			return err
		}
		return ErrInboxRecipientSettled
	}
	if err := integrationstore.LockIntegrationsTx(ctx, tx, lease.ProjectID, resources, locked.IntegrationID); err != nil {
		return err
	}
	if err := lockIntegrationConversationsTx(ctx, tx, lease.ProjectID,
		AgentInputOrigin{IntegrationID: locked.IntegrationID, Address: address}); err != nil {
		return err
	}
	if envelope.Launch != nil {
		if _, err := lockAgentProfileTx(ctx, q, lease.ProjectID, launch.Launch.ProfileID); err != nil {
			return err
		}
		config, err := loadAgentConfigTx(ctx, q, lease.ProjectID, launch.Launch.AgentConfigID)
		if err != nil {
			return err
		}
		if err := validateSavedAgentConfigModelContractTx(ctx, q, config); err != nil {
			return err
		}
	} else {
		if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
			ProjectID: lease.ProjectID, AgentID: input.AgentID,
		}}); err != nil {
			return err
		}
		if result, err := resolveInboxInputOutcome(ctx, q, input); err != nil || result.Outcome != InboxSlotPending {
			if err != nil {
				return err
			}
			return ErrInboxRecipientSettled
		}
		if input.Subscription != nil {
			if err := validateInboxSubscriptionTx(ctx, tx, input); err != nil {
				return err
			}
		}
	}
	return work.CheckLease(ctx)
}

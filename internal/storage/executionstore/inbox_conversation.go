package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CheckInboxConversationAuthority authorizes provider conversation preparation
// from a frozen, uncommitted recipient. It performs no writes or provider I/O.
// Call before each provider request; admission still rechecks all authority.
// Frozen profile selections survive launcher edits. App disconnection, profile
// deletion, config revocation and subscription removal take effect immediately.
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
	var slots map[string]struct {
		Launch *LaunchAgentInput `json:"launch"`
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
	var resources []string
	var apps []uuid.UUID
	if slots[key].Launch != nil {
		slot, progress, err := decodeInboxLaunchSlot(snapshot, key)
		if err != nil {
			return err
		}
		if progress.Committed != nil || slot.Selection.Address != address {
			return storeerr.ErrUnauthorized
		}
		resources, err = launchAppIDsTx(ctx, q, slot.Launch)
		if err != nil {
			return err
		}
		for _, ref := range resources {
			id, err := publicid.Decode(publicid.KindProjectApp, ref)
			if err != nil {
				return err
			}
			apps = append(apps, id)
		}
	}
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease, apps...)
	if err != nil {
		return err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return storeerr.ErrIdempotencyConflict
	}
	if err := integrationstore.LockAppsTx(
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
			return storeerr.ErrUnauthorized
		}
		if _, err := lockAgentProfileTx(ctx, q, lease.ProjectID, slot.Launch.ProfileID); err != nil {
			return err
		}
		var config AgentConfigRecord
		if slot.Launch.DerivedConfig != nil {
			project, err := loadProjectTx(ctx, q, lease.ProjectID)
			if err != nil {
				return err
			}
			config = AgentConfigRecord{
				OrgID:             project.OrgID,
				ConfiguredModelID: slot.Launch.DerivedConfig.ConfiguredModelID,
			}
		} else {
			config, err = loadAgentConfigTx(ctx, q, lease.ProjectID, slot.Launch.AgentConfigID)
			if err != nil {
				return err
			}
		}
		if err := lockAgentConfigModelForUseTx(ctx, q, config); err != nil {
			return err
		}
	} else {
		slot, progress, _, err := decodeInboxInputSlot(locked, key)
		if err != nil {
			return err
		}
		if progress.Committed != nil || slot.Input.Origin.Address != address {
			return storeerr.ErrUnauthorized
		}
		if err := lifecyclelock.Agents(
			ctx,
			tx,
			[]lifecyclelock.AgentRef{{ProjectID: lease.ProjectID, AgentID: slot.AgentID}},
		); err != nil {
			return err
		}
		agent, err := loadAgentInProjectTx(ctx, tx, lease.ProjectID, slot.AgentID)
		if err != nil {
			return err
		}
		if agent.State == AgentStateArchived {
			return storeerr.ErrUnauthorized
		}
		if slot.Subscription != nil {
			if err := validateInboxSubscriptionTx(ctx, tx, slot); err != nil {
				return err
			}
		}
	}
	// Fence again after profile/model/agent lock waits without entering an earlier
	// lock class. The transaction ends before the caller performs provider I/O.
	_, err = q.ReadIntegrationInboxLease(
		ctx,
		dbsqlc.ReadIntegrationInboxLeaseParams{
			ProjectID:  lease.ProjectID,
			ID:         lease.ReceiptID,
			ClaimToken: lease.Token,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return integrationstore.ErrIntegrationInboxLeaseLost
	}
	return err
}

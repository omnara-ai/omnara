package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CheckInboxConversationAuthority authorizes provider conversation preparation
// from a frozen, uncommitted recipient. It performs no writes or provider I/O.
// Call before each provider request; admission still rechecks all authority.
// Frozen profile selections survive app disable, while profile deletion, config
// connection revocation and listener removal take effect immediately.
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
	var resources map[string]agentconfig.AppResourceCompiled
	var connections []uuid.UUID
	if slots[key].Launch != nil {
		slot, progress, err := decodeInboxLaunchSlot(snapshot, key)
		if err != nil {
			return err
		}
		if progress.Committed != nil || slot.Selection.Address != address {
			return storeerr.ErrUnauthorized
		}
		resources, err = launchAppResourcesTx(ctx, q, slot.Launch)
		if err != nil {
			return err
		}
		for _, resource := range resources {
			if !resource.Enabled || resource.ConnectionID == "" {
				continue
			}
			id, err := publicid.Decode(publicid.KindIntegrationConnection, resource.ConnectionID)
			if err != nil {
				return err
			}
			connections = append(connections, id)
		}
	}
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease, connections...)
	if err != nil {
		return err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return storeerr.ErrIdempotencyConflict
	}
	if err := integrationstore.LockAppConnectionsTx(
		ctx,
		tx,
		lease.ProjectID,
		resources,
		locked.ConnectionID,
	); err != nil {
		return err
	}
	if err := lockAppConversationsTx(
		ctx,
		tx,
		lease.ProjectID,
		resources,
		AgentInputOrigin{ConnectionID: locked.ConnectionID, Address: address},
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
		if slot.Listener != nil {
			if err := validateInboxListenerTx(ctx, tx, slot); err != nil {
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

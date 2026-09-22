package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type InboxLaunchSlot struct {
	Selection   integrationstore.InboxAppSelection `json:"selection"`
	AgentID     uuid.UUID                          `json:"agent_id"`
	Launch      LaunchAgentInput                   `json:"launch"`
	ArtifactIDs []uuid.UUID                        `json:"artifact_ids,omitempty"`
}

type InboxLaunchPreparation struct {
	Artifacts []artifactstore.PreparedArtifact `json:"artifacts"`
}

type inboxLaunchCommit struct {
	AgentID  uuid.UUID `json:"agent_id"`
	ConfigID uuid.UUID `json:"config_id"`
	InputID  uuid.UUID `json:"input_id"`
	TargetID uuid.UUID `json:"target_id"`
}

type inboxLaunchProgress struct {
	Prepared  *InboxLaunchPreparation `json:"prepared"`
	Committed *inboxLaunchCommit      `json:"committed"`
}

func (s *Store) AdmitInboxLaunchSlot(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	slotKey string,
) (LaunchAgentResult, error) {
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil || slotKey == "" {
		return LaunchAgentResult{}, storeerr.InvalidRequest(errors.New("inbox lease and slot are required"))
	}
	return storeutil.RetryTransaction(ctx, "admit_inbox_launch_slot", func() (LaunchAgentResult, error) {
		return s.admitInboxLaunchSlotOnce(ctx, lease, slotKey)
	})
}

func (s *Store) admitInboxLaunchSlotOnce(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	slotKey string,
) (LaunchAgentResult, error) {
	// Read the immutable plan first to discover app gates that must precede the receipt lock.
	snapshot, err := s.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	slot, progress, err := decodeInboxLaunchSlot(snapshot, slotKey)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if progress.Committed != nil {
		return s.replayInboxLaunch(ctx, lease.ProjectID, slot, *progress.Committed)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LaunchAgentResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	resources, err := launchAppIDsTx(ctx, q, slot.Launch)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	var apps []uuid.UUID
	for _, ref := range resources {
		id, err := publicid.Decode(publicid.KindProjectApp, ref)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		apps = append(apps, id)
	}
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease, apps...)
	if err != nil {
		// Release the connection before diagnostic reads, which may need the pool's only session.
		_ = tx.Rollback(ctx)
		// Another attempt may have committed and released its lease while this worker waited.
		latest, readErr := s.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if readErr == nil {
			latestSlot, latestProgress, decodeErr := decodeInboxLaunchSlot(latest, slotKey)
			if decodeErr == nil && latestProgress.Committed != nil {
				return s.replayInboxLaunch(ctx, lease.ProjectID, latestSlot, *latestProgress.Committed)
			}
		}
		return LaunchAgentResult{}, err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return LaunchAgentResult{}, storeerr.ErrIdempotencyConflict
	}
	slot, progress, err = decodeInboxLaunchSlot(locked, slotKey)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if progress.Committed != nil {
		_ = tx.Rollback(ctx)
		return s.replayInboxLaunch(ctx, lease.ProjectID, slot, *progress.Committed)
	}
	artifacts, err := validateInboxLaunchPreparation(slot, progress.Prepared)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	// These gates are already held. Recheck only this slot's authority so another
	// slot's revocation cannot block its independent progress.
	if err := integrationstore.LockAppsTx(ctx, tx, lease.ProjectID, resources); err != nil {
		return LaunchAgentResult{}, err
	}
	selection := slot.Selection
	origins := []AgentInputOrigin{{AppID: selection.AppID, Address: selection.Address}}
	for _, attachment := range slot.Launch.Subscriptions {
		prepared, err := integrationstore.PrepareAppSubscriptionTx(ctx, tx, lease.ProjectID, attachment)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		origins = append(origins, AgentInputOrigin{AppID: prepared.AppID, Address: prepared.Address})
	}
	if err := lockAppConversationsTx(ctx, tx, lease.ProjectID, origins...); err != nil {
		return LaunchAgentResult{}, err
	}
	scheduled := locked.Source == integrationstore.IntegrationInboxSourceScheduled
	if scheduled {
		app, err := s.integrations.GetProjectAppByIDTx(ctx, tx, locked.AppID)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		if err := validateScheduledInboxLaunch(locked, app, slot); err != nil {
			return LaunchAgentResult{}, err
		}
	}
	slot.Launch.admission = &launchAdmission{
		Scheduled:     scheduled,
		AgentID:       slot.AgentID,
		AppID:         selection.AppID,
		SelectionSlot: selection.Slot,
		Artifacts:     artifacts,
	}
	txNotifications := s.newTxNotifications()
	result, err := s.launchAgentTx(ctx, tx, q, txNotifications, slot.Launch)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if !result.Created || result.Agent.ID != slot.AgentID {
		return LaunchAgentResult{}, storeerr.ErrIdempotencyConflict
	}
	committed, err := json.Marshal(
		inboxLaunchCommit{
			AgentID:  result.Agent.ID,
			ConfigID: result.AgentConfig.ID,
			InputID:  result.AgentInput.ID,
			TargetID: result.IntegrationTarget.ID,
		},
	)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if err := work.CommitSlot(ctx, slotKey, committed); err != nil {
		return LaunchAgentResult{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "admit inbox launch slot"); err != nil {
		return LaunchAgentResult{}, err
	}
	return result, nil
}

func decodeInboxLaunchSlot(
	receipt integrationstore.IntegrationInboxRecord,
	slotKey string,
) (InboxLaunchSlot, inboxLaunchProgress, error) {
	var slot InboxLaunchSlot
	var progress inboxLaunchProgress
	fail := func(message string) (InboxLaunchSlot, inboxLaunchProgress, error) {
		return slot, progress, storeerr.InvalidRequest(errors.New(message))
	}
	var slots map[string]json.RawMessage
	if err := json.Unmarshal(receipt.Plan, &slots); err != nil || len(slots[slotKey]) == 0 {
		return fail("launch slot is missing from frozen plan")
	}
	if err := json.Unmarshal(slots[slotKey], &slot); err != nil {
		return fail("invalid frozen launch slot")
	}
	if slot.AgentID.Version() != 7 || slot.AgentID.Variant() != uuid.RFC4122 {
		return fail("planned agent requires a UUIDv7 identity")
	}
	selection := slot.Selection
	if selection.AppID == uuid.Nil || selection.AppID != receipt.AppID || selection.Slot == "" {
		return fail("launch selection must belong to the receipt app and name an app slot")
	}
	if slot.Launch.ProjectID == uuid.Nil {
		slot.Launch.ProjectID = receipt.ProjectID
	}
	if slot.Launch.ProjectID != receipt.ProjectID || slot.Launch.ProfileID == uuid.Nil || slot.Launch.Subagent != nil ||
		slot.Launch.IdempotencyKey == "" {
		return fail("inbox launch requires a same-project profile launch and a frozen idempotency key")
	}
	var err error
	slot.Launch, err = validateLaunchAgentInput(slot.Launch)
	if err != nil {
		return slot, progress, err
	}
	initial, _, err := prepareLaunchInitialInput(slot.Launch)
	if err != nil {
		return slot, progress, err
	}
	if slot.Launch.InitialInput == nil || initial.Origin == nil || initial.Actor == nil ||
		initial.Origin.AppID != selection.AppID || initial.Origin.Address != selection.Address {
		return fail("inbox launch requires initial content, actor and origin matching its frozen selection")
	}
	var stages map[string]json.RawMessage
	if err := json.Unmarshal(receipt.Progress, &stages); err != nil {
		return fail("invalid inbox launch progress")
	}
	if raw := stages[slotKey]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &progress); err != nil {
			return fail("invalid inbox launch slot progress")
		}
	}
	return slot, progress, nil
}

func validateInboxLaunchPreparation(
	slot InboxLaunchSlot,
	prepared *InboxLaunchPreparation,
) ([]artifactstore.PreparedArtifact, error) {
	_, blocks, err := prepareLaunchInitialInput(slot.Launch)
	if err != nil {
		return nil, err
	}
	var artifacts []artifactstore.PreparedArtifact
	if prepared != nil {
		artifacts = prepared.Artifacts
	}
	return validateInboxPreparedArtifacts(slot.ArtifactIDs, blocks, artifacts)
}

func validateInboxPreparedArtifacts(
	ids []uuid.UUID,
	blocks []CreateContentBlockInput,
	artifacts []artifactstore.PreparedArtifact,
) ([]artifactstore.PreparedArtifact, error) {
	invalid := func(message string) ([]artifactstore.PreparedArtifact, error) {
		return nil, storeerr.InvalidRequest(errors.New(message))
	}
	planned := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id.Version() != 7 || id.Variant() != uuid.RFC4122 || planned[id] {
			return invalid("planned artifact IDs must be distinct UUIDv7 identities")
		}
		planned[id] = true
	}
	referenced := map[uuid.UUID]bool{}
	for _, block := range blocks {
		if block.ArtifactID != uuid.Nil {
			if !planned[block.ArtifactID] {
				return invalid("input file reference is not frozen in the plan")
			}
			referenced[block.ArtifactID] = true
		}
	}
	if len(referenced) != len(planned) {
		return invalid("every planned artifact must be referenced by the input")
	}
	if len(artifacts) != len(planned) {
		return invalid("input files require durable preparation for every planned artifact")
	}
	for _, artifact := range artifacts {
		if !planned[artifact.ID] {
			return invalid("prepared artifact identity differs from the frozen plan")
		}
		delete(planned, artifact.ID)
		if err := artifact.Validate(); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

func (s *Store) replayInboxLaunch(
	ctx context.Context,
	projectID uuid.UUID,
	slot InboxLaunchSlot,
	committed inboxLaunchCommit,
) (LaunchAgentResult, error) {
	if committed.AgentID != slot.AgentID || committed.ConfigID == uuid.Nil || committed.InputID == uuid.Nil ||
		committed.TargetID == uuid.Nil {
		return LaunchAgentResult{}, fmt.Errorf(
			"committed launch differs from frozen identity: %w",
			storeerr.ErrIdempotencyConflict,
		)
	}
	agent, err := s.GetAgentInProject(ctx, projectID, slot.AgentID)
	return LaunchAgentResult{Agent: agent}, err
}

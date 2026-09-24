package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type InboxLaunchSlot struct {
	Files       []InboxPlannedFile                         `json:"files,omitempty"`
	Selection   integrationstore.InboxIntegrationSelection `json:"selection"`
	AgentID     uuid.UUID                                  `json:"agent_id"`
	Launch      InboxLaunchPlan                            `json:"launch"`
	ArtifactIDs []uuid.UUID                                `json:"artifact_ids,omitempty"`
}

type InboxLaunchPrincipal struct {
	Type string    `json:"type"`
	ID   uuid.UUID `json:"id"`
}

type InboxLaunchPlan struct {
	ProfileID           uuid.UUID                                            `json:"profile_id"`
	AgentConfigID       uuid.UUID                                            `json:"agent_config_id"`
	DerivedBaseConfigID uuid.UUID                                            `json:"derived_base_config_id"`
	LaunchedBy          InboxLaunchPrincipal                                 `json:"launched_by"`
	IdempotencyKey      string                                               `json:"idempotency_key"`
	InitialInput        *LaunchInitialInput                                  `json:"initial_input"`
	Subscriptions       []integrationstore.IntegrationSubscriptionAttachment `json:"subscriptions"`
}

func (p InboxLaunchPlan) launchInput(projectID uuid.UUID) LaunchAgentInput {
	return LaunchAgentInput{
		ProjectID: projectID, ProfileID: p.ProfileID, AgentConfigID: p.AgentConfigID,
		DerivedBaseConfigID: p.DerivedBaseConfigID,
		LaunchedBy:          identitystore.PrincipalRecord{Type: p.LaunchedBy.Type, ID: p.LaunchedBy.ID},
		IdempotencyKey:      p.IdempotencyKey, InitialInput: p.InitialInput, Subscriptions: p.Subscriptions,
	}
}

func (s *Store) AdmitInboxLaunchSlot(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	slotKey string,
	artifacts []artifactstore.PreparedArtifact,
) (LaunchAgentResult, error) {
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil || slotKey == "" {
		return LaunchAgentResult{}, storeerr.InvalidRequest(errors.New("inbox lease and slot are required"))
	}
	return storeutil.RetryTransaction(ctx, "admit_inbox_launch_slot", func() (LaunchAgentResult, error) {
		return s.admitInboxLaunchSlotOnce(ctx, lease, slotKey, artifacts)
	})
}

func (s *Store) admitInboxLaunchSlotOnce(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	slotKey string,
	artifacts []artifactstore.PreparedArtifact,
) (LaunchAgentResult, error) {
	// Read the immutable plan first to discover integration gates that must precede the receipt lock.
	snapshot, err := s.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	raw, err := inboxPlanSlot(snapshot, slotKey)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	slot, err := decodeInboxLaunchSlot(snapshot, raw)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if outcome, err := resolveInboxLaunchOutcome(
		ctx, s.q, snapshot.ProjectID, slot,
	); err != nil || outcome.Outcome != InboxSlotPending {
		return LaunchAgentResult{Agent: outcome.Agent}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LaunchAgentResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	resources, err := launchIntegrationIDsTx(ctx, q, slot.Launch.launchInput(lease.ProjectID))
	if err != nil {
		return LaunchAgentResult{}, err
	}
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease, resources...)
	if err != nil {
		// Release the connection before diagnostic reads, which may need the pool's only session.
		_ = tx.Rollback(ctx)
		// Another attempt may have committed and released its lease while this worker waited.
		if outcome, readErr := resolveInboxLaunchOutcome(
			ctx, s.q, snapshot.ProjectID, slot,
		); readErr == nil && outcome.Outcome != InboxSlotPending {
			return LaunchAgentResult{Agent: outcome.Agent}, nil
		}
		return LaunchAgentResult{}, err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return LaunchAgentResult{}, storeerr.ErrIdempotencyConflict
	}
	if outcome, err := resolveInboxLaunchOutcome(
		ctx, q, locked.ProjectID, slot,
	); err != nil || outcome.Outcome != InboxSlotPending {
		return LaunchAgentResult{Agent: outcome.Agent}, err
	}
	artifacts, err = validateInboxLaunchArtifacts(slot, artifacts)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	// These gates are already held. Recheck only this slot's authority so another
	// slot's revocation cannot block its independent progress.
	if err := integrationstore.LockIntegrationsTx(ctx, tx, lease.ProjectID, resources); err != nil {
		return LaunchAgentResult{}, err
	}
	selection := slot.Selection
	origins := []AgentInputOrigin{{IntegrationID: selection.IntegrationID, Address: selection.Address}}
	for _, attachment := range slot.Launch.Subscriptions {
		prepared, err := integrationstore.PrepareIntegrationSubscriptionTx(ctx, tx, lease.ProjectID, attachment)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		origins = append(origins, AgentInputOrigin{IntegrationID: prepared.IntegrationID, Address: prepared.Address})
	}
	if err := lockIntegrationConversationsTx(ctx, tx, lease.ProjectID, origins...); err != nil {
		return LaunchAgentResult{}, err
	}
	scheduled := locked.Source == integrationstore.IntegrationInboxSourceScheduled
	if scheduled {
		integration, err := s.integrations.GetProjectIntegrationByIDTx(ctx, tx, locked.IntegrationID)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		if err := validateScheduledInboxLaunch(locked, integration, slot); err != nil {
			return LaunchAgentResult{}, err
		}
	}
	launch := slot.Launch.launchInput(lease.ProjectID)
	launch.admission = &launchAdmission{
		Scheduled:     scheduled,
		AgentID:       slot.AgentID,
		IntegrationID: selection.IntegrationID,
		SelectionSlot: selection.Slot,
		Artifacts:     artifacts,
	}
	txNotifications := s.newTxNotifications()
	result, err := s.launchAgentTx(ctx, tx, q, txNotifications, launch)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if !result.Created || result.Agent.ID != slot.AgentID {
		return LaunchAgentResult{}, storeerr.ErrIdempotencyConflict
	}
	if err := work.CheckLease(ctx); err != nil {
		return LaunchAgentResult{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "admit inbox launch slot"); err != nil {
		return LaunchAgentResult{}, err
	}
	return result, nil
}

func decodeInboxLaunchSlot(
	receipt integrationstore.IntegrationInboxRecord,
	raw json.RawMessage,
) (InboxLaunchSlot, error) {
	var slot InboxLaunchSlot
	fail := func(message string) (InboxLaunchSlot, error) {
		return slot, storeerr.InvalidRequest(errors.New(message))
	}
	if err := json.Unmarshal(raw, &slot); err != nil {
		return fail("invalid frozen launch slot")
	}
	if slot.AgentID.Version() != 7 || slot.AgentID.Variant() != uuid.RFC4122 {
		return fail("planned agent requires a UUIDv7 identity")
	}
	selection := slot.Selection
	if selection.IntegrationID == uuid.Nil || selection.IntegrationID != receipt.IntegrationID || selection.Slot == "" {
		return fail("launch selection must belong to the receipt integration and name an integration slot")
	}
	if slot.Launch.ProfileID == uuid.Nil || slot.Launch.AgentConfigID == uuid.Nil ||
		slot.Launch.DerivedBaseConfigID == uuid.Nil || slot.Launch.IdempotencyKey == "" {
		return fail("inbox launch requires profile, derived and base config identities and an idempotency key")
	}
	launch, err := validateLaunchAgentInput(slot.Launch.launchInput(receipt.ProjectID))
	if err != nil {
		return slot, err
	}
	initial, _, err := prepareLaunchInitialInput(launch)
	if err != nil {
		return slot, err
	}
	if slot.Launch.InitialInput == nil || initial.Origin == nil || initial.Actor == nil ||
		initial.Origin.IntegrationID != selection.IntegrationID || initial.Origin.Address != selection.Address {
		return fail("inbox launch requires initial content, actor and origin matching its frozen selection")
	}
	return slot, nil
}

func validateInboxLaunchArtifacts(
	slot InboxLaunchSlot,
	artifacts []artifactstore.PreparedArtifact,
) ([]artifactstore.PreparedArtifact, error) {
	_, blocks, err := prepareLaunchInitialInput(LaunchAgentInput{InitialInput: slot.Launch.InitialInput})
	if err != nil {
		return nil, err
	}
	return validateInboxPreparedArtifacts(slot.ArtifactIDs, slot.Files, blocks, artifacts)
}

func validateInboxPreparedArtifacts(
	ids []uuid.UUID,
	files []InboxPlannedFile,
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
		return invalid("input files require verified artifacts for every planned artifact")
	}
	expected := make(map[uuid.UUID]artifactstore.PreparedArtifact, len(files))
	for _, file := range files {
		if file.Expected == nil || file.Expected.ID != file.ArtifactID || !planned[file.ArtifactID] {
			return invalid("planned file requires expected metadata matching its artifact identity")
		}
		if _, exists := expected[file.ArtifactID]; exists {
			return invalid("planned files must have distinct artifact identities")
		}
		expected[file.ArtifactID] = *file.Expected
	}
	if len(expected) != len(planned) {
		return invalid("every planned artifact requires expected file metadata")
	}
	for _, artifact := range artifacts {
		if !planned[artifact.ID] {
			return invalid("prepared artifact identity differs from the frozen plan")
		}
		delete(planned, artifact.ID)
		if artifact != expected[artifact.ID] {
			return invalid("prepared artifact metadata differs from the frozen plan")
		}
		if err := artifact.Validate(); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

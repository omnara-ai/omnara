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

type InboxLaunchRecipient struct {
	InitialInput *LaunchInitialInput               `json:"-"`
	Files        []InboxPlannedFile                `json:"-"`
	LaunchClaim  integrationstore.InboxLaunchClaim `json:"launch_claim"`
	AgentID      uuid.UUID                         `json:"agent_id"`
	Launch       InboxLaunchPlan                   `json:"launch"`
	ArtifactIDs  []uuid.UUID                       `json:"artifact_ids,omitempty"`
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
	Subscriptions       []integrationstore.IntegrationSubscriptionAttachment `json:"subscriptions"`
}

func (p InboxLaunchPlan) launchInput(projectID uuid.UUID, initial *LaunchInitialInput) LaunchAgentInput {
	return LaunchAgentInput{
		ProjectID: projectID, ProfileID: p.ProfileID, AgentConfigID: p.AgentConfigID,
		DerivedBaseConfigID: p.DerivedBaseConfigID,
		LaunchedBy:          identitystore.PrincipalRecord{Type: p.LaunchedBy.Type, ID: p.LaunchedBy.ID},
		IdempotencyKey:      p.IdempotencyKey, InitialInput: initial, Subscriptions: p.Subscriptions,
	}
}

func (s *Store) AdmitInboxLaunchRecipient(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	recipientKey string,
	artifacts []artifactstore.PreparedArtifact,
) (LaunchAgentResult, error) {
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil || recipientKey == "" {
		return LaunchAgentResult{}, storeerr.InvalidRequest(errors.New("inbox lease and recipient are required"))
	}
	return storeutil.RetryTransaction(ctx, "admit_inbox_launch_recipient", func() (LaunchAgentResult, error) {
		return s.admitInboxLaunchRecipientOnce(ctx, lease, recipientKey, artifacts)
	})
}

func (s *Store) admitInboxLaunchRecipientOnce(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	recipientKey string,
	artifacts []artifactstore.PreparedArtifact,
) (LaunchAgentResult, error) {
	// Read the immutable plan first to discover integration gates that must precede the receipt lock.
	snapshot, err := s.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	raw, err := inboxPlanRecipient(snapshot, recipientKey)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	recipient, err := decodeInboxLaunchRecipient(snapshot, raw)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if outcome, err := resolveInboxLaunchOutcome(
		ctx, s.q, snapshot.ProjectID, recipient,
	); err != nil || outcome.Outcome != InboxRecipientPending {
		return LaunchAgentResult{Agent: outcome.Agent}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LaunchAgentResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	resources, err := launchIntegrationIDsTx(ctx, q, recipient.Launch.launchInput(lease.ProjectID, recipient.InitialInput))
	if err != nil {
		return LaunchAgentResult{}, err
	}
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease, resources...)
	if err != nil {
		// Release the connection before diagnostic reads, which may need the pool's only session.
		_ = tx.Rollback(ctx)
		// Another attempt may have committed and released its lease while this worker waited.
		if outcome, readErr := resolveInboxLaunchOutcome(
			ctx, s.q, snapshot.ProjectID, recipient,
		); readErr == nil && outcome.Outcome != InboxRecipientPending {
			return LaunchAgentResult{Agent: outcome.Agent}, nil
		}
		return LaunchAgentResult{}, err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return LaunchAgentResult{}, storeerr.ErrIdempotencyConflict
	}
	if outcome, err := resolveInboxLaunchOutcome(
		ctx, q, locked.ProjectID, recipient,
	); err != nil || outcome.Outcome != InboxRecipientPending {
		return LaunchAgentResult{Agent: outcome.Agent}, err
	}
	artifacts, err = validateInboxLaunchArtifacts(recipient, artifacts)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	// These gates are already held. Recheck only this recipient's authority so another
	// recipient's revocation cannot block its independent progress.
	if err := integrationstore.LockIntegrationsTx(ctx, tx, lease.ProjectID, resources); err != nil {
		return LaunchAgentResult{}, err
	}
	claim := recipient.LaunchClaim
	origins := []AgentInputOrigin{{IntegrationID: claim.IntegrationID, Address: claim.Address}}
	for _, attachment := range recipient.Launch.Subscriptions {
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
		integration, err := s.integrations.GetIntegrationByIDTx(ctx, tx, locked.IntegrationID)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		if err := validateScheduledInboxLaunch(locked, integration, recipient); err != nil {
			return LaunchAgentResult{}, err
		}
	}
	launch := recipient.Launch.launchInput(lease.ProjectID, recipient.InitialInput)
	launch.admission = &launchAdmission{
		Scheduled:     scheduled,
		AgentID:       recipient.AgentID,
		IntegrationID: claim.IntegrationID,
		LaunchKey:     claim.LaunchKey,
		Artifacts:     artifacts,
	}
	txNotifications := s.newTxNotifications()
	result, err := s.launchAgentTx(ctx, tx, q, txNotifications, launch)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if !result.Created || result.Agent.ID != recipient.AgentID {
		return LaunchAgentResult{}, storeerr.ErrIdempotencyConflict
	}
	if err := work.CheckLease(ctx); err != nil {
		return LaunchAgentResult{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "admit inbox launch recipient"); err != nil {
		return LaunchAgentResult{}, err
	}
	return result, nil
}

func decodeInboxLaunchRecipient(
	receipt integrationstore.IntegrationInboxRecord,
	raw json.RawMessage,
) (InboxLaunchRecipient, error) {
	var recipient InboxLaunchRecipient
	fail := func(message string) (InboxLaunchRecipient, error) {
		return recipient, storeerr.InvalidRequest(errors.New(message))
	}
	if err := json.Unmarshal(raw, &recipient); err != nil {
		return fail("invalid frozen launch recipient")
	}
	if recipient.AgentID.Version() != 7 || recipient.AgentID.Variant() != uuid.RFC4122 {
		return fail("planned agent requires a UUIDv7 identity")
	}
	claim := recipient.LaunchClaim
	if claim.IntegrationID == uuid.Nil || claim.IntegrationID != receipt.IntegrationID ||
		claim.LaunchKey == "" {
		return fail("launch claim must belong to the receipt integration and name a launch key")
	}
	if recipient.Launch.ProfileID == uuid.Nil || recipient.Launch.AgentConfigID == uuid.Nil ||
		recipient.Launch.DerivedBaseConfigID == uuid.Nil || recipient.Launch.IdempotencyKey == "" {
		return fail("inbox launch requires profile, derived and base config identities and an idempotency key")
	}
	message, err := inboxMessage(receipt)
	if err != nil {
		return recipient, err
	}
	content, files, err := message.RecipientContent(recipient.ArtifactIDs)
	if err != nil {
		return recipient, err
	}
	recipient.Files, recipient.InitialInput = files, message.initialInput(content)
	launch, err := validateLaunchAgentInput(recipient.Launch.launchInput(receipt.ProjectID, recipient.InitialInput))
	if err != nil {
		return recipient, err
	}
	initial, _, err := prepareLaunchInitialInput(launch)
	if err != nil {
		return recipient, err
	}
	if recipient.InitialInput == nil || initial.Origin == nil || initial.Actor == nil ||
		initial.Origin.IntegrationID != claim.IntegrationID || initial.Origin.Address != claim.Address {
		return fail("inbox launch requires initial content, actor and origin matching its frozen claim")
	}
	return recipient, nil
}

func validateInboxLaunchArtifacts(
	recipient InboxLaunchRecipient,
	artifacts []artifactstore.PreparedArtifact,
) ([]artifactstore.PreparedArtifact, error) {
	_, blocks, err := prepareLaunchInitialInput(LaunchAgentInput{InitialInput: recipient.InitialInput})
	if err != nil {
		return nil, err
	}
	return validateInboxPreparedArtifacts(recipient.ArtifactIDs, recipient.Files, blocks, artifacts)
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

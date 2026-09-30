package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type InboxRecipientOutcome string

const (
	InboxRecipientPending   InboxRecipientOutcome = ""
	InboxRecipientDelivered InboxRecipientOutcome = "delivered"
	InboxRecipientSkipped   InboxRecipientOutcome = "skipped"
)

// InboxPlannedFile reads the immutable metadata needed for storage admission.
// Provider file identity and fetching remain owned by the integration consumer.
type InboxPlannedFile struct {
	ArtifactID     uuid.UUID                       `json:"artifact_id"`
	ProviderFileID string                          `json:"provider_file_id"`
	Expected       *artifactstore.PreparedArtifact `json:"expected,omitempty"`
}

type inboxRecipientResult struct {
	Outcome          InboxRecipientOutcome
	Agent            AgentRecord
	Input            AgentInputRecord
	SiblingDelivered bool
}

// GetIntegrationInboxOutcomes reads historical execution facts, not live authority.
// An archived recipient with an existing input remains delivered; one without an
// input is skipped. Neither outcome grants permission to deliver any new work.
func (s *Store) GetIntegrationInboxOutcomes(
	ctx context.Context, receipt integrationstore.IntegrationInboxRecord,
) (map[string]InboxRecipientOutcome, error) {
	return integrationInboxOutcomes(ctx, s.q, receipt)
}

func integrationInboxOutcomes(
	ctx context.Context, q *dbsqlc.Queries, receipt integrationstore.IntegrationInboxRecord,
) (map[string]InboxRecipientOutcome, error) {
	outcomes := make(map[string]InboxRecipientOutcome)
	if len(receipt.Plan) == 0 {
		return outcomes, nil
	}
	recipients, err := inboxPlanRecipients(receipt)
	if err != nil {
		return nil, err
	}
	for key, raw := range recipients {
		result, err := resolveInboxRecipientOutcome(ctx, q, receipt, raw)
		if err != nil {
			return nil, err
		}
		outcomes[key] = result.Outcome
	}
	return outcomes, nil
}

// CompleteIntegrationInbox fences completion and checks every frozen recipient in
// the same transaction. A failed upload leaves its pending recipient incomplete.
func (s *Store) CompleteIntegrationInbox(ctx context.Context, lease integrationstore.IntegrationInboxLease) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease)
	if err != nil {
		return err
	}
	receipt := work.Receipt()
	if len(receipt.Plan) == 0 {
		return storeerr.ErrStateTransitionConflict
	}
	outcomes, err := integrationInboxOutcomes(ctx, dbsqlc.New(tx), receipt)
	if err != nil {
		return err
	}
	for _, outcome := range outcomes {
		if outcome == InboxRecipientPending {
			return storeerr.ErrStateTransitionConflict
		}
	}
	if err := work.Complete(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func inboxPlanRecipients(receipt integrationstore.IntegrationInboxRecord) (map[string]json.RawMessage, error) {
	var plan struct {
		Recipients map[string]json.RawMessage `json:"recipients"`
	}
	if json.Unmarshal(receipt.Plan, &plan) != nil || plan.Recipients == nil {
		return nil, storeerr.InvalidRequest(errors.New("invalid frozen inbox plan"))
	}
	return plan.Recipients, nil
}

func inboxPlanRecipient(receipt integrationstore.IntegrationInboxRecord, key string) (json.RawMessage, error) {
	recipients, err := inboxPlanRecipients(receipt)
	if err != nil {
		return nil, err
	}
	if len(recipients[key]) == 0 {
		return nil, storeerr.InvalidRequest(errors.New("recipient is missing from frozen inbox plan"))
	}
	return recipients[key], nil
}

func resolveInboxRecipientOutcome(
	ctx context.Context, q *dbsqlc.Queries, receipt integrationstore.IntegrationInboxRecord, raw json.RawMessage,
) (inboxRecipientResult, error) {
	var envelope struct {
		Launch *InboxLaunchPlan `json:"launch"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return inboxRecipientResult{}, storeerr.ErrInvalidRequest
	}
	if envelope.Launch != nil {
		recipient, err := decodeInboxLaunchRecipient(receipt, raw)
		if err != nil {
			return inboxRecipientResult{}, err
		}
		return resolveInboxLaunchOutcome(ctx, q, receipt.ProjectID, recipient)
	}
	recipient, _, err := decodeInboxInputRecipient(receipt, raw)
	if err != nil {
		return inboxRecipientResult{}, err
	}
	return resolveInboxInputOutcome(ctx, q, recipient)
}

func inboxInputScope(scope integrationdefinition.Scope, integrationID uuid.UUID) (string, error) {
	if scope.Provider() == "" {
		return "", storeerr.InvalidRequest(errors.New("inbox scope requires a provider"))
	}
	return integrationstore.IdempotencyScope(integrationstore.IntegrationRecord{
		ID: integrationID, Provider: scope.Provider(),
	}), nil
}

func inboxInputByKey(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID, scope, key string,
) (AgentInputRecord, bool, error) {
	row, err := q.GetAgentInputByIdempotency(ctx, dbsqlc.GetAgentInputByIdempotencyParams{
		ProjectID: projectID, AgentID: agentID, IdempotencyScope: scope, InputIdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInputRecord{}, false, nil
	}
	if err != nil {
		return AgentInputRecord{}, false, err
	}
	return agentInputRecordFromIdempotencySQLC(row), true, nil
}

func resolveInboxLaunchOutcome(
	ctx context.Context, q *dbsqlc.Queries, projectID uuid.UUID, recipient InboxLaunchRecipient,
) (inboxRecipientResult, error) {
	row, err := q.GetAgentByIdempotencyKey(ctx, dbsqlc.GetAgentByIdempotencyKeyParams{
		ProjectID: projectID, IdempotencyKey: recipient.Launch.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return inboxRecipientResult{}, nil
	}
	if err != nil {
		return inboxRecipientResult{}, err
	}
	if row.ID != recipient.AgentID {
		return inboxRecipientResult{}, storeerr.ErrIdempotencyConflict
	}
	// The planned UUIDv7 can only be admitted by this inbox launch. The agent,
	// initial config activation and initial content commit in one transaction.
	return inboxRecipientResult{Outcome: InboxRecipientDelivered, Agent: agentRecordFromIdempotencySQLC(row)}, nil
}

func resolveInboxInputOutcome(
	ctx context.Context, q *dbsqlc.Queries, recipient InboxInputRecipient,
) (inboxRecipientResult, error) {
	// Read archival before input identity. If archived, any delivery that won the
	// agent lock is already committed and must be observed before deciding skip.
	agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{
		ProjectID: recipient.Input.ProjectID, ID: recipient.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return inboxRecipientResult{}, nil
	}
	if err != nil {
		return inboxRecipientResult{}, err
	}
	input, found, err := inboxInputByKey(
		ctx, q, recipient.Input.ProjectID, recipient.AgentID,
		recipient.Input.IdempotencyScope, recipient.Input.IdempotencyKey,
	)
	if err != nil {
		return inboxRecipientResult{}, err
	}
	result := inboxRecipientResult{}
	if !found && recipient.Sibling != nil {
		sibling, siblingFound, err := inboxInputByKey(
			ctx, q, recipient.Input.ProjectID, recipient.AgentID, recipient.Input.IdempotencyScope, recipient.Sibling.Key,
		)
		if err != nil {
			return inboxRecipientResult{}, err
		}
		result.SiblingDelivered = siblingFound
		// A late attachment callback must admit its own files even when the
		// companion message was delivered. Plain sibling callbacks share delivery.
		if siblingFound && recipient.Sibling.AttachmentNotice == "" {
			input, found = sibling, true
		}
	}
	if found {
		if input.InputKind != "content" || input.IntegrationTargetID == uuid.Nil {
			return inboxRecipientResult{}, storeerr.ErrIdempotencyConflict
		}
		result.Outcome, result.Input = InboxRecipientDelivered, input
	} else if agent.State == string(AgentStateArchived) {
		result.Outcome = InboxRecipientSkipped
	}
	return result, nil
}

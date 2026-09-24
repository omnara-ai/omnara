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

type InboxSlotOutcome string

const (
	InboxSlotPending   InboxSlotOutcome = ""
	InboxSlotDelivered InboxSlotOutcome = "delivered"
	InboxSlotSkipped   InboxSlotOutcome = "skipped"
)

// InboxPlannedFile reads the immutable metadata needed for storage admission.
// Provider file identity and fetching remain owned by the integration consumer.
type InboxPlannedFile struct {
	ArtifactID uuid.UUID                       `json:"artifact_id"`
	Expected   *artifactstore.PreparedArtifact `json:"expected,omitempty"`
}

type inboxSlotResult struct {
	Outcome          InboxSlotOutcome
	Agent            AgentRecord
	Input            AgentInputRecord
	SiblingDelivered bool
}

// GetIntegrationInboxOutcomes reads historical execution facts, not live authority.
// An archived recipient with an existing input remains delivered; one without an
// input is skipped. Neither outcome grants permission to deliver any new work.
func (s *Store) GetIntegrationInboxOutcomes(
	ctx context.Context, receipt integrationstore.IntegrationInboxRecord,
) (map[string]InboxSlotOutcome, error) {
	return integrationInboxOutcomes(ctx, s.q, receipt)
}

func integrationInboxOutcomes(
	ctx context.Context, q *dbsqlc.Queries, receipt integrationstore.IntegrationInboxRecord,
) (map[string]InboxSlotOutcome, error) {
	outcomes := make(map[string]InboxSlotOutcome)
	if len(receipt.Plan) == 0 {
		return outcomes, nil
	}
	slots, err := inboxPlanSlots(receipt)
	if err != nil {
		return nil, err
	}
	for key, raw := range slots {
		result, err := resolveInboxSlotOutcome(ctx, q, receipt, raw)
		if err != nil {
			return nil, err
		}
		outcomes[key] = result.Outcome
	}
	return outcomes, nil
}

// CompleteIntegrationInbox fences completion and checks every frozen recipient in
// the same transaction. A failed upload leaves its pending slot incomplete.
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
		if outcome == InboxSlotPending {
			return storeerr.ErrStateTransitionConflict
		}
	}
	if err := work.Complete(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func inboxPlanSlots(receipt integrationstore.IntegrationInboxRecord) (map[string]json.RawMessage, error) {
	var slots map[string]json.RawMessage
	if json.Unmarshal(receipt.Plan, &slots) != nil || slots == nil {
		return nil, storeerr.InvalidRequest(errors.New("invalid frozen inbox plan"))
	}
	return slots, nil
}

func inboxPlanSlot(receipt integrationstore.IntegrationInboxRecord, key string) (json.RawMessage, error) {
	slots, err := inboxPlanSlots(receipt)
	if err != nil {
		return nil, err
	}
	if len(slots[key]) == 0 {
		return nil, storeerr.InvalidRequest(errors.New("slot is missing from frozen inbox plan"))
	}
	return slots[key], nil
}

func resolveInboxSlotOutcome(
	ctx context.Context, q *dbsqlc.Queries, receipt integrationstore.IntegrationInboxRecord, raw json.RawMessage,
) (inboxSlotResult, error) {
	var envelope struct {
		Launch *InboxLaunchPlan `json:"launch"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return inboxSlotResult{}, storeerr.ErrInvalidRequest
	}
	if envelope.Launch != nil {
		slot, err := decodeInboxLaunchSlot(receipt, raw)
		if err != nil {
			return inboxSlotResult{}, err
		}
		return resolveInboxLaunchOutcome(ctx, q, receipt.ProjectID, slot)
	}
	slot, _, err := decodeInboxInputSlot(receipt, raw)
	if err != nil {
		return inboxSlotResult{}, err
	}
	return resolveInboxInputOutcome(ctx, q, slot)
}

func inboxInputScope(scope integrationdefinition.Scope, integrationID uuid.UUID) (string, error) {
	if scope.Provider() == "" {
		return "", storeerr.InvalidRequest(errors.New("inbox scope requires a provider"))
	}
	return integrationstore.IdempotencyScope(integrationstore.ProjectIntegrationRecord{
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
	ctx context.Context, q *dbsqlc.Queries, projectID uuid.UUID, slot InboxLaunchSlot,
) (inboxSlotResult, error) {
	row, err := q.GetAgentByIdempotencyKey(ctx, dbsqlc.GetAgentByIdempotencyKeyParams{
		ProjectID: projectID, IdempotencyKey: slot.Launch.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return inboxSlotResult{}, nil
	}
	if err != nil {
		return inboxSlotResult{}, err
	}
	if row.ID != slot.AgentID {
		return inboxSlotResult{}, storeerr.ErrIdempotencyConflict
	}
	// The planned UUIDv7 can only be admitted by this inbox launch. The agent,
	// initial config activation and initial content commit in one transaction.
	return inboxSlotResult{Outcome: InboxSlotDelivered, Agent: agentRecordFromIdempotencySQLC(row)}, nil
}

func resolveInboxInputOutcome(ctx context.Context, q *dbsqlc.Queries, slot InboxInputSlot) (inboxSlotResult, error) {
	// Read archival before input identity. If archived, any delivery that won the
	// agent lock is already committed and must be observed before deciding skip.
	agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{
		ProjectID: slot.Input.ProjectID, ID: slot.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return inboxSlotResult{}, nil
	}
	if err != nil {
		return inboxSlotResult{}, err
	}
	input, found, err := inboxInputByKey(
		ctx, q, slot.Input.ProjectID, slot.AgentID, slot.Input.IdempotencyScope, slot.Input.IdempotencyKey,
	)
	if err != nil {
		return inboxSlotResult{}, err
	}
	result := inboxSlotResult{}
	if !found && slot.Sibling != nil {
		sibling, siblingFound, err := inboxInputByKey(
			ctx, q, slot.Input.ProjectID, slot.AgentID, slot.Input.IdempotencyScope, slot.Sibling.Key,
		)
		if err != nil {
			return inboxSlotResult{}, err
		}
		result.SiblingDelivered = siblingFound
		// A late attachment callback must admit its own files even when the
		// companion message was delivered. Plain sibling callbacks share delivery.
		if siblingFound && slot.Sibling.AttachmentNotice == "" {
			input, found = sibling, true
		}
	}
	if found {
		if input.InputKind != "content" || input.IntegrationTargetID == uuid.Nil {
			return inboxSlotResult{}, storeerr.ErrIdempotencyConflict
		}
		result.Outcome, result.Input = InboxSlotDelivered, input
	} else if agent.State == string(AgentStateArchived) {
		result.Outcome = InboxSlotSkipped
	}
	return result, nil
}

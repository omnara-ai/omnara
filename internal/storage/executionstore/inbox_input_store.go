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
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type InboxMessageSibling struct {
	Key              string `json:"key"`
	AttachmentNotice string `json:"attachment_notice,omitempty"`
}

type InboxInputSlot struct {
	Scope        integrationdefinition.Scope  `json:"scope"`
	Files        []InboxPlannedFile           `json:"files,omitempty"`
	Sibling      *InboxMessageSibling         `json:"sibling,omitempty"`
	AgentID      uuid.UUID                    `json:"agent_id"`
	Input        CreateAgentContentInputInput `json:"input"`
	ArtifactIDs  []uuid.UUID                  `json:"artifact_ids,omitempty"`
	Subscription *InboxSubscriptionAuthority  `json:"subscription,omitempty"`
}

type InboxSubscriptionAuthority struct {
	Alternatives []integrationstore.ConversationAddress `json:"alternatives"`
}

type InboxInputSkipReason string

const InboxInputSkipAgentArchived InboxInputSkipReason = "agent_archived"

func (s *Store) AdmitInboxInputSlot(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	slotKey string,
	artifacts []artifactstore.PreparedArtifact,
) (InboxInputResult, error) {
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil || slotKey == "" {
		return InboxInputResult{}, storeerr.InvalidRequest(errors.New("inbox lease and slot are required"))
	}
	return storeutil.RetryTransaction(ctx, "admit_inbox_input_slot", func() (InboxInputResult, error) {
		return s.admitInboxInputSlotOnce(ctx, lease, slotKey, artifacts)
	})
}

func (s *Store) admitInboxInputSlotOnce(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	slotKey string,
	artifacts []artifactstore.PreparedArtifact,
) (InboxInputResult, error) {
	snapshot, err := s.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return InboxInputResult{}, err
	}
	raw, err := inboxPlanSlot(snapshot, slotKey)
	if err != nil {
		return InboxInputResult{}, err
	}
	slot, blocks, err := decodeInboxInputSlot(snapshot, raw)
	if err != nil {
		return InboxInputResult{}, err
	}
	if outcome, err := resolveInboxInputOutcome(
		ctx, s.q, slot,
	); err != nil || outcome.Outcome != InboxSlotPending {
		if err != nil {
			return InboxInputResult{}, err
		}
		return replayInboxInput(ctx, s.q, slot, outcome)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return InboxInputResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	work, err := s.integrations.LockIntegrationInboxLeaseTx(ctx, tx, lease)
	if err != nil {
		_ = tx.Rollback(ctx)
		if outcome, readErr := resolveInboxInputOutcome(
			ctx, s.q, slot,
		); readErr == nil && outcome.Outcome != InboxSlotPending {
			return replayInboxInput(ctx, s.q, slot, outcome)
		}
		return InboxInputResult{}, err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return InboxInputResult{}, storeerr.ErrIdempotencyConflict
	}
	q := dbsqlc.New(tx)
	if err := integrationstore.LockConversationTx(
		ctx,
		tx,
		lease.ProjectID,
		slot.Input.Origin.IntegrationID,
		slot.Input.Origin.Address,
	); err != nil {
		return InboxInputResult{}, err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: lease.ProjectID, AgentID: slot.AgentID,
	}}); err != nil {
		return InboxInputResult{}, err
	}
	outcome, err := resolveInboxInputOutcome(ctx, q, slot)
	if err != nil {
		return InboxInputResult{}, err
	}
	if outcome.Outcome != InboxSlotPending {
		return replayInboxInput(ctx, q, slot, outcome)
	}
	artifacts, err = validateInboxPreparedArtifacts(slot.ArtifactIDs, slot.Files, blocks, artifacts)
	if err != nil {
		return InboxInputResult{}, err
	}
	if outcome.SiblingDelivered {
		supplemental := []CreateContentBlockInput{{
			BlockKind: ContentBlockKindText, TextContent: slot.Sibling.AttachmentNotice,
			Metadata: map[string]string{"omnara_hidden": "true"},
		}}
		for _, block := range blocks {
			if block.ArtifactID != uuid.Nil {
				block.Ordinal = int32(len(supplemental))
				supplemental = append(supplemental, block)
			}
		}
		blocks = supplemental
		slot.Input.ContentBlocks, err = marshalAgentInputContentBlocks(blocks)
		if err != nil {
			return InboxInputResult{}, err
		}
		slot.Input.CancelOpenInteractions = false
	}
	if slot.Subscription != nil {
		if err := validateInboxSubscriptionTx(ctx, tx, slot); err != nil {
			return InboxInputResult{}, err
		}
	}
	notifications := s.newTxNotifications()
	result, err := s.admitOriginContentTx(
		ctx,
		tx,
		notifications,
		slot.Input,
		blocks,
		artifacts,
	)
	if err != nil {
		return InboxInputResult{}, err
	}
	if err := work.CheckLease(ctx); err != nil {
		return InboxInputResult{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "admit inbox input slot"); err != nil {
		return InboxInputResult{}, err
	}
	return result, nil
}

func decodeInboxInputSlot(
	receipt integrationstore.IntegrationInboxRecord,
	raw json.RawMessage,
) (InboxInputSlot, []CreateContentBlockInput, error) {
	var slot InboxInputSlot
	fail := func() (InboxInputSlot, []CreateContentBlockInput, error) {
		return slot, nil, storeerr.InvalidRequest(errors.New("invalid frozen existing-agent input slot"))
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope["selection"] != nil {
		return fail()
	}
	if json.Unmarshal(raw, &slot) != nil || slot.AgentID == uuid.Nil {
		return fail()
	}
	if (slot.Input.ProjectID != uuid.Nil && slot.Input.ProjectID != receipt.ProjectID) ||
		(slot.Input.AgentID != uuid.Nil && slot.Input.AgentID != slot.AgentID) || slot.Input.Origin == nil ||
		slot.Input.Origin.IntegrationID != receipt.IntegrationID || slot.Input.Actor == nil {
		return fail()
	}
	if slot.Sibling != nil &&
		(slot.Sibling.Key == "" || slot.Sibling.Key == slot.Input.IdempotencyKey ||
			len(slot.Sibling.Key) > 512 || len(slot.Sibling.AttachmentNotice) > 16384) {
		return fail()
	}
	slot.Input.ProjectID, slot.Input.AgentID = receipt.ProjectID, slot.AgentID
	var blocks []CreateContentBlockInput
	var err error
	slot.Input, blocks, err = prepareOriginContentInput(slot.Input)
	if err != nil {
		return slot, nil, err
	}
	slot.Input.IdempotencyScope, err = inboxInputScope(slot.Scope, receipt.IntegrationID)
	if err != nil {
		return slot, nil, err
	}
	return slot, blocks, nil
}

func replayInboxInput(
	ctx context.Context, q *dbsqlc.Queries, slot InboxInputSlot, outcome inboxSlotResult,
) (InboxInputResult, error) {
	if outcome.Outcome == InboxSlotSkipped {
		return InboxInputResult{Skipped: InboxInputSkipAgentArchived}, nil
	}
	content, err := agentInputContentBlocks(ctx, q, slot.Input.ProjectID, slot.AgentID, []uuid.UUID{outcome.Input.ID})
	return InboxInputResult{AgentInput: outcome.Input, ContentBlocks: content[outcome.Input.ID]}, err
}

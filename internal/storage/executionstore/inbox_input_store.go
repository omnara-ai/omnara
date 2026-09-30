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

type InboxInputRecipient struct {
	Scope        integrationdefinition.Scope  `json:"-"`
	Files        []InboxPlannedFile           `json:"-"`
	Sibling      *InboxMessageSibling         `json:"-"`
	AgentID      uuid.UUID                    `json:"agent_id"`
	Input        CreateAgentContentInputInput `json:"-"`
	ArtifactIDs  []uuid.UUID                  `json:"artifact_ids,omitempty"`
	Subscription *InboxSubscriptionAuthority  `json:"subscription,omitempty"`
}

type InboxSubscriptionAuthority struct {
	Alternatives []integrationstore.ConversationAddress `json:"alternatives"`
}

type InboxInputSkipReason string

const InboxInputSkipAgentArchived InboxInputSkipReason = "agent_archived"

func (s *Store) AdmitInboxInputRecipient(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	recipientKey string,
	artifacts []artifactstore.PreparedArtifact,
) (InboxInputResult, error) {
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil || recipientKey == "" {
		return InboxInputResult{}, storeerr.InvalidRequest(errors.New("inbox lease and recipient are required"))
	}
	return storeutil.RetryTransaction(ctx, "admit_inbox_input_recipient", func() (InboxInputResult, error) {
		return s.admitInboxInputRecipientOnce(ctx, lease, recipientKey, artifacts)
	})
}

func (s *Store) admitInboxInputRecipientOnce(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	recipientKey string,
	artifacts []artifactstore.PreparedArtifact,
) (InboxInputResult, error) {
	snapshot, err := s.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return InboxInputResult{}, err
	}
	raw, err := inboxPlanRecipient(snapshot, recipientKey)
	if err != nil {
		return InboxInputResult{}, err
	}
	recipient, blocks, err := decodeInboxInputRecipient(snapshot, raw)
	if err != nil {
		return InboxInputResult{}, err
	}
	if outcome, err := resolveInboxInputOutcome(
		ctx, s.q, recipient,
	); err != nil || outcome.Outcome != InboxRecipientPending {
		if err != nil {
			return InboxInputResult{}, err
		}
		return replayInboxInput(ctx, s.q, recipient, outcome)
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
			ctx, s.q, recipient,
		); readErr == nil && outcome.Outcome != InboxRecipientPending {
			return replayInboxInput(ctx, s.q, recipient, outcome)
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
		recipient.Input.Origin.IntegrationID,
		recipient.Input.Origin.Address,
	); err != nil {
		return InboxInputResult{}, err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: lease.ProjectID, AgentID: recipient.AgentID,
	}}); err != nil {
		return InboxInputResult{}, err
	}
	outcome, err := resolveInboxInputOutcome(ctx, q, recipient)
	if err != nil {
		return InboxInputResult{}, err
	}
	if outcome.Outcome != InboxRecipientPending {
		return replayInboxInput(ctx, q, recipient, outcome)
	}
	artifacts, err = validateInboxPreparedArtifacts(recipient.ArtifactIDs, recipient.Files, blocks, artifacts)
	if err != nil {
		return InboxInputResult{}, err
	}
	if outcome.SiblingDelivered {
		supplemental := []CreateContentBlockInput{{
			BlockKind: ContentBlockKindText, TextContent: recipient.Sibling.AttachmentNotice,
			Metadata: map[string]string{"omnara_hidden": "true"},
		}}
		for _, block := range blocks {
			if block.ArtifactID != uuid.Nil {
				block.Ordinal = int32(len(supplemental))
				supplemental = append(supplemental, block)
			}
		}
		blocks = supplemental
		recipient.Input.ContentBlocks, err = marshalAgentInputContentBlocks(blocks)
		if err != nil {
			return InboxInputResult{}, err
		}
		recipient.Input.CancelOpenInteractions = false
	}
	if recipient.Subscription != nil {
		if err := validateInboxSubscriptionTx(ctx, tx, recipient); err != nil {
			return InboxInputResult{}, err
		}
	}
	notifications := s.newTxNotifications()
	result, err := s.admitOriginContentTx(
		ctx,
		tx,
		notifications,
		recipient.Input,
		blocks,
		artifacts,
	)
	if err != nil {
		return InboxInputResult{}, err
	}
	if err := work.CheckLease(ctx); err != nil {
		return InboxInputResult{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "admit inbox input recipient"); err != nil {
		return InboxInputResult{}, err
	}
	return result, nil
}

func decodeInboxInputRecipient(
	receipt integrationstore.IntegrationInboxRecord,
	raw json.RawMessage,
) (InboxInputRecipient, []CreateContentBlockInput, error) {
	var recipient InboxInputRecipient
	fail := func() (InboxInputRecipient, []CreateContentBlockInput, error) {
		return recipient, nil, storeerr.InvalidRequest(errors.New("invalid frozen existing-agent input recipient"))
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope["launch_claim"] != nil {
		return fail()
	}
	if json.Unmarshal(raw, &recipient) != nil || recipient.AgentID == uuid.Nil {
		return fail()
	}
	message, err := inboxMessage(receipt)
	if err != nil {
		return recipient, nil, err
	}
	content, files, err := message.RecipientContent(recipient.ArtifactIDs)
	if err != nil {
		return recipient, nil, err
	}
	recipient.Scope, recipient.Sibling, recipient.Files = message.Scope, message.Sibling, files
	recipient.Input = message.input(receipt.ProjectID, recipient.AgentID, content)
	if recipient.Sibling != nil &&
		(recipient.Sibling.Key == "" || recipient.Sibling.Key == recipient.Input.IdempotencyKey ||
			len(recipient.Sibling.Key) > 512 || len(recipient.Sibling.AttachmentNotice) > 16384) {
		return fail()
	}
	var blocks []CreateContentBlockInput
	recipient.Input, blocks, err = prepareOriginContentInput(recipient.Input)
	if err != nil {
		return recipient, nil, err
	}
	recipient.Input.IdempotencyScope, err = inboxInputScope(recipient.Scope, receipt.IntegrationID)
	if err != nil {
		return recipient, nil, err
	}
	return recipient, blocks, nil
}

func replayInboxInput(
	ctx context.Context, q *dbsqlc.Queries, recipient InboxInputRecipient, outcome inboxRecipientResult,
) (InboxInputResult, error) {
	if outcome.Outcome == InboxRecipientSkipped {
		return InboxInputResult{Skipped: InboxInputSkipAgentArchived}, nil
	}
	content, err := agentInputContentBlocks(
		ctx, q, recipient.Input.ProjectID, recipient.AgentID, []uuid.UUID{outcome.Input.ID},
	)
	return InboxInputResult{AgentInput: outcome.Input, ContentBlocks: content[outcome.Input.ID]}, err
}

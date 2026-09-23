package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
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
	Sibling      *InboxMessageSibling         `json:"sibling,omitempty"`
	AgentID      uuid.UUID                    `json:"agent_id"`
	Input        CreateAgentContentInputInput `json:"input"`
	ArtifactIDs  []uuid.UUID                  `json:"artifact_ids,omitempty"`
	Subscription *InboxSubscriptionAuthority  `json:"subscription,omitempty"`
}

type InboxSubscriptionAuthority struct {
	Event        string                       `json:"event"`
	Alternatives []InboxSubscriptionReference `json:"alternatives"`
}

type InboxSubscriptionReference struct {
	Type    string                       `json:"type"`
	Address appstore.ConversationAddress `json:"address"`
}

type InboxInputPreparation struct {
	Artifacts []artifactstore.PreparedArtifact `json:"artifacts"`
}

type InboxInputSkipReason string

const InboxInputSkipAgentArchived InboxInputSkipReason = "agent_archived"

type inboxInputCommit struct {
	Skipped          InboxInputSkipReason `json:"skipped,omitempty"`
	IdempotencyKey   string               `json:"idempotency_key"`
	InputID          uuid.UUID            `json:"input_id"`
	TargetID         uuid.UUID            `json:"target_id"`
	IdempotencyScope string               `json:"idempotency_scope"`
}

type inboxInputProgress struct {
	Prepared  *InboxInputPreparation `json:"prepared"`
	Committed *inboxInputCommit      `json:"committed"`
}

func (s *Store) AdmitInboxInputSlot(
	ctx context.Context,
	lease appstore.AppInboxLease,
	slotKey string,
) (InboxInputResult, error) {
	if lease.ProjectID == uuid.Nil || lease.ReceiptID == uuid.Nil || lease.Token == uuid.Nil || slotKey == "" {
		return InboxInputResult{}, storeerr.InvalidRequest(errors.New("inbox lease and slot are required"))
	}
	return storeutil.RetryTransaction(ctx, "admit_inbox_input_slot", func() (InboxInputResult, error) {
		return s.admitInboxInputSlotOnce(ctx, lease, slotKey)
	})
}

func (s *Store) admitInboxInputSlotOnce(
	ctx context.Context,
	lease appstore.AppInboxLease,
	slotKey string,
) (InboxInputResult, error) {
	snapshot, err := s.apps.GetAppInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return InboxInputResult{}, err
	}
	slot, progress, _, err := decodeInboxInputSlot(snapshot, slotKey)
	if err != nil {
		return InboxInputResult{}, err
	}
	if progress.Committed != nil {
		return s.replayInboxInput(ctx, slot, *progress.Committed)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return InboxInputResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	work, err := s.apps.LockAppInboxLeaseTx(ctx, tx, lease)
	if err != nil {
		_ = tx.Rollback(ctx)
		latest, readErr := s.apps.GetAppInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if readErr == nil {
			latestSlot, latestProgress, _, decodeErr := decodeInboxInputSlot(latest, slotKey)
			if decodeErr == nil && latestProgress.Committed != nil {
				return s.replayInboxInput(ctx, latestSlot, *latestProgress.Committed)
			}
		}
		return InboxInputResult{}, err
	}
	locked := work.Receipt()
	if !sameJSON(snapshot.Plan, locked.Plan) {
		return InboxInputResult{}, storeerr.ErrIdempotencyConflict
	}
	slot, progress, blocks, err := decodeInboxInputSlot(locked, slotKey)
	if err != nil {
		return InboxInputResult{}, err
	}
	if progress.Committed != nil {
		_ = tx.Rollback(ctx)
		return s.replayInboxInput(ctx, slot, *progress.Committed)
	}
	slot.Input, _, err = s.resolveInputOriginTx(ctx, tx, slot.Input)
	if err != nil {
		return InboxInputResult{}, err
	}
	if err := appstore.LockConversationTx(
		ctx,
		tx,
		lease.ProjectID,
		slot.Input.Origin.AppID,
		slot.Input.Origin.Address,
	); err != nil {
		return InboxInputResult{}, err
	}
	settled, err := s.settleArchivedInboxInputTx(ctx, tx, work, slotKey, slot)
	if err != nil {
		return InboxInputResult{}, err
	}
	if settled != nil {
		if err := tx.Commit(ctx); err != nil {
			return InboxInputResult{}, err
		}
		return *settled, nil
	}
	var artifacts []artifactstore.PreparedArtifact
	if progress.Prepared != nil {
		artifacts = progress.Prepared.Artifacts
	}
	artifacts, err = validateInboxPreparedArtifacts(slot.ArtifactIDs, blocks, artifacts)
	if err != nil {
		return InboxInputResult{}, err
	}
	notifications := s.newTxNotifications()
	if slot.Sibling != nil {
		_, own, err := loadAgentInputByIdempotencyMaybeTx(
			ctx,
			tx,
			slot.Input.ProjectID,
			slot.AgentID,
			slot.Input.IdempotencyScope,
			slot.Input.IdempotencyKey,
		)
		if err != nil {
			return InboxInputResult{}, err
		}
		if !own {
			companion := slot.Input
			companion.IdempotencyKey = slot.Sibling.Key
			prior, found, err := s.originContentReplayTx(
				ctx,
				tx,
				companion,
			)
			if err != nil {
				return InboxInputResult{}, err
			}
			if found && slot.Sibling.AttachmentNotice == "" {
				committed, err := json.Marshal(
					inboxInputCommit{
						InputID:          prior.AgentInput.ID,
						TargetID:         prior.AgentInput.AppTargetID,
						IdempotencyScope: prior.AgentInput.IdempotencyScope,
						IdempotencyKey:   companion.IdempotencyKey,
					},
				)
				if err != nil {
					return InboxInputResult{}, err
				}
				if err := work.CommitSlot(ctx, slotKey, committed); err != nil {
					return InboxInputResult{}, err
				}
				if err := s.commitTxWithNotifications(
					ctx,
					tx,
					notifications,
					"admit inbox message sibling",
				); err != nil {
					return InboxInputResult{}, err
				}
				return prior, nil
			}
			if found {
				supplemental := []CreateContentBlockInput{
					{
						BlockKind:   ContentBlockKindText,
						TextContent: slot.Sibling.AttachmentNotice,
						Metadata:    map[string]string{"omnara_hidden": "true"},
					},
				}
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
		}
	}

	if slot.Subscription != nil {
		_, replay, err := loadAgentInputByIdempotencyMaybeTx(
			ctx,
			tx,
			slot.Input.ProjectID,
			slot.AgentID,
			slot.Input.IdempotencyScope,
			slot.Input.IdempotencyKey,
		)
		if err != nil {
			return InboxInputResult{}, err
		}
		if !replay {
			if err := validateInboxSubscriptionTx(ctx, tx, slot); err != nil {
				return InboxInputResult{}, err
			}
		}
	}
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
	committed, err := json.Marshal(
		inboxInputCommit{
			IdempotencyKey:   slot.Input.IdempotencyKey,
			InputID:          result.AgentInput.ID,
			TargetID:         result.AgentInput.AppTargetID,
			IdempotencyScope: result.AgentInput.IdempotencyScope,
		},
	)
	if err != nil {
		return InboxInputResult{}, err
	}
	if err := work.CommitSlot(ctx, slotKey, committed); err != nil {
		return InboxInputResult{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "admit inbox input slot"); err != nil {
		return InboxInputResult{}, err
	}
	return result, nil
}

// settleArchivedInboxInputTx runs after locking the inbox lease and conversation.
// Holding the agent lock makes archival terminal for this slot, but a delivery
// that won the race still replays as delivered. Missing subscriptions are not
// terminal: restoring the same address/type/event can authorize a later retry.
// A non-nil result has been recorded in progress; the caller must commit tx.
func (s *Store) settleArchivedInboxInputTx(
	ctx context.Context,
	tx pgx.Tx,
	work *appstore.AppInboxLeaseTx,
	key string,
	slot InboxInputSlot,
) (*InboxInputResult, error) {
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: slot.Input.ProjectID, AgentID: slot.AgentID,
	}}); err != nil {
		return nil, err
	}
	agent, err := loadAgentInProjectTx(ctx, tx, slot.Input.ProjectID, slot.AgentID)
	if err != nil || agent.State != AgentStateArchived {
		return nil, err
	}
	slot.Input, _, err = s.resolveInputOriginTx(ctx, tx, slot.Input)
	if err != nil {
		return nil, err
	}
	result, found, err := s.originContentReplayTx(ctx, tx, slot.Input)
	if err != nil {
		return nil, err
	}
	if !found && slot.Sibling != nil && slot.Sibling.AttachmentNotice == "" {
		companion := slot.Input
		companion.IdempotencyKey = slot.Sibling.Key
		result, found, err = s.originContentReplayTx(ctx, tx, companion)
		if err != nil {
			return nil, err
		}
	}
	var committed []byte
	if found {
		committed, err = json.Marshal(inboxInputCommit{
			IdempotencyKey: result.AgentInput.InputIdempotencyKey,
			InputID:        result.AgentInput.ID, TargetID: result.AgentInput.AppTargetID,
			IdempotencyScope: result.AgentInput.IdempotencyScope,
		})
	} else {
		result = InboxInputResult{Skipped: InboxInputSkipAgentArchived}
		committed, err = json.Marshal(struct {
			Skipped InboxInputSkipReason `json:"skipped"`
		}{Skipped: result.Skipped})
	}
	if err != nil {
		return nil, err
	}
	if err := work.CommitSlot(ctx, key, committed); err != nil {
		return nil, err
	}
	return &result, nil
}

func decodeInboxInputSlot(
	receipt appstore.AppInboxRecord,
	key string,
) (InboxInputSlot, inboxInputProgress, []CreateContentBlockInput, error) {
	var slot InboxInputSlot
	var progress inboxInputProgress
	fail := func() (InboxInputSlot, inboxInputProgress, []CreateContentBlockInput, error) {
		return slot, progress, nil, storeerr.InvalidRequest(errors.New("invalid frozen existing-agent input slot"))
	}
	var slots map[string]json.RawMessage
	if json.Unmarshal(receipt.Plan, &slots) != nil || len(slots[key]) == 0 {
		return fail()
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(slots[key], &envelope) != nil || envelope["selection"] != nil {
		return fail()
	}
	if json.Unmarshal(slots[key], &slot) != nil || slot.AgentID == uuid.Nil {
		return fail()
	}
	if (slot.Input.ProjectID != uuid.Nil && slot.Input.ProjectID != receipt.ProjectID) ||
		(slot.Input.AgentID != uuid.Nil && slot.Input.AgentID != slot.AgentID) || slot.Input.Origin == nil ||
		slot.Input.Origin.AppID != receipt.AppID || slot.Input.Actor == nil {
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
		return slot, progress, nil, err
	}
	var stages map[string]json.RawMessage
	if json.Unmarshal(receipt.Progress, &stages) != nil {
		return fail()
	}
	if raw := stages[key]; len(raw) != 0 && json.Unmarshal(raw, &progress) != nil {
		return fail()
	}
	return slot, progress, blocks, nil
}

func (s *Store) replayInboxInput(
	ctx context.Context,
	slot InboxInputSlot,
	committed inboxInputCommit,
) (InboxInputResult, error) {
	if committed.Skipped != "" {
		if committed.Skipped != InboxInputSkipAgentArchived || committed.InputID != uuid.Nil ||
			committed.TargetID != uuid.Nil || committed.IdempotencyKey != "" || committed.IdempotencyScope != "" {
			return InboxInputResult{}, storeerr.ErrIdempotencyConflict
		}
		return InboxInputResult{Skipped: committed.Skipped}, nil
	}
	if committed.InputID == uuid.Nil || committed.TargetID == uuid.Nil || committed.IdempotencyScope == "" {
		return InboxInputResult{}, storeerr.ErrIdempotencyConflict
	}
	key := committed.IdempotencyKey
	if key == "" {
		key = slot.Input.IdempotencyKey
	}
	row, err := s.q.GetAgentInputByIdempotency(ctx, dbsqlc.GetAgentInputByIdempotencyParams{
		ProjectID:           slot.Input.ProjectID,
		AgentID:             slot.AgentID,
		IdempotencyScope:    committed.IdempotencyScope,
		InputIdempotencyKey: key,
	})
	if err != nil {
		return InboxInputResult{}, err
	}
	record := agentInputRecordFromIdempotencySQLC(row)
	if record.ID != committed.InputID || record.AppTargetID != committed.TargetID {
		return InboxInputResult{}, storeerr.ErrIdempotencyConflict
	}
	content, err := agentInputContentBlocks(ctx, s.q, slot.Input.ProjectID, slot.AgentID, []uuid.UUID{record.ID})
	return InboxInputResult{AgentInput: record, ContentBlocks: content[record.ID]}, err
}

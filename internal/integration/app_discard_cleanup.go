package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type DiscardedAppInboxReader interface {
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
}

type DiscardedAppArtifactCleaner interface {
	DeleteUnreferencedPreparedArtifact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

// CleanupDiscardedAppInboxArtifacts runs only AFTER successful discard commit.
// It re-reads terminal state and takes candidate IDs from the frozen plan, not
// preparation progress: an upload may have finished before Prepare was recorded.
// Committed slot keys are always preserved. Failed/partial receipts retain their
// blobs until explicit discard. Missing objects are harmless and errors can be retried
// while the receipt is retained. The caller must report errors as warnings after
// successful discard, never as a rollback of that discard. No receipt is mutated.
// The pass is bounded to 30 seconds; it cannot guarantee reclamation after a
// crash, missing blob configuration, later stale upload, or receipt retention.
func CleanupDiscardedAppInboxArtifacts(
	ctx context.Context,
	inbox DiscardedAppInboxReader,
	artifacts DiscardedAppArtifactCleaner,
	projectID, receiptID uuid.UUID,
) error {
	if projectID == uuid.Nil || receiptID == uuid.Nil || inbox == nil || artifacts == nil {
		return storeerr.InvalidRequest(errors.New("project, receipt, inbox reader and artifact cleaner are required"))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	receipt, err := inbox.GetIntegrationInbox(ctx, projectID, receiptID)
	if err != nil {
		return err
	}
	if receipt.ID != receiptID || receipt.ProjectID != projectID ||
		receipt.State != integrationstore.IntegrationInboxDiscarded {
		return storeerr.ErrStateTransitionConflict
	}
	var progress map[string]map[string]json.RawMessage
	if err := json.Unmarshal(receipt.Progress, &progress); err != nil || progress == nil {
		return errors.New("discarded receipt has invalid progress")
	}
	if len(receipt.Plan) == 0 {
		return nil // No frozen plan means no prepared uploads.
	}
	plan, err := decodeAppInboxPlan(receipt.Plan)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(plan))
	protected := make(map[[2]uuid.UUID]bool)
	for key, slot := range plan {
		_, committed := progress[key]["committed"]
		for _, id := range slot.ArtifactIDs {
			if id == uuid.Nil {
				return fmt.Errorf("discarded slot %s has an invalid artifact ID", key)
			}
			if committed {
				protected[[2]uuid.UUID{slot.AgentID, id}] = true
			}
		}
		if !committed {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var failures []error
	for _, key := range keys {
		slot := plan[key]
		for _, id := range slot.ArtifactIDs {
			if protected[[2]uuid.UUID{slot.AgentID, id}] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if err := artifacts.DeleteUnreferencedPreparedArtifact(ctx, projectID, slot.AgentID, id); err != nil {
				failures = append(failures, fmt.Errorf("clean discarded slot %s: %w", key, err))
			}
		}
	}
	return errors.Join(failures...)
}

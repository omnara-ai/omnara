package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AppInboxCleanupReader interface {
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
}

type AppInboxArtifactCleaner interface {
	DeleteUnreferencedPreparedArtifact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

// appInboxSlotProgress reads only the preparation and settlement facts needed by
// the consumer. A committed skip settles a slot without delivering an input.
type appInboxSlotProgress struct {
	Prepared  json.RawMessage `json:"prepared"`
	Committed *struct {
		Skipped executionstore.InboxInputSkipReason `json:"skipped"`
	} `json:"committed"`
}

func (p appInboxSlotProgress) skipped() bool {
	return p.Committed != nil && p.Committed.Skipped == executionstore.InboxInputSkipAgentArchived
}

func (p appInboxSlotProgress) delivered() bool {
	// Treat unknown commit outcomes as delivered so cleanup fails closed.
	return p.Committed != nil && !p.skipped()
}

func appInboxPlanHasArtifacts(plan AppInboxPlan) bool {
	for _, slot := range plan {
		if len(slot.ArtifactIDs) != 0 {
			return true
		}
	}
	return false
}

func CleanupTerminalAppInboxArtifacts(
	ctx context.Context,
	inbox AppInboxCleanupReader,
	artifacts AppInboxArtifactCleaner,
	projectID, receiptID uuid.UUID,
) error {
	// Cleanup uses IDs from the frozen plan because uploads can finish before Prepare is recorded.
	if projectID == uuid.Nil || receiptID == uuid.Nil || inbox == nil || artifacts == nil {
		return storeerr.InvalidRequest(errors.New("project, receipt, inbox reader and artifact cleaner are required"))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	receipt, err := inbox.GetIntegrationInbox(ctx, projectID, receiptID)
	if err != nil {
		return err
	}
	if receipt.ID != receiptID || receipt.ProjectID != projectID {
		return storeerr.ErrStateTransitionConflict
	}
	if receipt.State != integrationstore.IntegrationInboxFailed &&
		receipt.State != integrationstore.IntegrationInboxCompleted {
		return nil
	}
	var progress map[string]appInboxSlotProgress
	if err := json.Unmarshal(receipt.Progress, &progress); err != nil || progress == nil {
		return errors.New("terminal receipt has invalid progress")
	}
	if len(receipt.Plan) == 0 {
		return nil
	}
	plan, err := decodeAppInboxPlan(receipt.Plan)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(plan))
	for key, slot := range plan {
		outcome := progress[key]
		if receipt.State == integrationstore.IntegrationInboxCompleted && outcome.Committed == nil {
			return storeerr.ErrStateTransitionConflict
		}
		for _, id := range slot.ArtifactIDs {
			if id == uuid.Nil {
				return fmt.Errorf("terminal slot %s has an invalid artifact ID", key)
			}
		}
		if !outcome.delivered() {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var failures []error
	for _, key := range keys {
		slot := plan[key]
		for _, id := range slot.ArtifactIDs {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if err := artifacts.DeleteUnreferencedPreparedArtifact(ctx, projectID, slot.AgentID, id); err != nil {
				failures = append(failures, fmt.Errorf("clean terminal slot %s: %w", key, err))
			}
		}
	}
	return errors.Join(failures...)
}

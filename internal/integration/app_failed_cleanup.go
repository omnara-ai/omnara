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

type FailedAppInboxReader interface {
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
}

type FailedAppArtifactCleaner interface {
	DeleteUnreferencedPreparedArtifact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

func CleanupFailedAppInboxArtifacts(
	ctx context.Context,
	inbox FailedAppInboxReader,
	artifacts FailedAppArtifactCleaner,
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
	if receipt.ID != receiptID || receipt.ProjectID != projectID ||
		receipt.State != integrationstore.IntegrationInboxFailed {
		return storeerr.ErrStateTransitionConflict
	}
	var progress map[string]map[string]json.RawMessage
	if err := json.Unmarshal(receipt.Progress, &progress); err != nil || progress == nil {
		return errors.New("failed receipt has invalid progress")
	}
	if len(receipt.Plan) == 0 {
		return nil
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
				return fmt.Errorf("failed slot %s has an invalid artifact ID", key)
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
				failures = append(failures, fmt.Errorf("clean failed slot %s: %w", key, err))
			}
		}
	}
	return errors.Join(failures...)
}

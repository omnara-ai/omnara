package integration

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type IntegrationInboxCleanupReader interface {
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
}

type IntegrationInboxArtifactCleaner interface {
	DeleteUnreferencedPreparedArtifact(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

func integrationInboxPlanHasArtifacts(plan IntegrationInboxPlan) bool {
	for _, recipient := range plan.Recipients {
		if len(recipient.ArtifactIDs) != 0 {
			return true
		}
	}
	return false
}

func CleanupTerminalIntegrationInboxArtifacts(
	ctx context.Context,
	inbox IntegrationInboxCleanupReader,
	artifacts IntegrationInboxArtifactCleaner,
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
	if receipt.ID != receiptID || receipt.ProjectID != projectID {
		return storeerr.ErrStateTransitionConflict
	}
	if receipt.State != integrationstore.IntegrationInboxFailed &&
		receipt.State != integrationstore.IntegrationInboxCompleted {
		return nil
	}
	if len(receipt.Plan) == 0 {
		return nil
	}
	plan, err := decodeIntegrationInboxPlan(receipt.Plan)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(plan.Recipients))
	for key, recipient := range plan.Recipients {
		for _, id := range recipient.ArtifactIDs {
			if id == uuid.Nil {
				return fmt.Errorf("terminal recipient %s has an invalid artifact ID", key)
			}
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var failures []error
	for _, key := range keys {
		recipient := plan.Recipients[key]
		for _, id := range recipient.ArtifactIDs {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if err := artifacts.DeleteUnreferencedPreparedArtifact(ctx, projectID, recipient.AgentID, id); err != nil {
				failures = append(failures, fmt.Errorf("clean terminal recipient %s: %w", key, err))
			}
		}
	}
	return errors.Join(failures...)
}

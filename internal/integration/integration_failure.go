package integration

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (c *IntegrationInboxConsumer) FinalizeFailure(ctx context.Context, projectID, receiptID uuid.UUID) error {
	receipt, err := c.inbox.GetIntegrationInbox(ctx, projectID, receiptID)
	if err != nil {
		return err
	}
	if receipt.State != integrationstore.IntegrationInboxFailed {
		return storeerr.ErrStateTransitionConflict
	}
	message := inboxFailureMessage
	unfinished := true
	var plan IntegrationInboxPlan
	if len(receipt.Plan) != 0 {
		plan, err = decodeIntegrationInboxPlan(receipt.Plan)
		if err != nil {
			return err
		}
		var progress map[string]integrationInboxSlotProgress
		if err := json.Unmarshal(receipt.Progress, &progress); err != nil {
			return err
		}
		unfinished = false
		for key := range plan {
			if progress[key].Committed == nil {
				unfinished = true
			} else if progress[key].delivered() {
				message = "I couldn't deliver this request to every agent. Some agents have already received it."
			}
		}
	}
	var failures []error
	if unfinished {
		noticeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		integration, err := c.inbox.GetProjectIntegrationByID(noticeCtx, receipt.IntegrationID)
		if err == nil {
			if provider, ok := c.providers[integration.Provider].(interface {
				NotifyInboxFailure(
					context.Context, integrationstore.ProjectIntegrationRecord, integrationstore.IntegrationInboxRecord, string,
				) error
			}); ok {
				err = provider.NotifyInboxFailure(noticeCtx, integration, receipt, message)
			}
		}
		cancel()
		failures = append(failures, err)
	}
	if artifacts, ok := c.artifacts.(IntegrationInboxArtifactCleaner); ok && integrationInboxPlanHasArtifacts(plan) {
		failures = append(failures, CleanupTerminalIntegrationInboxArtifacts(ctx, c.inbox, artifacts, projectID, receiptID))
	}
	return errors.Join(failures...)
}

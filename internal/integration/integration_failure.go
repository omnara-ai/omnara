package integration

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
	var failures []error
	var plan IntegrationInboxPlan
	if len(receipt.Plan) != 0 {
		plan, err = decodeIntegrationInboxPlan(receipt.Plan)
		if err != nil {
			return err
		}
		outcomes, err := c.router.execution.GetIntegrationInboxOutcomes(ctx, receipt)
		unfinished = false
		if err != nil {
			failures = append(failures, err)
		} else {
			for _, outcome := range outcomes {
				if outcome == executionstore.InboxSlotPending {
					unfinished = true
				} else if outcome == executionstore.InboxSlotDelivered {
					message = "I couldn't deliver this request to every agent. Some agents have already received it."
				}
			}
		}
	}
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

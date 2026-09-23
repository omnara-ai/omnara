package apps

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (c *AppInboxConsumer) FinalizeFailure(ctx context.Context, projectID, receiptID uuid.UUID) error {
	receipt, err := c.inbox.GetAppInbox(ctx, projectID, receiptID)
	if err != nil {
		return err
	}
	if receipt.State != appstore.AppInboxFailed {
		return storeerr.ErrStateTransitionConflict
	}
	message := inboxFailureMessage
	unfinished := true
	var plan AppInboxPlan
	if len(receipt.Plan) != 0 {
		plan, err = decodeAppInboxPlan(receipt.Plan)
		if err != nil {
			return err
		}
		var progress map[string]appInboxSlotProgress
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
		app, err := c.inbox.GetProjectAppByID(noticeCtx, receipt.AppID)
		if err == nil {
			if provider, ok := c.providers[app.Provider].(interface {
				NotifyInboxFailure(
					context.Context, appstore.ProjectAppRecord, appstore.AppInboxRecord, string,
				) error
			}); ok {
				err = provider.NotifyInboxFailure(noticeCtx, app, receipt, message)
			}
		}
		cancel()
		failures = append(failures, err)
	}
	if artifacts, ok := c.artifacts.(AppInboxArtifactCleaner); ok && appInboxPlanHasArtifacts(plan) {
		failures = append(failures, CleanupTerminalAppInboxArtifacts(ctx, c.inbox, artifacts, projectID, receiptID))
	}
	return errors.Join(failures...)
}

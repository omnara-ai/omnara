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

// FinalizeFailure is a best-effort follow-up to the worker's terminal commit.
// It never requeues work or writes progress. Crash recovery can finalize a receipt
// without this pass; losing a notice must not leave the conversation blocked.
func (c *AppInboxConsumer) FinalizeFailure(ctx context.Context, projectID, receiptID uuid.UUID) error {
	receipt, err := c.inbox.GetIntegrationInbox(ctx, projectID, receiptID)
	if err != nil {
		return err
	}
	if receipt.State != integrationstore.IntegrationInboxFailed {
		return storeerr.ErrStateTransitionConflict
	}
	message := inboxFailureMessage
	if len(receipt.Plan) != 0 {
		plan, err := decodeAppInboxPlan(receipt.Plan)
		if err != nil {
			return err
		}
		var progress map[string]map[string]json.RawMessage
		if err := json.Unmarshal(receipt.Progress, &progress); err != nil {
			return err
		}
		unfinished := false
		for key := range plan {
			if _, committed := progress[key]["committed"]; !committed {
				unfinished = true
			} else {
				message = "I couldn't deliver this request to every agent. Some agents have already received it."
			}
		}
		if !unfinished {
			return nil // An empty plan or failed Complete does not mean lost input.
		}
	}
	var failures []error
	noticeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	app, err := c.inbox.GetProjectAppByID(noticeCtx, receipt.AppID)
	if err == nil {
		if provider, ok := c.providers[app.Provider].(interface {
			NotifyInboxFailure(context.Context, integrationstore.ProjectAppRecord, integrationstore.IntegrationInboxRecord, string) error
		}); ok {
			err = provider.NotifyInboxFailure(noticeCtx, app, receipt, message)
		}
	}
	cancel()
	failures = append(failures, err)
	if artifacts, ok := c.artifacts.(FailedAppArtifactCleaner); ok {
		failures = append(failures, CleanupFailedAppInboxArtifacts(ctx, c.inbox, artifacts, projectID, receiptID))
	}
	return errors.Join(failures...)
}

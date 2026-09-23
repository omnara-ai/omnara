package apps

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const inboxFailureMessage = "I couldn't deliver this request to the agent. Please send your message again."
const scheduledInboxFailureMessage = "I couldn't deliver this scheduled request to the agent."
const selectedInboxFailureMessage = "I couldn't deliver this request to the agent. " +
	"Please mention me again to choose a profile."

func checkInboxFailureReceipt(app appstore.ProjectAppRecord, receipt appstore.AppInboxRecord,
	provider string,
) error {
	if receipt.ID == uuid.Nil || receipt.State != appstore.AppInboxFailed ||
		receipt.ProjectID != app.ProjectID || receipt.AppID != app.ID ||
		app.State != appstore.ProjectAppStateActive || app.Provider != provider {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func inboxFailureSelectedEvent(receipt appstore.AppInboxRecord, provider string) (AppEvent, error) {
	var events []AppEvent
	if json.Unmarshal(receipt.Events, &events) != nil || len(events) != 1 || events[0].Event.Kind != "message" {
		return AppEvent{}, fmt.Errorf("invalid selected inbox failure source")
	}
	if err := events[0].Event.Scope.Validate(provider); err != nil {
		return AppEvent{}, err
	}
	return events[0], nil
}

func scheduledInboxFailureScope(receipt appstore.AppInboxRecord,
	provider string,
) (appdefinition.Scope, bool, error) {
	if len(receipt.Plan) == 0 {
		return appdefinition.Scope{}, false, nil
	}
	plan, err := decodeAppInboxPlan(receipt.Plan)
	if err != nil {
		return appdefinition.Scope{}, false, err
	}
	if len(plan) == 0 {
		return appdefinition.Scope{}, false, nil
	}
	if len(plan) != 1 {
		return appdefinition.Scope{}, false, fmt.Errorf("invalid scheduled failure plan")
	}
	for _, slot := range plan {
		if err := slot.Scope.Validate(provider); err != nil {
			return appdefinition.Scope{}, false, err
		}
		return slot.Scope, true, nil
	}
	return appdefinition.Scope{}, false, nil
}

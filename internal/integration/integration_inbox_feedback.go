package integration

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const inboxFailureMessage = "I couldn't deliver this request to the agent. Please send your message again."
const scheduledInboxFailureMessage = "I couldn't deliver this scheduled request to the agent."
const selectedInboxFailureMessage = "I couldn't deliver this request to the agent. " +
	"Please mention me again to choose a profile."

func checkInboxFailureReceipt(
	integration integrationstore.ProjectIntegrationRecord,
	receipt integrationstore.IntegrationInboxRecord,
	provider string,
) error {
	if receipt.ID == uuid.Nil || receipt.State != integrationstore.IntegrationInboxFailed ||
		receipt.ProjectID != integration.ProjectID || receipt.IntegrationID != integration.ID ||
		integration.State != integrationstore.ProjectIntegrationStateActive || integration.Provider != provider {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func inboxFailureSelectedEvent(
	receipt integrationstore.IntegrationInboxRecord,
	provider string,
) (IntegrationEvent, error) {
	var events []IntegrationEvent
	if json.Unmarshal(receipt.Events, &events) != nil || len(events) != 1 || events[0].Event.Kind != "message" {
		return IntegrationEvent{}, fmt.Errorf("invalid selected inbox failure source")
	}
	if err := events[0].Event.Scope.Validate(provider); err != nil {
		return IntegrationEvent{}, err
	}
	return events[0], nil
}

func scheduledInboxFailureScope(receipt integrationstore.IntegrationInboxRecord,
	provider string,
) (integrationdefinition.Scope, bool, error) {
	if len(receipt.Plan) == 0 {
		return integrationdefinition.Scope{}, false, nil
	}
	plan, err := decodeIntegrationInboxPlan(receipt.Plan)
	if err != nil {
		return integrationdefinition.Scope{}, false, err
	}
	if len(plan) == 0 {
		return integrationdefinition.Scope{}, false, nil
	}
	if len(plan) != 1 {
		return integrationdefinition.Scope{}, false, fmt.Errorf("invalid scheduled failure plan")
	}
	for _, slot := range plan {
		if err := slot.Scope.Validate(provider); err != nil {
			return integrationdefinition.Scope{}, false, err
		}
		return slot.Scope, true, nil
	}
	return integrationdefinition.Scope{}, false, nil
}

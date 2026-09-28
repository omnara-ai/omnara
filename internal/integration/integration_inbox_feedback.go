package integration

import (
	"context"
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
	integration integrationstore.IntegrationRecord,
	receipt integrationstore.IntegrationInboxRecord,
	provider string,
) error {
	if receipt.ID == uuid.Nil || receipt.State != integrationstore.IntegrationInboxFailed ||
		receipt.ProjectID != integration.ProjectID || receipt.IntegrationID != integration.ID ||
		integration.State != integrationstore.IntegrationStateActive || integration.Provider != provider {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func inboxFailureSelectedEvent(
	ctx context.Context,
	reader interface {
		GetIntegrationProfileChoice(
			context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
		) (integrationstore.IntegrationProfileChoiceRecord, error)
	},
	receipt integrationstore.IntegrationInboxRecord, provider string,
) (IntegrationEvent, error) {
	if receipt.Source != integrationstore.IntegrationInboxSourceChoice || receipt.StateID == uuid.Nil {
		return IntegrationEvent{}, fmt.Errorf("invalid selected inbox failure source")
	}
	choice, err := reader.GetIntegrationProfileChoice(ctx, receipt.ProjectID, receipt.IntegrationID, receipt.StateID)
	if err != nil {
		return IntegrationEvent{}, err
	}
	event, err := selectedIntegrationEvent(choice)
	if err != nil {
		return IntegrationEvent{}, err
	}
	if event.Event.Kind != "message" {
		return IntegrationEvent{}, fmt.Errorf("invalid selected inbox failure event")
	}
	if err := event.Event.Scope.Validate(provider); err != nil {
		return IntegrationEvent{}, err
	}
	return *event, nil
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
	if len(plan.Recipients) == 0 {
		return integrationdefinition.Scope{}, false, nil
	}
	if len(plan.Recipients) != 1 {
		return integrationdefinition.Scope{}, false, fmt.Errorf("invalid scheduled failure plan")
	}
	if err := plan.Message.Scope.Validate(provider); err != nil {
		return integrationdefinition.Scope{}, false, err
	}
	return plan.Message.Scope, true, nil
}

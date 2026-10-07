package integration

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (l *ChatIntegrationLauncher) HandleState(ctx context.Context, receipt integrationstore.IntegrationInboxRecord,
	integration integrationstore.IntegrationRecord, process IntegrationStateProcess,
) ([]IntegrationRecipientAdmission, error) {
	choice, err := l.store.GetIntegrationProfileChoice(ctx, receipt.ProjectID, integration.ID, receipt.IntegrationStateID)
	if err != nil {
		return nil, err
	}
	event, err := selectedIntegrationEvent(choice)
	if err != nil {
		return nil, err
	}
	outcomes, err := process(event, choice.Payload)
	if errors.Is(err, ErrIntegrationLaunchUnavailable) {
		err = fmt.Errorf("%w: %w", ErrIntegrationInboundPermanent, err)
	}
	return outcomes, err
}

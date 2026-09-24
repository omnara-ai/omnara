package integration

import (
	"context"
	"fmt"
	"maps"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type IntegrationScheduledHandler func(
	context.Context,
	integrationstore.IntegrationInboxLease,
	integrationstore.IntegrationInboxRecord,
	integrationstore.ProjectIntegrationRecord,
) ([]IntegrationSlotAdmission, error)

type IntegrationInboxConsumerOption func(*IntegrationInboxConsumer)

func WithIntegrationScheduledHandlers(
	handlers map[integrationdefinition.Type]IntegrationScheduledHandler,
) IntegrationInboxConsumerOption {
	snapshot := maps.Clone(handlers)
	return func(consumer *IntegrationInboxConsumer) { consumer.scheduled = snapshot }
}

func (c *IntegrationInboxConsumer) consumeScheduled(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	integration integrationstore.ProjectIntegrationRecord,
) ([]IntegrationSlotAdmission, error) {
	handler := c.scheduled[integration.IntegrationType]
	if handler == nil {
		return nil, fmt.Errorf(
			"%w: no scheduled handler for integration type %s",
			ErrScheduledActionFailed,
			integration.IntegrationType,
		)
	}
	return handler(ctx, lease, receipt, integration)
}

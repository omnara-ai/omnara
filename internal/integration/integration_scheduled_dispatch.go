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
	integrationstore.IntegrationRecord,
) ([]IntegrationSlotAdmission, error)

type IntegrationInboxConsumerOption func(*IntegrationInboxConsumer)

func WithIntegrationScheduledHandlers(
	handlers map[integrationdefinition.Kind]IntegrationScheduledHandler,
) IntegrationInboxConsumerOption {
	snapshot := maps.Clone(handlers)
	return func(consumer *IntegrationInboxConsumer) { consumer.scheduled = snapshot }
}

func (c *IntegrationInboxConsumer) consumeScheduled(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	integration integrationstore.IntegrationRecord,
) ([]IntegrationSlotAdmission, error) {
	handler := c.scheduled[integration.IntegrationKind]
	if handler == nil {
		return nil, fmt.Errorf(
			"%w: no scheduled handler for integration type %s",
			ErrScheduledActionFailed,
			integration.IntegrationKind,
		)
	}
	return handler(ctx, lease, receipt, integration)
}

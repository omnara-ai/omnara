package integration

import (
	"context"
	"fmt"
	"maps"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

// AppScheduledHandler owns an app type's scheduled action and its recovery.
// Actions may complete without launching an agent or using a provider adapter.
type AppScheduledHandler func(
	context.Context,
	integrationstore.IntegrationInboxLease,
	integrationstore.IntegrationInboxRecord,
	integrationstore.ProjectAppRecord,
) ([]AppSlotAdmission, error)

type AppInboxConsumerOption func(*AppInboxConsumer)

// WithAppScheduledHandlers registers app-owned actions independently of the
// provider adapters used to normalize incoming events.
func WithAppScheduledHandlers(handlers map[appdefinition.Type]AppScheduledHandler) AppInboxConsumerOption {
	snapshot := maps.Clone(handlers)
	return func(consumer *AppInboxConsumer) { consumer.scheduled = snapshot }
}

func (c *AppInboxConsumer) consumeScheduled(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	app integrationstore.ProjectAppRecord,
) ([]AppSlotAdmission, error) {
	handler := c.scheduled[app.AppType]
	if handler == nil {
		return nil, fmt.Errorf("%w: no scheduled handler for app type %s", ErrScheduledActionFailed, app.AppType)
	}
	return handler(ctx, lease, receipt, app)
}

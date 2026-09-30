package integration

import (
	"context"
	"maps"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type IntegrationStateProcess func(*IntegrationEvent, []byte) ([]IntegrationRecipientAdmission, error)

// IntegrationStateHandler completes through process (nil event for no deliveries), or returns an error.
// It owns state interpretation and any workflow-specific failure classification.
type IntegrationStateHandler func(context.Context, integrationstore.IntegrationInboxRecord,
	integrationstore.IntegrationRecord, IntegrationStateProcess) ([]IntegrationRecipientAdmission, error)

func WithIntegrationStateHandlers(
	handlers map[integrationdefinition.Kind]IntegrationStateHandler,
) IntegrationInboxConsumerOption {
	snapshot := maps.Clone(handlers)
	return func(consumer *IntegrationInboxConsumer) { consumer.stateHandlers = snapshot }
}

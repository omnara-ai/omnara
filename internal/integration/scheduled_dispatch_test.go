package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestScheduledDispatchUsesIntegrationKindWithoutThreadInputsOrProvider(t *testing.T) {
	event := integrationstore.ScheduledIntegrationEvent{
		TriggerID: uuid.New(),
		Occurrence: cronschedule.Occurrence{
			Name: "Refresh", DueAt: time.Now().UTC(), FiredAt: time.Now().UTC(), Timezone: "UTC",
		},
		Settings: json.RawMessage(`{"refresh":true}`),
	}
	payload, err := json.Marshal(event)
	require.NoError(t, err)
	receipt := integrationstore.IntegrationInboxRecord{
		ID: uuid.New(), ProjectID: uuid.New(), IntegrationID: uuid.New(),
		Source: integrationstore.IntegrationInboxSourceScheduled, Payload: payload, ClaimToken: uuid.New(),
	}
	const refreshType integrationdefinition.Kind = "refresh"
	const archiveType integrationdefinition.Kind = "archive"
	var calls []integrationdefinition.Kind
	handlers := map[integrationdefinition.Kind]IntegrationScheduledHandler{}
	for _, integrationKind := range []integrationdefinition.Kind{refreshType, archiveType} {
		handlers[integrationKind] = func(
			ctx context.Context,
			lease integrationstore.IntegrationInboxLease,
			got integrationstore.IntegrationInboxRecord,
			integration integrationstore.IntegrationRecord,
		) ([]IntegrationRecipientAdmission, error) {
			require.Equal(t, t.Context(), ctx)
			require.Equal(t, receipt.Lease(), lease)
			require.Equal(t, receipt, got)
			require.Equal(t, integrationKind, integration.IntegrationKind)
			accepted, err := got.ScheduledEvent()
			require.NoError(t, err)
			require.JSONEq(t, string(event.Settings), string(accepted.Settings))
			calls = append(calls, integrationKind)
			return nil, nil
		}
	}
	consumer := NewIntegrationInboxConsumer(nil, nil, nil, nil, nil, nil, WithIntegrationScheduledHandlers(handlers))
	delete(handlers, refreshType)
	for _, integrationKind := range []integrationdefinition.Kind{refreshType, archiveType} {
		results, err := consumer.consumeScheduled(
			t.Context(),
			receipt.Lease(),
			receipt,
			integrationstore.IntegrationRecord{
				ID:              receipt.IntegrationID,
				ProjectID:       receipt.ProjectID,
				IntegrationKind: integrationKind,
				Provider:        integrationdefinition.ProviderSlack,
			},
		)
		require.NoError(t, err)
		require.Empty(t, results)
	}
	require.Equal(t, []integrationdefinition.Kind{refreshType, archiveType}, calls)
}

func TestScheduledDispatchDoesNotFallBackToProvider(t *testing.T) {
	provider := &SlackIntegrationInboxProvider{}
	handler := NewThreadIntegrationScheduledHandler(nil, nil, provider)
	consumer := NewIntegrationInboxConsumer(nil, nil, nil,
		map[integrationdefinition.Provider]IntegrationInboxProvider{integrationdefinition.ProviderSlack: provider}, nil, nil,
		WithIntegrationScheduledHandlers(map[integrationdefinition.Kind]IntegrationScheduledHandler{
			integrationdefinition.SlackThread: handler.Handle,
		}),
	)
	_, err := consumer.consumeScheduled(t.Context(), integrationstore.IntegrationInboxLease{},
		integrationstore.IntegrationInboxRecord{}, integrationstore.IntegrationRecord{
			IntegrationKind: "another_slack_integration", Provider: integrationdefinition.ProviderSlack,
		},
	)
	require.ErrorIs(t, err, ErrScheduledActionFailed)
	require.ErrorContains(t, err, "another_slack_integration")
}

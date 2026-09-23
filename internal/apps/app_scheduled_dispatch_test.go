package apps

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/stretchr/testify/require"
)

func TestScheduledDispatchUsesAppTypeWithoutThreadInputsOrProvider(t *testing.T) {
	event := appstore.ScheduledAppEvent{
		TriggerID: uuid.New(),
		Occurrence: cronschedule.Occurrence{
			Name: "Refresh", DueAt: time.Now().UTC(), FiredAt: time.Now().UTC(), Timezone: "UTC",
		},
		Settings: json.RawMessage(`{"refresh":true}`),
	}
	payload, err := json.Marshal(event)
	require.NoError(t, err)
	receipt := appstore.AppInboxRecord{
		ID: uuid.New(), ProjectID: uuid.New(), AppID: uuid.New(),
		Source: appstore.AppInboxSourceScheduled, Payload: payload, ClaimToken: uuid.New(),
	}
	const refreshType appdefinition.Type = "refresh"
	const archiveType appdefinition.Type = "archive"
	var calls []appdefinition.Type
	handlers := map[appdefinition.Type]AppScheduledHandler{}
	for _, appType := range []appdefinition.Type{refreshType, archiveType} {
		handlers[appType] = func(
			ctx context.Context,
			lease appstore.AppInboxLease,
			got appstore.AppInboxRecord,
			app appstore.ProjectAppRecord,
		) ([]AppSlotAdmission, error) {
			require.Equal(t, t.Context(), ctx)
			require.Equal(t, receipt.Lease(), lease)
			require.Equal(t, receipt, got)
			require.Equal(t, appType, app.AppType)
			accepted, err := got.ScheduledEvent()
			require.NoError(t, err)
			require.JSONEq(t, string(event.Settings), string(accepted.Settings))
			calls = append(calls, appType)
			return nil, nil
		}
	}
	consumer := NewAppInboxConsumer(nil, nil, nil, nil, nil, nil, WithAppScheduledHandlers(handlers))
	delete(handlers, refreshType)
	for _, appType := range []appdefinition.Type{refreshType, archiveType} {
		results, err := consumer.consumeScheduled(t.Context(), receipt.Lease(), receipt, appstore.ProjectAppRecord{
			ID: receipt.AppID, ProjectID: receipt.ProjectID, AppType: appType, Provider: appdefinition.ProviderSlack,
		})
		require.NoError(t, err)
		require.Empty(t, results)
	}
	require.Equal(t, []appdefinition.Type{refreshType, archiveType}, calls)
}

func TestScheduledDispatchDoesNotFallBackToProvider(t *testing.T) {
	provider := &SlackAppInboxProvider{}
	handler := NewThreadAppScheduledHandler(nil, nil, provider)
	consumer := NewAppInboxConsumer(nil, nil, nil,
		map[string]AppInboxProvider{appdefinition.ProviderSlack: provider}, nil, nil,
		WithAppScheduledHandlers(map[appdefinition.Type]AppScheduledHandler{
			appdefinition.SlackThread: handler.Handle,
		}),
	)
	_, err := consumer.consumeScheduled(t.Context(), appstore.AppInboxLease{},
		appstore.AppInboxRecord{}, appstore.ProjectAppRecord{
			AppType: "another_slack_app", Provider: appdefinition.ProviderSlack,
		},
	)
	require.ErrorIs(t, err, ErrScheduledActionFailed)
	require.ErrorContains(t, err, "another_slack_app")
}

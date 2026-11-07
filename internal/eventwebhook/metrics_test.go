package eventwebhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/stretchr/testify/require"
)

func webhookMetrics(set *metrics.Set) string {
	response := httptest.NewRecorder()
	set.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return response.Body.String()
}

func TestSenderMetricsCoverSlotsAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, store, delivery := testSender()
		sender.maxInFlight = 2
		set := metrics.New()
		sender.recorder = metrics.NewEventWebhookRecorder(set)
		sender.client = &http.Client{Transport: testTransport(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
		store.pending <- delivery
		store.pending <- delivery
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); sender.Run(ctx) }()
		synctest.Wait()
		body := webhookMetrics(set)
		require.Contains(t, body, "omnara_event_webhook_slots_used 2")
		require.Contains(t, body, "omnara_event_webhook_capacity 2")
		cancel()
		<-done
		body = webhookMetrics(set)
		require.Contains(t, body, "omnara_event_webhook_slots_used 0")
		require.Contains(t, body, "omnara_event_webhook_capacity 0")
		require.Contains(t, body, `omnara_event_webhook_attempts_total{cause="none",outcome="canceled"} 2`)
	})
}

func TestDeletedEndpointIsNotAnAcknowledgedDelivery(t *testing.T) {
	sender, store, delivery := testSender()
	store.endpoint.URL = ""
	delivery.CreatedAt = time.Now().Add(-time.Minute)
	set := metrics.New()
	sender.recorder = metrics.NewEventWebhookRecorder(set)
	sender.deliver(t.Context(), delivery)
	body := webhookMetrics(set)
	require.Contains(t, body, `omnara_event_webhook_attempts_total{cause="none",outcome="skipped"} 1`)
	require.Contains(t, body, `omnara_event_webhook_delivery_age_seconds_count{attempt="first"} 0`)
	require.EqualValues(t, 1, store.completed.Load())
}

func TestSenderClassifiesPreparationFailuresAsLocal(t *testing.T) {
	sender, store, delivery := testSender()
	store.toolCallErr = errors.New("database unavailable")
	set := metrics.New()
	sender.recorder = metrics.NewEventWebhookRecorder(set)
	sender.deliver(t.Context(), delivery)
	require.Contains(t, webhookMetrics(set),
		`omnara_event_webhook_attempts_total{cause="local",outcome="retry_scheduled"} 1`)
}

func TestSenderSeparatesSigningConfigurationFromLocalFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		cause string
		level string
	}{
		{"unavailable secret", fmt.Errorf("access revoked: %w", storeerr.ErrNotFound), "configuration", "WARN"},
		{"invalid signing key", nil, "configuration", "WARN"},
		{"database failure", errors.New("database unavailable"), "local", "ERROR"},
		{"decryption failure", errors.New("decrypt secret payload: key management unavailable"), "local", "ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender, store, delivery := testSender()
			store.endpoint.SigningSecretID = uuid.New()
			store.secret, store.secretErr = "invalid key", test.err
			store.retryResult = executionstore.EventWebhookDeliveryRetryResult{GaveUp: true}
			set := metrics.New()
			sender.recorder = metrics.NewEventWebhookRecorder(set)
			var output bytes.Buffer
			sender.log = slog.New(slog.NewJSONHandler(&output, nil))
			sender.deliver(t.Context(), delivery)
			require.Contains(t, webhookMetrics(set),
				`omnara_event_webhook_attempts_total{cause="`+test.cause+`",outcome="gave_up"} 1`)
			var record map[string]any
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
			require.Equal(t, test.cause, record["event_webhook.cause"])
			require.Equal(t, test.level, record["level"])
		})
	}
}

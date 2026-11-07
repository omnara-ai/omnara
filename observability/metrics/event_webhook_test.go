package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEventWebhookBacklogEmptyFailureAndRecovery(t *testing.T) {
	set := New(WithNamespace("review"))
	m := NewEventWebhookBacklogRecorder(set)
	body := scrapeMetrics(t, set)
	require.Contains(t, body, `review_event_webhook_queue_deliveries{state="due"} NaN`)
	require.Contains(t, body, "review_event_webhook_backlog_sample_last_success_timestamp_seconds 0")
	m.Record(EventWebhookBacklogSample{
		Due: 3, Delayed: 2, InFlight: 1, Expired: 4, OldestDueAge: 2 * time.Minute, OldestDueLag: time.Second,
	}, nil)
	body = scrapeMetrics(t, set)
	require.Contains(t, body, `review_event_webhook_queue_deliveries{state="due"} 3`)
	require.Contains(t, body, `review_event_webhook_queue_deliveries{state="delayed"} 2`)
	require.Contains(t, body, `review_event_webhook_queue_deliveries{state="in_flight"} 1`)
	require.Contains(t, body, `review_event_webhook_queue_deliveries{state="expired"} 4`)
	require.Contains(t, body, "review_event_webhook_oldest_due_age_seconds 120")
	require.Contains(t, body, "review_event_webhook_oldest_due_lag_seconds 1")
	lastSuccess := func() float64 {
		families, err := set.registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() == "review_event_webhook_backlog_sample_last_success_timestamp_seconds" {
				return family.Metric[0].GetGauge().GetValue()
			}
		}
		t.Fatal("missing sample freshness")
		return 0
	}
	success := lastSuccess()
	require.Positive(t, success)
	m.Record(EventWebhookBacklogSample{}, errors.New("unavailable"))
	body = scrapeMetrics(t, set)
	require.Contains(t, body, "review_event_webhook_oldest_due_age_seconds NaN")
	require.Contains(t, body, "review_event_webhook_oldest_due_lag_seconds NaN")
	require.Contains(t, body, "review_event_webhook_backlog_sample_errors_total 1")
	// Failure leaves freshness untouched, and an empty successful sample is healthy.
	require.Equal(t, success, lastSuccess())
	m.Record(EventWebhookBacklogSample{}, nil)
	body = scrapeMetrics(t, set)
	require.Contains(t, body, `review_event_webhook_queue_deliveries{state="due"} 0`)
	require.Contains(t, body, "review_event_webhook_oldest_due_age_seconds 0")
	m.RecordExpired(2)
	m.RecordExpired(0)
	require.Contains(t, scrapeMetrics(t, set), "review_event_webhook_expired_total 2")
	var absent *EventWebhookBacklogRecorder
	absent.Record(EventWebhookBacklogSample{}, nil)
	absent.RecordExpired(2)
}

func TestEventWebhookDeliveryMetrics(t *testing.T) {
	set := New()
	m := NewEventWebhookRecorder(set)
	m.RecordCapacity(8)
	done := m.StartDelivery()
	body := scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_event_webhook_slots_used 1")
	require.Contains(t, body, "omnara_event_webhook_capacity 8")
	m.RecordAttempt("retry_scheduled", "recipient")
	m.RecordAcknowledged(time.Now().Add(-time.Minute), 2)
	done()
	body = scrapeMetrics(t, set)
	require.Contains(t, body, `omnara_event_webhook_attempts_total{cause="recipient",outcome="retry_scheduled"} 1`)
	require.Contains(t, body, `omnara_event_webhook_delivery_age_seconds_count{attempt="retry"} 1`)
	require.Contains(t, body, "omnara_event_webhook_slots_used 0")
	var absent *EventWebhookRecorder
	absent.RecordCapacity(1)
	absent.StartDelivery()()
	absent.RecordAttempt("completed", "none")
	absent.RecordAcknowledged(time.Now(), 1)
}

func TestApplicationDeliveryBufferMetrics(t *testing.T) {
	set := New()
	notifications := NewNotificationRecorder(set)
	depth := 2
	notifications.RecordPublisherQueue(4096, func() int { return depth })
	stream := NewAgentStreamRecorder(set)
	stream.RecordDrop("tool_call_update")
	stream.RecordDrop("model_output_delta")
	body := scrapeMetrics(t, set)
	require.Contains(t, body, "omnara_notifications_publisher_queue_depth 2")
	require.Contains(t, body, "omnara_notifications_publisher_queue_capacity 4096")
	require.Contains(t, body, `omnara_agent_stream_buffer_dropped_total{frame="tool_call_update"} 1`)
	require.Contains(t, body, `omnara_agent_stream_buffer_dropped_total{frame="model_output_delta"} 1`)
	depth = 0
	require.Contains(t, scrapeMetrics(t, set), "omnara_notifications_publisher_queue_depth 0")
}

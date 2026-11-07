package metrics

import (
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type EventWebhookRecorder struct {
	slots, capacity prometheus.Gauge
	attempts        *prometheus.CounterVec
	deliveryAge     *prometheus.HistogramVec
}

func NewEventWebhookRecorder(set *Set) *EventWebhookRecorder {
	m := &EventWebhookRecorder{
		slots: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "slots_used",
			Help: "Delivery slots occupied on this worker, including payload reads, HTTP attempts, and result persistence.",
		}),
		capacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "capacity",
			Help: "Configured delivery slots on this worker; zero while the sender is stopped.",
		}),
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "attempts_total",
			Help: "Claimed delivery attempt outcomes, including local failures and deleted endpoints.",
		}, []string{"outcome", "cause"}),
		deliveryAge: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "delivery_age_seconds",
			Help:    "Age since enqueue when a receiver returns 2xx; retries can acknowledge a delivery more than once.",
			Buckets: []float64{0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 600},
		}, []string{"attempt"}),
	}
	for _, outcome := range []string{
		"completed", "skipped", "retry_scheduled", "gave_up", "retry_failed", "complete_failed", "canceled",
	} {
		for _, cause := range []string{"none", "recipient", "configuration", "local"} {
			m.attempts.WithLabelValues(outcome, cause)
		}
	}
	m.deliveryAge.WithLabelValues("first")
	m.deliveryAge.WithLabelValues("retry")
	set.MustRegister(m.slots, m.capacity, m.attempts, m.deliveryAge)
	return m
}

func (m *EventWebhookRecorder) RecordCapacity(capacity int) {
	if m != nil {
		m.capacity.Set(float64(capacity))
	}
}

func (m *EventWebhookRecorder) StartDelivery() func() {
	if m == nil {
		return func() {}
	}
	m.slots.Inc()
	return m.slots.Dec
}

func (m *EventWebhookRecorder) RecordAttempt(outcome, cause string) {
	if m != nil {
		m.attempts.WithLabelValues(outcome, cause).Inc()
	}
}

func (m *EventWebhookRecorder) RecordAcknowledged(createdAt time.Time, attempt int32) {
	if m != nil && !createdAt.IsZero() {
		label := "first"
		if attempt > 1 {
			label = "retry"
		}
		m.deliveryAge.WithLabelValues(label).Observe(math.Max(time.Since(createdAt).Seconds(), 0))
	}
}

type EventWebhookBacklogSample struct {
	Due, Delayed, InFlight, Expired int64
	OldestDueAge, OldestDueLag      time.Duration
}

type EventWebhookBacklogRecorder struct {
	queue                                   *prometheus.GaugeVec
	oldestDueAge, oldestDueLag, lastSuccess prometheus.Gauge
	sampleErrors, expired                   prometheus.Counter
}

func NewEventWebhookBacklogRecorder(set *Set) *EventWebhookBacklogRecorder {
	m := &EventWebhookBacklogRecorder{
		queue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "queue_deliveries",
			Help: "Cluster-wide delivery rows by exclusive state; maintenance samples, max across replicas, never sum. " +
				"NaN before a sample or on failure.",
		}, []string{"state"}),
		oldestDueAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "oldest_due_age_seconds",
			Help: "Age since enqueue of the oldest due delivery without a live claim, including organization-throttled work. " +
				"Zero when empty; NaN before sampling or on failure.",
		}),
		oldestDueLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "oldest_due_lag_seconds",
			Help: "Time since next_attempt_at for the oldest due delivery without a live claim, " +
				"including organization-throttled work. " +
				"Zero when empty; NaN before sampling or on failure.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "backlog_sample_last_success_timestamp_seconds",
			Help: "Unix time of the last successful delivery queue sample, including empty results; zero before success.",
		}),
		sampleErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "backlog_sample_errors_total",
			Help: "Failed delivery queue samples, including canceled samples.",
		}),
		expired: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: set.Namespace(), Subsystem: "event_webhook", Name: "expired_total",
			Help: "Delivery rows deleted after their delivery window expired without persisted completion. " +
				"Excludes endpoint deletion and counts each cleanup deletion once.",
		}),
	}
	m.invalidate()
	set.MustRegister(m.queue, m.oldestDueAge, m.oldestDueLag, m.lastSuccess, m.sampleErrors, m.expired)
	return m
}

func (m *EventWebhookBacklogRecorder) invalidate() {
	for _, state := range []string{"due", "delayed", "in_flight", "expired"} {
		m.queue.WithLabelValues(state).Set(math.NaN())
	}
	m.oldestDueAge.Set(math.NaN())
	m.oldestDueLag.Set(math.NaN())
}

func (m *EventWebhookBacklogRecorder) Record(sample EventWebhookBacklogSample, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.invalidate()
		m.sampleErrors.Inc()
		return
	}
	m.queue.WithLabelValues("due").Set(float64(sample.Due))
	m.queue.WithLabelValues("delayed").Set(float64(sample.Delayed))
	m.queue.WithLabelValues("in_flight").Set(float64(sample.InFlight))
	m.queue.WithLabelValues("expired").Set(float64(sample.Expired))
	m.oldestDueAge.Set(math.Max(sample.OldestDueAge.Seconds(), 0))
	m.oldestDueLag.Set(math.Max(sample.OldestDueLag.Seconds(), 0))
	m.lastSuccess.SetToCurrentTime()
}

func (m *EventWebhookBacklogRecorder) RecordExpired(count int64) {
	if m != nil && count > 0 {
		m.expired.Add(float64(count))
	}
}

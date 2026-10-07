package metrics

import (
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type IntegrationInboxRecorder struct {
	oldestReadyLag     prometheus.Gauge
	lastSuccess        prometheus.Gauge
	processingTotal    *prometheus.CounterVec
	processingDuration *prometheus.HistogramVec
}

func NewIntegrationInboxRecorder(set *Set) *IntegrationInboxRecorder {
	m := &IntegrationInboxRecorder{
		oldestReadyLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "integration_inbox", Name: "oldest_ready_lag_seconds",
			Help: "Sampled age since next_attempt_at of the oldest due queued receipt, including inactive scopes. " +
				"Zero when empty; NaN before a sample or on failure.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "integration_inbox", Name: "lag_sample_last_success_timestamp_seconds",
			Help: "Unix time of the last successful inbox lag sample, including empty results; zero before success.",
		}),
		processingTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara", Subsystem: "integration_inbox", Name: "processing_total",
			Help: "Inbox processing attempts by outcome observed by the worker; excludes crash recovery.",
		}, []string{"outcome"}),
		processingDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omnara", Subsystem: "integration_inbox", Name: "processing_duration_seconds",
			Help:    "Inbox processing attempt duration, including settlement and failure finalization; excludes queue time.",
			Buckets: []float64{0.01, 0.1, 0.5, 1, 5, 15, 30, 60, 120, 300},
		}, []string{"outcome"}),
	}
	m.oldestReadyLag.Set(math.NaN())
	set.MustRegister(m.oldestReadyLag, m.lastSuccess, m.processingTotal, m.processingDuration)
	return m
}

func (m *IntegrationInboxRecorder) RecordOldestReadyLag(lag time.Duration, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.oldestReadyLag.Set(math.NaN())
		return
	}
	m.oldestReadyLag.Set(lag.Seconds())
	m.lastSuccess.SetToCurrentTime()
}

func (m *IntegrationInboxRecorder) RecordProcessing(outcome string, duration time.Duration) {
	if m == nil {
		return
	}
	switch outcome {
	case "completed", "retry_scheduled", "failed", "lease_lost", "retry_not_recorded":
	default:
		outcome = "other"
	}
	m.processingTotal.WithLabelValues(outcome).Inc()
	m.processingDuration.WithLabelValues(outcome).Observe(duration.Seconds())
}

type IntegrationInboxIntakeRecorder struct {
	total *prometheus.CounterVec
}

func NewIntegrationInboxIntakeRecorder(set *Set) *IntegrationInboxIntakeRecorder {
	m := &IntegrationInboxIntakeRecorder{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara", Subsystem: "integration_inbox", Name: "intake_total",
			Help: "Per-integration verified webhook or Discord MESSAGE_CREATE intake attempts. " +
				"Webhook accepted/duplicate distinguish new/existing receipts; Discord committed includes replays. " +
				"Filtered means excluded before insertion; error means receipt persistence failed. " +
				"Excludes pure checkpoints.",
		}, []string{"provider", "outcome"}),
	}
	set.MustRegister(m.total)
	return m
}

func (m *IntegrationInboxIntakeRecorder) Record(provider, outcome string) {
	if m == nil {
		return
	}
	switch provider {
	case "slack", "discord", "github":
	default:
		provider = "other"
	}
	switch outcome {
	case "accepted", "duplicate", "committed", "filtered", "error":
	default:
		outcome = "error"
	}
	m.total.WithLabelValues(provider, outcome).Inc()
}

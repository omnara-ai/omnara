package metrics

import (
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type IntegrationInboxRecorder struct {
	oldestReadyLag prometheus.Gauge
	lastSuccess    prometheus.Gauge
}

func NewIntegrationInboxRecorder(set *Set) *IntegrationInboxRecorder {
	m := &IntegrationInboxRecorder{
		oldestReadyLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "integration_inbox", Name: "oldest_ready_lag_seconds",
			Help: "Sampled age since available_at of the oldest due pending receipt, including inactive scopes. " +
				"Zero when empty; NaN before a sample or on failure.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "integration_inbox", Name: "lag_sample_last_success_timestamp_seconds",
			Help: "Unix time of the last successful inbox lag sample, including empty results; zero before success.",
		}),
	}
	m.oldestReadyLag.Set(math.NaN())
	set.MustRegister(m.oldestReadyLag, m.lastSuccess)
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

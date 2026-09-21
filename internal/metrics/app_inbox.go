package metrics

import (
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// AppInboxRecorder exports cached samples only; scraping never queries storage.
type AppInboxRecorder struct {
	oldestReadyLag prometheus.Gauge
	lastSuccess    prometheus.Gauge
}

func NewAppInboxRecorder(set *Set) *AppInboxRecorder {
	m := &AppInboxRecorder{
		oldestReadyLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "app_inbox", Name: "oldest_ready_lag_seconds",
			Help: "Sampled age since available_at of the oldest due pending receipt, including inactive scopes. " +
				"Zero when empty; NaN before a sample or on failure.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "app_inbox", Name: "lag_sample_last_success_timestamp_seconds",
			Help: "Unix time of the last successful inbox lag sample, including empty results; zero before success.",
		}),
	}
	m.oldestReadyLag.Set(math.NaN())
	set.MustRegister(m.oldestReadyLag, m.lastSuccess)
	return m
}

func (m *AppInboxRecorder) RecordOldestReadyLag(lag time.Duration, err error) {
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

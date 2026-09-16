package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const SubsystemIntegrationControl = "integration_control"

type IntegrationControlRecorder struct {
	oldestUnappliedTimestamp prometheus.Gauge
	lastObservationTimestamp prometheus.Gauge
}

func NewIntegrationControlRecorder(set *Set) *IntegrationControlRecorder {
	m := &IntegrationControlRecorder{
		oldestUnappliedTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara",
			Subsystem: SubsystemIntegrationControl,
			Name:      "oldest_unapplied_timestamp_seconds",
			Help: "Creation time of the oldest pending or processing integration control receipt, " +
				"as Unix seconds. Zero when the last successful observation found no unapplied receipts.",
		}),
		lastObservationTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara",
			Subsystem: SubsystemIntegrationControl,
			Name:      "last_observation_timestamp_seconds",
			Help: "Time of the last successful oldest-unapplied integration control observation, " +
				"as Unix seconds. Zero until first observed.",
		}),
	}
	set.MustRegister(m.oldestUnappliedTimestamp, m.lastObservationTimestamp)
	return m
}

// RecordOldestUnapplied records only successful observations. A nil oldest means
// the queue is empty. Callers must not replace the last observation on an error.
// Alert on time() minus a nonzero oldest timestamp; use the observation timestamp
// to distinguish fresh empty queues from failed or stalled maintenance.
func (m *IntegrationControlRecorder) RecordOldestUnapplied(oldest *time.Time, observedAt time.Time) {
	if m == nil {
		return
	}
	var oldestSeconds float64
	if oldest != nil {
		oldestSeconds = float64(oldest.Unix()) + float64(oldest.Nanosecond())/float64(time.Second)
	}
	m.oldestUnappliedTimestamp.Set(oldestSeconds)
	m.lastObservationTimestamp.Set(
		float64(observedAt.Unix()) + float64(observedAt.Nanosecond())/float64(time.Second),
	)
}

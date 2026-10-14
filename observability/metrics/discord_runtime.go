package metrics

import (
	"math"

	"github.com/prometheus/client_golang/prometheus"
)

type DiscordRuntimeRecorder struct {
	claims   prometheus.Gauge
	capacity prometheus.Gauge
}

func NewDiscordRuntimeRecorder(set *Set) *DiscordRuntimeRecorder {
	m := &DiscordRuntimeRecorder{
		claims: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "discord_runtime", Name: "claims",
			Help: "Discord connection slots occupied by this worker, including connection attempts; " +
				"not a connection health check.",
		}),
		capacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "discord_runtime", Name: "capacity",
			Help: "Maximum Discord connection slots on this worker.",
		}),
	}
	set.MustRegister(m.claims, m.capacity)
	return m
}

func (m *DiscordRuntimeRecorder) RecordCapacity(capacity int) {
	if m != nil {
		m.capacity.Set(float64(capacity))
	}
}

func (m *DiscordRuntimeRecorder) RecordClaims(claims int) {
	if m != nil {
		m.claims.Set(float64(claims))
	}
}

type DiscordRuntimeDemandRecorder struct {
	unclaimed   prometheus.Gauge
	lastSuccess prometheus.Gauge
}

func NewDiscordRuntimeDemandRecorder(set *Set) *DiscordRuntimeDemandRecorder {
	m := &DiscordRuntimeDemandRecorder{
		unclaimed: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "discord_runtime", Name: "unclaimed_integrations",
			Help: "Cluster-wide active Discord integrations without an unexpired worker claim, " +
				"including retry backoff and unavailable credentials. " +
				"Sampled by maintenance; use max across replicas. NaN before a sample or on failure.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "discord_runtime", Name: "demand_sample_last_success_timestamp_seconds",
			Help: "Unix time of the last successful unclaimed integration count; zero before success.",
		}),
	}
	m.unclaimed.Set(math.NaN())
	set.MustRegister(m.unclaimed, m.lastSuccess)
	return m
}

func (m *DiscordRuntimeDemandRecorder) RecordUnclaimed(count int64, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.unclaimed.Set(math.NaN())
		return
	}
	m.unclaimed.Set(float64(count))
	m.lastSuccess.SetToCurrentTime()
}

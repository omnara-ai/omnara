package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const SubsystemProviderRuntime = "provider_runtime"

type ProviderRuntimeOperation string

const (
	ProviderRuntimeOperationDiscovery    ProviderRuntimeOperation = "discovery"
	ProviderRuntimeOperationConfirmation ProviderRuntimeOperation = "confirmation"
)

type ProviderRuntimeResult string

const (
	ProviderRuntimeResultSuccess ProviderRuntimeResult = "success"
	ProviderRuntimeResultError   ProviderRuntimeResult = "error"
)

type ProviderRuntimeEvent string

const (
	ProviderRuntimeEventPages              ProviderRuntimeEvent = "pages"
	ProviderRuntimeEventScopes             ProviderRuntimeEvent = "scopes"
	ProviderRuntimeEventScopeCooldownSkips ProviderRuntimeEvent = "scope_cooldown_skips"
	ProviderRuntimeEventTargets            ProviderRuntimeEvent = "targets"
	ProviderRuntimeEventObservations       ProviderRuntimeEvent = "observations"
	ProviderRuntimeEventProviderErrors     ProviderRuntimeEvent = "provider_errors"
	ProviderRuntimeEventMarkersSet         ProviderRuntimeEvent = "markers_set"
	ProviderRuntimeEventMarkersCleared     ProviderRuntimeEvent = "markers_cleared"
	ProviderRuntimeEventConfirmations      ProviderRuntimeEvent = "confirmations"
	ProviderRuntimeEventDeletionClaims     ProviderRuntimeEvent = "deletion_claims"
	ProviderRuntimeEventDeletionClaimRaces ProviderRuntimeEvent = "deletion_claim_races"
)

type ProviderRuntimeRecorder struct {
	passesTotal  *prometheus.CounterVec
	eventsTotal  *prometheus.CounterVec
	passDuration *prometheus.HistogramVec
}

func NewProviderRuntimeRecorder(set *Set) *ProviderRuntimeRecorder {
	m := &ProviderRuntimeRecorder{
		passesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara",
			Subsystem: SubsystemProviderRuntime,
			Name:      "reconciliation_passes_total",
			Help:      "Total provider runtime reconciliation passes.",
		}, []string{"operation", "result"}),
		eventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara",
			Subsystem: SubsystemProviderRuntime,
			Name:      "reconciliation_events_total",
			Help:      "Total provider runtime reconciliation events.",
		}, []string{"operation", "event"}),
		passDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omnara",
			Subsystem: SubsystemProviderRuntime,
			Name:      "reconciliation_pass_duration_seconds",
			Help:      "Provider runtime reconciliation pass duration in seconds.",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
		}, []string{"operation", "result"}),
	}
	set.MustRegister(m.passesTotal, m.eventsTotal, m.passDuration)
	return m
}

func (m *ProviderRuntimeRecorder) RecordPass(
	operation ProviderRuntimeOperation,
	result ProviderRuntimeResult,
	duration time.Duration,
) {
	if m == nil {
		return
	}
	labels := []string{string(operation), string(result)}
	m.passesTotal.WithLabelValues(labels...).Inc()
	m.passDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func (m *ProviderRuntimeRecorder) RecordEvents(
	operation ProviderRuntimeOperation,
	event ProviderRuntimeEvent,
	count int,
) {
	if m == nil || count <= 0 {
		return
	}
	m.eventsTotal.WithLabelValues(string(operation), string(event)).Add(float64(count))
}

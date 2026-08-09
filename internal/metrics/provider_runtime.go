package metrics

import "github.com/prometheus/client_golang/prometheus"

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
	passesTotal *prometheus.CounterVec
	eventsTotal *prometheus.CounterVec
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
	}
	set.MustRegister(m.passesTotal, m.eventsTotal)
	return m
}

func (m *ProviderRuntimeRecorder) RecordPass(
	operation ProviderRuntimeOperation,
	result ProviderRuntimeResult,
) {
	if m == nil {
		return
	}
	m.passesTotal.WithLabelValues(string(operation), string(result)).Inc()
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

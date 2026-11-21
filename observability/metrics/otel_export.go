package metrics

import "github.com/prometheus/client_golang/prometheus"

// OtelExportRecorder reports the trace export sender's delivery slots.
type OtelExportRecorder struct {
	slots, capacity prometheus.Gauge
}

func NewOtelExportRecorder(set *Set) *OtelExportRecorder {
	m := &OtelExportRecorder{
		slots: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "otel_export", Name: "slots_used",
			Help: "Trace export slots occupied on this worker, including trace loading, HTTP attempts, and result persistence.",
		}),
		capacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: "otel_export", Name: "capacity",
			Help: "Configured trace export slots on this worker; zero while the sender is stopped.",
		}),
	}
	set.MustRegister(m.slots, m.capacity)
	return m
}

func (m *OtelExportRecorder) RecordCapacity(capacity int) {
	if m != nil {
		m.capacity.Set(float64(capacity))
	}
}

func (m *OtelExportRecorder) StartDelivery() func() {
	if m == nil {
		return func() {}
	}
	m.slots.Inc()
	return m.slots.Dec
}

// OtelExportExpiryRecorder counts trace deliveries that aged out undelivered.
// Maintenance owns it, separately from the sender's recorder on workers.
type OtelExportExpiryRecorder struct {
	expired prometheus.Counter
}

func NewOtelExportExpiryRecorder(set *Set) *OtelExportExpiryRecorder {
	m := &OtelExportExpiryRecorder{
		expired: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: set.Namespace(), Subsystem: "otel_export", Name: "expired_total",
			Help: "Trace delivery rows deleted after their delivery window expired without persisted completion. " +
				"Excludes exporter deletion and counts each cleanup deletion once.",
		}),
	}
	set.MustRegister(m.expired)
	return m
}

func (m *OtelExportExpiryRecorder) RecordExpired(count int64) {
	if m != nil && count > 0 {
		m.expired.Add(float64(count))
	}
}

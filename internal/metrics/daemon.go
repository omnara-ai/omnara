package metrics

import "github.com/prometheus/client_golang/prometheus"

const SubsystemDaemon = "daemon"

type DaemonRecorder struct {
	socketEventsTotal *prometheus.CounterVec
}

func NewDaemonRecorder(set *Set) *DaemonRecorder {
	m := &DaemonRecorder{
		socketEventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara",
			Subsystem: SubsystemDaemon,
			Name:      "socket_events_total",
			Help:      "Total number of daemon websocket transport events.",
		}, []string{"event", "result", "reason"}),
	}
	set.MustRegister(m.socketEventsTotal)
	return m
}

func (m *DaemonRecorder) RecordSocketEvent(event, result, reason string) {
	if m == nil {
		return
	}
	m.socketEventsTotal.WithLabelValues(event, result, reason).Inc()
}

package metrics

import "github.com/prometheus/client_golang/prometheus"

type AgentStreamRecorder struct{ dropped *prometheus.CounterVec }

func NewAgentStreamRecorder(set *Set) *AgentStreamRecorder {
	m := &AgentStreamRecorder{dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: set.Namespace(), Subsystem: "agent_stream", Name: "buffer_dropped_total",
		Help: "Best-effort SSE frames discarded because an application's subscriber buffer is full, across all subscribers.",
	}, []string{"frame"})}
	m.dropped.WithLabelValues("tool_call_update")
	m.dropped.WithLabelValues("model_output_delta")
	set.MustRegister(m.dropped)
	return m
}

func (m *AgentStreamRecorder) RecordDrop(frame string) {
	if m != nil {
		m.dropped.WithLabelValues(frame).Inc()
	}
}

package metrics

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

const SubsystemNotifications = "notifications"

type NotificationRecorder struct {
	notificationsTotal *prometheus.CounterVec
	queueDepth         atomic.Pointer[func() int]
	queueCapacity      prometheus.Gauge
}

func NewNotificationRecorder(set *Set) *NotificationRecorder {
	m := &NotificationRecorder{
		queueCapacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.Namespace(), Subsystem: SubsystemNotifications, Name: "publisher_queue_capacity",
			Help: "Post-commit publisher channel capacity on this process.",
		}),
		notificationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: set.namespace,
			Subsystem: SubsystemNotifications,
			Name:      "total",
			Help:      "Total number of post-commit notification publish outcomes, labeled by intent.",
		}, []string{"intent", "result", "reason"}),
	}
	for _, intent := range []string{
		"daemon_work", "daemon_runtime_ended", "daemon_process_terminate", "agent_event", "tool_call_update",
	} {
		m.notificationsTotal.WithLabelValues(intent, "dropped", "queue_full")
	}
	queueDepth := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: set.Namespace(), Subsystem: SubsystemNotifications, Name: "publisher_queue_depth",
		Help: "Post-commit intents buffered on this process; excludes the batch currently publishing (at most 256).",
	}, func() float64 {
		if read := m.queueDepth.Load(); read != nil {
			return float64((*read)())
		}
		return 0
	})
	set.MustRegister(m.notificationsTotal, m.queueCapacity, queueDepth)
	return m
}

func (m *NotificationRecorder) RecordNotification(intent, result, reason string) {
	if m == nil {
		return
	}
	m.notificationsTotal.WithLabelValues(intent, result, reason).Inc()
}

// RecordPublisherQueue reads only local channel occupancy during scrapes.
func (m *NotificationRecorder) RecordPublisherQueue(capacity int, depth func() int) {
	if m == nil {
		return
	}
	m.queueCapacity.Set(float64(capacity))
	m.queueDepth.Store(&depth)
}

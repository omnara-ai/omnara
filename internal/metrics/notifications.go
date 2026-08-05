package metrics

import "github.com/prometheus/client_golang/prometheus"

const SubsystemNotifications = "notifications"

type NotificationRecorder struct {
	notificationsTotal *prometheus.CounterVec
}

func NewNotificationRecorder(set *Set) *NotificationRecorder {
	m := &NotificationRecorder{
		notificationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara",
			Subsystem: SubsystemNotifications,
			Name:      "total",
			Help:      "Total number of post-commit notification publish outcomes, labeled by intent.",
		}, []string{"intent", "result", "reason"}),
	}
	set.MustRegister(m.notificationsTotal)
	return m
}

func (m *NotificationRecorder) RecordNotification(intent, result, reason string) {
	if m == nil {
		return
	}
	m.notificationsTotal.WithLabelValues(intent, result, reason).Inc()
}

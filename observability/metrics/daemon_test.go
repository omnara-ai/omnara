package metrics

import (
	"strings"
	"testing"
)

func TestDaemonRecorderEmitsSocketEvents(t *testing.T) {
	set := New()
	recorder := NewDaemonRecorder(set)

	recorder.RecordSocketEvent("send_queue", "dropped", "queue_full")
	recorder.RecordSocketEvent("send_queue", "delivered", "none")

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`omnara_daemon_socket_events_total{event="send_queue",reason="queue_full",result="dropped"} 1`,
		`omnara_daemon_socket_events_total{event="send_queue",reason="none",result="delivered"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestNotificationRecorderEmitsMetrics(t *testing.T) {
	set := New()
	recorder := NewNotificationRecorder(set)

	recorder.RecordNotification("daemon_work", "published", "none")
	recorder.RecordNotification("daemon_work", "skipped", "presence_miss")
	recorder.RecordNotification("agent_event", "published", "none")
	recorder.RecordNotification("agent_event", "dropped", "queue_full")

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`omnara_notifications_total{intent="daemon_work",reason="none",result="published"} 1`,
		`omnara_notifications_total{intent="daemon_work",reason="presence_miss",result="skipped"} 1`,
		`omnara_notifications_total{intent="agent_event",reason="none",result="published"} 1`,
		`omnara_notifications_total{intent="agent_event",reason="queue_full",result="dropped"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

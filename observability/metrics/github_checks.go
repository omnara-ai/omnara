package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type GitHubCheckRecorder struct {
	fullPass prometheus.Gauge
	retrying prometheus.Gauge
}

func NewGitHubCheckRecorder(set *Set) *GitHubCheckRecorder {
	m := &GitHubCheckRecorder{
		fullPass: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "github_checks", Name: "full_pass_duration_seconds",
			Help: "Duration of this maintenance replica's last completed pass over every tracked GitHub check.",
		}),
		retrying: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: "github_checks", Name: "retrying",
			Help: "GitHub check updates on this maintenance replica waiting to retry after a failure.",
		}),
	}
	set.MustRegister(m.fullPass, m.retrying)
	return m
}

func (m *GitHubCheckRecorder) Record(fullPass time.Duration, retrying int) {
	if m == nil {
		return
	}
	if fullPass > 0 {
		m.fullPass.Set(fullPass.Seconds())
	}
	m.retrying.Set(float64(retrying))
}

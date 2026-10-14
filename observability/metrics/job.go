package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const SubsystemMaintenance = "maintenance"

type JobResult string

const (
	JobResultSuccess  JobResult = "success"
	JobResultError    JobResult = "error"
	JobResultCanceled JobResult = "canceled"
)

// JobRecorder records runs of periodic background jobs, including the last
// successful run so alerts can detect a job that stopped making progress.
type JobRecorder struct {
	runsTotal   *prometheus.CounterVec
	runDuration *prometheus.HistogramVec
	lastSuccess *prometheus.GaugeVec
	itemsTotal  *prometheus.CounterVec
}

func NewJobRecorder(set *Set, subsystem string) *JobRecorder {
	m := &JobRecorder{
		runsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "job_runs_total",
			Help:      "Total job runs.",
		}, []string{"job", "result"}),
		runDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "job_run_duration_seconds",
			Help:      "Job run duration in seconds.",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"job", "result"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "job_last_success_timestamp_seconds",
			Help:      "Unix time of the most recent successful job run.",
		}, []string{"job"}),
		itemsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "job_items_total",
			Help:      "Total items processed by jobs, labeled by outcome.",
		}, []string{"job", "outcome"}),
	}
	set.MustRegister(m.runsTotal, m.runDuration, m.lastSuccess, m.itemsTotal)
	return m
}

func (m *JobRecorder) RecordRun(job string, result JobResult, duration time.Duration) {
	if m == nil {
		return
	}
	m.runsTotal.WithLabelValues(job, string(result)).Inc()
	m.runDuration.WithLabelValues(job, string(result)).Observe(duration.Seconds())
	if result == JobResultSuccess {
		m.lastSuccess.WithLabelValues(job).SetToCurrentTime()
	}
}

func (m *JobRecorder) RecordItems(job, outcome string, count int64) {
	if m == nil || count <= 0 {
		return
	}
	m.itemsTotal.WithLabelValues(job, outcome).Add(float64(count))
}

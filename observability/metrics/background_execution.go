package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type BackgroundSubmissionResult string

const (
	BackgroundSubmissionAccepted BackgroundSubmissionResult = "accepted"
	BackgroundSubmissionFull     BackgroundSubmissionResult = "full"
	BackgroundSubmissionShutdown BackgroundSubmissionResult = "shutdown"
)

type BackgroundExecutionRecorder struct {
	submissionsInFlight prometheus.Gauge
	submissionsTotal    *prometheus.CounterVec
	submissionDuration  prometheus.Histogram
	startDelay          prometheus.Histogram
}

func NewBackgroundExecutionRecorder(set *Set, capacity int, depth func() int) *BackgroundExecutionRecorder {
	const subsystem = "background_execution"
	buckets := []float64{0.0001, 0.001, 0.01, 0.1, 1, 5, 15, 30, 60, 120, 300, 600}
	m := &BackgroundExecutionRecorder{
		submissionsInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: subsystem, Name: "submissions_in_flight",
			Help: "Submission calls currently in progress, including callers waiting for queue space.",
		}),
		submissionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara", Subsystem: subsystem, Name: "submissions_total",
			Help: "Valid task submission calls by return outcome; accepted does not guarantee execution before shutdown.",
		}, []string{"result"}),
		submissionDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "omnara", Subsystem: subsystem, Name: "submission_duration_seconds",
			Help:    "Time spent submitting tasks, including waiting for queue space; recorded when submission returns.",
			Buckets: buckets,
		}),
		startDelay: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "omnara", Subsystem: subsystem, Name: "start_delay_seconds",
			Help:    "Time from submission to execution start, including admission and queue wait; excludes unstarted tasks.",
			Buckets: buckets,
		}),
	}
	for _, result := range []BackgroundSubmissionResult{
		BackgroundSubmissionAccepted, BackgroundSubmissionFull, BackgroundSubmissionShutdown,
	} {
		m.submissionsTotal.WithLabelValues(string(result))
	}
	set.MustRegister(m.submissionsInFlight, m.submissionsTotal, m.submissionDuration, m.startDelay,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: subsystem, Name: "queue_depth",
			Help: "Tasks currently buffered in the background queue; excludes running tasks and blocked submitters.",
		}, func() float64 { return float64(depth()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "omnara", Subsystem: subsystem, Name: "queue_capacity",
			Help: "Maximum number of tasks buffered in the background queue.",
		}, func() float64 { return float64(capacity) }),
	)
	return m
}

func (m *BackgroundExecutionRecorder) StartSubmission() {
	if m != nil {
		m.submissionsInFlight.Inc()
	}
}

func (m *BackgroundExecutionRecorder) RecordSubmission(result BackgroundSubmissionResult, duration time.Duration) {
	if m != nil {
		m.submissionsInFlight.Dec()
		m.submissionsTotal.WithLabelValues(string(result)).Inc()
		m.submissionDuration.Observe(duration.Seconds())
	}
}

func (m *BackgroundExecutionRecorder) RecordStart(delay time.Duration) {
	if m != nil {
		m.startDelay.Observe(delay.Seconds())
	}
}

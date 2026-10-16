package metrics

import (
	"math"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type WorkerExecutionKind string

const (
	WorkerExecutionKindModel WorkerExecutionKind = "model"
	WorkerExecutionKindTool  WorkerExecutionKind = "tool"
	WorkerExecutionKindOther WorkerExecutionKind = "other"
)

type WorkerExecutionOutcome string

const (
	WorkerExecutionOutcomeSuccess     WorkerExecutionOutcome = "success"
	WorkerExecutionOutcomeRecoverable WorkerExecutionOutcome = "recoverable"
	WorkerExecutionOutcomeCanceled    WorkerExecutionOutcome = "canceled"
	WorkerExecutionOutcomeError       WorkerExecutionOutcome = "error"
)

var workerExecutionBuckets = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800}

type WorkerRecorder struct {
	claimDeferrals    prometheus.Counter
	capacity          prometheus.Gauge
	slotsUsed         atomic.Pointer[func() int]
	executionsActive  prometheus.Gauge
	readyToClaim      prometheus.Histogram
	executionsTotal   *prometheus.CounterVec
	executionDuration *prometheus.HistogramVec
}

func NewWorkerRecorder(set *Set) *WorkerRecorder {
	namespace := set.Namespace()
	recorder := &WorkerRecorder{
		claimDeferrals: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "worker", Name: "claim_deferrals_total",
			Help: "Failed agent claims deferred so other agents can run.",
		}),
		capacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "worker", Name: "capacity",
			Help: "Configured agent work slots on this worker; zero until the worker starts.",
		}),
		executionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "worker", Name: "executions_active",
			Help: "Claimed model/tool work currently executing on this worker. " +
				"Excludes slots reserved while claiming or backing off and retained async tool work.",
		}),
		readyToClaim: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "worker", Name: "ready_to_claim_seconds",
			Help: "DB-clock delay from an agent wakeup becoming claimable to its committed claim. " +
				"Covers claimed work only; use omnara_agent_oldest_ready_age_seconds for work still waiting.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
		}),
		executionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "worker", Name: "executions_total",
			Help: "Claimed agent work executions by kind and outcome.",
		}, []string{"kind", "outcome"}),
		executionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "worker", Name: "execution_duration_seconds",
			Help: "Claimed agent work execution time, including runtime lock renewal and release; " +
				"excludes scheduling delay and retained async tool work.",
			Buckets: workerExecutionBuckets,
		}, []string{"kind", "outcome"}),
	}
	slotsUsed := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "worker", Name: "slots_used",
		Help: "Occupied agent work slots on this worker, including slots reserved while claiming or backing off.",
	}, func() float64 {
		if used := recorder.slotsUsed.Load(); used != nil {
			return float64((*used)())
		}
		return 0
	})
	set.MustRegister(recorder.claimDeferrals, recorder.capacity, slotsUsed, recorder.executionsActive,
		recorder.readyToClaim, recorder.executionsTotal, recorder.executionDuration)
	return recorder
}

func (r *WorkerRecorder) RecordClaimDeferral() {
	if r != nil {
		r.claimDeferrals.Inc()
	}
}

// RecordSlots publishes the worker's configured capacity and reads slot
// occupancy from slotsUsed on each scrape.
func (r *WorkerRecorder) RecordSlots(capacity int, slotsUsed func() int) {
	if r == nil {
		return
	}
	r.capacity.Set(float64(capacity))
	r.slotsUsed.Store(&slotsUsed)
}

func (r *WorkerRecorder) RecordReadyToClaim(readyAt, claimedAt time.Time) {
	if r == nil || readyAt.IsZero() || claimedAt.IsZero() {
		return
	}
	r.readyToClaim.Observe(math.Max(claimedAt.Sub(readyAt).Seconds(), 0))
}

// StartExecution marks claimed work as executing until the returned function
// records its outcome. Call the returned function exactly once.
func (r *WorkerRecorder) StartExecution(kind WorkerExecutionKind) func(WorkerExecutionOutcome) {
	if r == nil {
		return func(WorkerExecutionOutcome) {}
	}
	switch kind {
	case WorkerExecutionKindModel, WorkerExecutionKindTool:
	default:
		kind = WorkerExecutionKindOther
	}
	started := time.Now()
	r.executionsActive.Inc()
	return func(outcome WorkerExecutionOutcome) {
		r.executionsActive.Dec()
		switch outcome {
		case WorkerExecutionOutcomeSuccess, WorkerExecutionOutcomeRecoverable,
			WorkerExecutionOutcomeCanceled, WorkerExecutionOutcomeError:
		default:
			outcome = WorkerExecutionOutcomeError
		}
		r.executionsTotal.WithLabelValues(string(kind), string(outcome)).Inc()
		r.executionDuration.WithLabelValues(string(kind), string(outcome)).Observe(time.Since(started).Seconds())
	}
}

type AgentBacklogSample struct {
	Ready          int64
	OldestReadyAge time.Duration
}

type AgentBacklogRecorder struct {
	ready          prometheus.Gauge
	oldestReadyAge prometheus.Gauge
	lastSuccess    prometheus.Gauge
}

func NewAgentBacklogRecorder(set *Set) *AgentBacklogRecorder {
	namespace := set.Namespace()
	m := &AgentBacklogRecorder{
		ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "agent", Name: "ready_backlog",
			Help: "Cluster-wide due agent wakeups for non-archived agents without a runtime lock. " +
				"Sampled by maintenance; use max across replicas, never sum. NaN before a sample or on failure.",
		}),
		oldestReadyAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "agent", Name: "oldest_ready_age_seconds",
			Help: "Age of the oldest claimable agent wakeup since it became claimable. " +
				"Zero when empty; NaN before a sample or on failure.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "agent", Name: "backlog_sample_last_success_timestamp_seconds",
			Help: "Unix time of the last successful agent backlog sample, including empty results; zero before success.",
		}),
	}
	m.ready.Set(math.NaN())
	m.oldestReadyAge.Set(math.NaN())
	set.MustRegister(m.ready, m.oldestReadyAge, m.lastSuccess)
	return m
}

func (m *AgentBacklogRecorder) Record(sample AgentBacklogSample, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.ready.Set(math.NaN())
		m.oldestReadyAge.Set(math.NaN())
		return
	}
	m.ready.Set(float64(sample.Ready))
	m.oldestReadyAge.Set(math.Max(sample.OldestReadyAge.Seconds(), 0))
	m.lastSuccess.SetToCurrentTime()
}

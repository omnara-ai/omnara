package metrics

import "github.com/prometheus/client_golang/prometheus"

type WorkerRecorder struct {
	claimDeferrals prometheus.Counter
}

func NewWorkerRecorder(set *Set) *WorkerRecorder {
	recorder := &WorkerRecorder{claimDeferrals: prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: set.Namespace(), Subsystem: "worker", Name: "claim_deferrals_total",
		Help: "Failed agent claims deferred so other agents can run.",
	})}
	set.MustRegister(recorder.claimDeferrals)
	return recorder
}

func (r *WorkerRecorder) RecordClaimDeferral() {
	if r != nil {
		r.claimDeferrals.Inc()
	}
}

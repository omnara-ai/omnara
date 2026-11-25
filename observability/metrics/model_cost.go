package metrics

import (
	"math"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

type ModelCostBacklogRecorder struct {
	pending     prometheus.Gauge
	lastSuccess prometheus.Gauge
}

func NewModelCostBacklogRecorder(set *Set, ceiling int64) *ModelCostBacklogRecorder {
	namespace := set.Namespace()
	m := &ModelCostBacklogRecorder{
		pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "model_cost", Name: "merge_backlog",
			Help: "Finished model calls whose cost is not yet merged into daily spend totals, counted up to " +
				strconv.FormatInt(ceiling, 10) + ". Sampled by maintenance; use max across replicas, never sum. " +
				"NaN before a sample or on failure.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "model_cost", Name: "merge_backlog_sample_last_success_timestamp_seconds",
			Help: "Unix time of the last successful model cost merge backlog sample; zero before success.",
		}),
	}
	m.pending.Set(math.NaN())
	set.MustRegister(m.pending, m.lastSuccess)
	return m
}

func (m *ModelCostBacklogRecorder) Record(pending int64, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.pending.Set(math.NaN())
		return
	}
	m.pending.Set(float64(pending))
	m.lastSuccess.SetToCurrentTime()
}

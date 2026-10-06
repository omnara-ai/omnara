package metrics

import "github.com/prometheus/client_golang/prometheus"

const SubsystemMemory = "memory"

type MemoryRecorder struct {
	listingDuration prometheus.Histogram
}

func NewMemoryRecorder(set *Set) *MemoryRecorder {
	m := &MemoryRecorder{
		listingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "omnara",
			Subsystem: SubsystemMemory,
			Name:      "listing_duration_seconds",
			Help:      "Memory file listing duration in seconds.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}),
	}
	set.MustRegister(m.listingDuration)
	return m
}

func (m *MemoryRecorder) StartListing() *prometheus.Timer {
	var observer prometheus.Observer
	if m != nil {
		observer = m.listingDuration
	}
	return prometheus.NewTimer(observer)
}

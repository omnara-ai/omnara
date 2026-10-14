package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type HTTPRecorder struct {
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	inFlight        prometheus.Gauge
}

func NewHTTPRecorder(set *Set, subsystem string) *HTTPRecorder {
	m := &HTTPRecorder{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "requests_total",
			Help:      "Total number of HTTP requests.",
		}, []string{"method", "route", "code"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route", "code"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "requests_in_flight",
			Help:      "Current number of HTTP requests being served.",
		}),
	}
	set.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.inFlight,
	)
	return m
}

// Start counts a request in flight and returns the function that records it
// once the matched route pattern and final status are known.
func (m *HTTPRecorder) Start() func(method, pattern string, status int) {
	if m == nil {
		return func(string, string, int) {}
	}
	started := time.Now()
	m.inFlight.Inc()
	return func(method, pattern string, status int) {
		m.inFlight.Dec()
		route := "unmatched"
		if pattern != "" {
			route = routePathPattern(pattern)
		}
		labels := []string{httpMethodLabel(method), route, strconv.Itoa(status)}
		m.requestsTotal.WithLabelValues(labels...).Inc()
		m.requestDuration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
	}
}

// httpMethodLabel collapses nonstandard methods so clients cannot grow the
// series.
func httpMethodLabel(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodPut, http.MethodHead, http.MethodPost, http.MethodDelete,
		http.MethodConnect, http.MethodOptions, "NOTIFY", http.MethodTrace, http.MethodPatch:
		return strings.ToLower(method)
	default:
		return "unknown"
	}
}

func routePathPattern(pattern string) string {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return pattern
	}
	switch method {
	case http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions:
		return path
	default:
		return pattern
	}
}

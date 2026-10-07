package metrics

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type routeContextKey struct{}
type statusContextKey struct{}

type httpRequestStatus struct {
	code int
}

func SetHTTPRequestStatusCode(ctx context.Context, status int) {
	requestStatus, _ := ctx.Value(statusContextKey{}).(*httpRequestStatus)
	if requestStatus != nil {
		requestStatus.code = status
	}
}

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

func (m *HTTPRecorder) Middleware(mux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		routeLabel := promhttp.WithLabelFromCtx("route", func(ctx context.Context) string {
			route, _ := ctx.Value(routeContextKey{}).(string)
			return route
		})
		statusLabel := promhttp.WithLabelFromCtx("code", func(ctx context.Context) string {
			requestStatus, _ := ctx.Value(statusContextKey{}).(*httpRequestStatus)
			if requestStatus == nil || requestStatus.code == 0 {
				return "unknown"
			}
			return strconv.Itoa(requestStatus.code)
		})
		instrumented := promhttp.InstrumentHandlerInFlight(m.inFlight,
			promhttp.InstrumentHandlerCounter(m.requestsTotal,
				promhttp.InstrumentHandlerDuration(m.requestDuration, next, routeLabel, statusLabel),
				routeLabel,
				statusLabel,
			),
		)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == ScrapePath {
				next.ServeHTTP(w, r)
				return
			}
			requestStatus := &httpRequestStatus{}
			ctx := context.WithValue(r.Context(), routeContextKey{}, routePattern(mux, r))
			ctx = context.WithValue(ctx, statusContextKey{}, requestStatus)
			instrumented.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Start counts a request in flight and returns the function that records it
// once the matched route pattern and final status are known. It is the
// alternative to Middleware for servers that already wrap the response writer
// and read the route from the routed request's Pattern.
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

// httpMethodLabel matches promhttp's method label so Start and Middleware
// report the same series.
func httpMethodLabel(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodPut, http.MethodHead, http.MethodPost, http.MethodDelete,
		http.MethodConnect, http.MethodOptions, "NOTIFY", http.MethodTrace, http.MethodPatch:
		return strings.ToLower(method)
	default:
		return "unknown"
	}
}

func routePattern(mux *http.ServeMux, r *http.Request) string {
	if r.Pattern != "" {
		return routePathPattern(r.Pattern)
	}
	_, pattern := mux.Handler(r)
	if pattern != "" {
		return routePathPattern(pattern)
	}
	return "unmatched"
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

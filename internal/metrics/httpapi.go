package metrics

import (
	"context"
	"net/http"
	"strconv"
	"strings"

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
			Namespace: "omnara",
			Subsystem: subsystem,
			Name:      "requests_total",
			Help:      "Total number of HTTP requests.",
		}, []string{"method", "route", "code"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omnara",
			Subsystem: subsystem,
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route", "code"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omnara",
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

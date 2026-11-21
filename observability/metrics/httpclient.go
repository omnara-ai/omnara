package metrics

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	log "github.com/omnara-ai/omnara/observability/wideevent"
)

type HTTPClientRecorder struct {
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	now             func() time.Time
}

type HTTPClientObserverOption func(*clientObserver)

type clientObserver struct {
	recorder  *HTTPClientRecorder
	hostLabel string
	pathLabel string
}

type HTTPRequestData struct {
	Method     string
	Host       string
	Path       string
	StatusCode int
	ErrorKind  string
	Duration   time.Duration
}

func WithHTTPClientHostLabel(host string) HTTPClientObserverOption {
	return func(observer *clientObserver) {
		observer.hostLabel = host
	}
}

func WithHTTPClientPathLabel(path string) HTTPClientObserverOption {
	return func(observer *clientObserver) {
		observer.pathLabel = normalizeHTTPClientPathLabel(path)
	}
}

func NewHTTPClientRecorder(set *Set, subsystem string) *HTTPClientRecorder {
	m := &HTTPClientRecorder{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "requests_total",
			Help:      "Total number of outbound HTTP requests.",
		}, []string{"host", "path", "method", "code", "result", "error_kind"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: set.namespace,
			Subsystem: subsystem,
			Name:      "request_duration_seconds",
			Help:      "Outbound HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"host", "path", "method", "code", "result", "error_kind"}),
		now: time.Now,
	}
	set.MustRegister(m.requestsTotal, m.requestDuration)
	return m
}

func NewObservedHTTPClient(
	base *http.Client,
	recorder *HTTPClientRecorder,
	opts ...HTTPClientObserverOption,
) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	next := *base
	next.Transport = NewObservedHTTPTransport(base.Transport, recorder, opts...)
	return &next
}

func NewObservedHTTPTransport(
	base http.RoundTripper,
	recorder *HTTPClientRecorder,
	opts ...HTTPClientObserverOption,
) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if recorder == nil {
		return base
	}
	return observedHTTPTransport{base: base, observer: newClientObserver(recorder, opts)}
}

type observedHTTPTransport struct {
	base     http.RoundTripper
	observer clientObserver
}

func (rt observedHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := rt.observer.now()
	resp, err := rt.base.RoundTrip(req)
	rt.observer.recordRoundTrip(req, start, resp, err)
	return resp, err
}

func (rt observedHTTPTransport) CloseIdleConnections() {
	if transport, ok := rt.base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

func newClientObserver(recorder *HTTPClientRecorder, opts []HTTPClientObserverOption) clientObserver {
	observer := clientObserver{recorder: recorder}
	for _, opt := range opts {
		opt(&observer)
	}
	return observer
}

func (o clientObserver) now() time.Time {
	if o.recorder == nil {
		return time.Time{}
	}
	return o.recorder.now()
}

func (o clientObserver) recordRoundTrip(req *http.Request, start time.Time, resp *http.Response, err error) {
	if start.IsZero() {
		return
	}
	host, path := httpRequestHostPath(req, o.hostLabel, o.pathLabel)
	request := HTTPRequestData{
		Method: req.Method,
		Host:   host,
		Path:   path,
	}
	if resp != nil {
		request.StatusCode = resp.StatusCode
	}
	if err != nil {
		request.ErrorKind = httpRequestErrorKind(err)
	}
	o.recorder.recordCall(req.Context(), request, start, err)
}

func (m *HTTPClientRecorder) recordCall(ctx context.Context, request HTTPRequestData, start time.Time, err error) {
	request.Duration = m.now().Sub(start)
	var errText string
	if err != nil {
		errText = truncate(log.ScrubLogString(err.Error()), 256)
	}
	log.AttachHTTPRequest(ctx, log.HTTPRequestTraceRecord{
		Method:     request.Method,
		Host:       request.Host,
		Path:       request.Path,
		StatusCode: request.StatusCode,
		Start:      start,
		Duration:   request.Duration,
		Error:      errText,
	})
	m.RecordRequest(request)
}

func (m *HTTPClientRecorder) RecordRequest(request HTTPRequestData) {
	code, result, errorKind := httpRequestResult(request.StatusCode, request.ErrorKind)
	labels := []string{request.Host, request.Path, request.Method, code, result, errorKind}
	m.requestsTotal.WithLabelValues(labels...).Inc()
	m.requestDuration.WithLabelValues(labels...).Observe(request.Duration.Seconds())
}

func httpRequestHostPath(req *http.Request, hostLabel, pathLabel string) (host, path string) {
	if req == nil {
		return "", "/*"
	}
	if req.URL != nil {
		host = req.URL.Host
	}
	if hostLabel != "" {
		host = hostLabel
	}
	if pathLabel != "" {
		return host, pathLabel
	}
	return host, "/*"
}

func normalizeHTTPClientPathLabel(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func httpRequestResult(statusCode int, errorKind string) (code, result, kind string) {
	if errorKind != "" {
		return "none", "error", errorKind
	}
	code = strconv.Itoa(statusCode)
	switch {
	case statusCode >= 500:
		return code, "error", "http_5xx"
	case statusCode >= 400:
		return code, "error", "http_4xx"
	default:
		return code, "success", "none"
	}
}

func httpRequestErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline_exceeded"
	default:
		return "transport"
	}
}

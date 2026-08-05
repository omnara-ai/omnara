package metrics

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/omnara-ai/omnara/internal/log"
)

type HTTPClientRecorder struct {
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	now             func() time.Time
}

type HTTPClientObserverOption func(*httpClientObserverOptions)

type httpClientObserverOptions struct {
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

func WithHTTPClientPathLabel(path string) HTTPClientObserverOption {
	return func(opts *httpClientObserverOptions) {
		opts.pathLabel = normalizeHTTPClientPathLabel(path)
	}
}

func NewHTTPClientRecorder(set *Set, subsystem string) *HTTPClientRecorder {
	m := &HTTPClientRecorder{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omnara",
			Subsystem: subsystem,
			Name:      "requests_total",
			Help:      "Total number of outbound HTTP requests.",
		}, []string{"host", "path", "method", "code", "result", "error_kind"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omnara",
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
	options := httpClientObserverOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	return observedHTTPTransport{base: base, recorder: recorder, pathLabel: options.pathLabel}
}

type observedHTTPTransport struct {
	base      http.RoundTripper
	recorder  *HTTPClientRecorder
	pathLabel string
}

func (rt observedHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := rt.recorder.now()
	resp, err := rt.base.RoundTrip(req)
	rt.recorder.RecordRoundTrip(req, rt.pathLabel, start, resp, err)
	return resp, err
}

func (m *HTTPClientRecorder) RecordRoundTrip(
	req *http.Request,
	pathLabel string,
	start time.Time,
	resp *http.Response,
	err error,
) {
	if start.IsZero() {
		return
	}
	host, path := httpRequestHostPath(req, pathLabel)
	duration := m.now().Sub(start)
	var errText string
	request := HTTPRequestData{
		Method:   req.Method,
		Host:     host,
		Path:     path,
		Duration: duration,
	}
	if resp != nil {
		request.StatusCode = resp.StatusCode
	}
	if err != nil {
		errText = truncate(log.ScrubLogString(err.Error()), 256)
		request.ErrorKind = httpRequestErrorKind(err)
	}
	log.AttachHTTPRequest(req.Context(), log.HTTPRequestTraceRecord{
		Method:     request.Method,
		Host:       request.Host,
		Path:       request.Path,
		StatusCode: request.StatusCode,
		Start:      start,
		Duration:   duration,
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

func httpRequestHostPath(req *http.Request, pathLabel string) (host, path string) {
	if req == nil {
		return "", "/*"
	}
	if req.URL != nil {
		host = req.URL.Host
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

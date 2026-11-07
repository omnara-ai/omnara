package metrics

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const SubsystemModelCall = "model_call"

// ModelCallSample is one model provider attempt. Nil timings mean the phase
// was never reached and are not observed. Labels carry no call or request IDs.
type ModelCallSample struct {
	APIFormat  string
	APIVariant string
	Model      string
	Purpose    string
	Outcome    string
	ErrorKind  string
	Cache      string

	RequestBytes      int
	UsageReported     bool
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	OutputTokens      int
	ReasoningTokens   int

	UploadComplete *time.Duration
	Headers        *time.Duration
	FirstContent   *time.Duration
	FirstText      *time.Duration
	StreamComplete *time.Duration
	Duration       time.Duration
}

type ModelCallRecorder struct {
	attemptsTotal    *prometheus.CounterVec
	phaseSeconds     *prometheus.HistogramVec
	ttftByTokens     *prometheus.HistogramVec
	ttftByBytes      *prometheus.HistogramVec
	requestBytes     *prometheus.HistogramVec
	inputTokens      *prometheus.HistogramVec
	tokensTotal      *prometheus.CounterVec
	usageMissedTotal *prometheus.CounterVec
}

var modelCallLatencyBuckets = []float64{0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8, 13, 21, 34, 60, 120}

// Inclusive upper bounds, in units of 1024, for the size labels on the TTFT
// histograms. Graphing TTFT against size needs size as a label because
// Prometheus cannot correlate two histograms observation by observation.
// Labels are zero-padded ("0016k", "00256KiB", then "inf") so they sort in
// size order as strings.
var (
	modelCallInputTokenBounds  = sizeBounds{units: []int{4, 16, 32, 64, 128}, format: "%04dk"}
	modelCallRequestByteBounds = sizeBounds{units: []int{16, 64, 256, 1024, 4096}, format: "%05dKiB"}
)

func NewModelCallRecorder(set *Set) *ModelCallRecorder {
	namespace := set.Namespace()
	model := []string{"api_format", "api_variant", "model"}
	m := &ModelCallRecorder{
		attemptsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "attempts_total",
			Help: "Model provider attempts, including retries and failures.",
		}, append(model, "purpose", "outcome", "error_kind")),
		phaseSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "phase_seconds",
			Help: "Seconds from request start to each attempt phase: upload_complete, headers, " +
				"first_content, first_text, stream_complete, and end. Unreached phases are not observed.",
			Buckets: modelCallLatencyBuckets,
		}, append(model, "cache", "phase")),
		ttftByTokens: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "ttft_by_input_tokens_seconds",
			Help: "Seconds from request start to the first nonempty text, reasoning, or tool-argument " +
				"content, by provider-reported input tokens (upper bound; unknown without usage).",
			Buckets: modelCallLatencyBuckets,
		}, append(model, "cache", "input_tokens_le")),
		ttftByBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "ttft_by_request_bytes_seconds",
			Help:    "Seconds from request start to first nonempty content, by request body bytes (upper bound).",
			Buckets: modelCallLatencyBuckets,
		}, append(model, "cache", "request_bytes_le")),
		requestBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "request_bytes",
			Help:    "Model provider request body bytes per attempt.",
			Buckets: prometheus.ExponentialBuckets(4*1024, 2, 13),
		}, model),
		inputTokens: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "input_tokens",
			Help:    "Provider-reported input tokens per attempt, including cached tokens.",
			Buckets: prometheus.ExponentialBuckets(1024, 2, 10),
		}, append(model, "cache")),
		tokensTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "tokens_total",
			Help: "Provider-reported tokens by kind: input (all input), cached_input, " +
				"cache_write_input, output (includes reasoning), reasoning.",
		}, append(model, "kind")),
		usageMissedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: "usage_missing_total",
			Help: "Attempts that ended without provider-reported usage.",
		}, append(model, "outcome")),
	}
	set.MustRegister(
		m.attemptsTotal, m.phaseSeconds, m.ttftByTokens, m.ttftByBytes,
		m.requestBytes, m.inputTokens, m.tokensTotal, m.usageMissedTotal,
	)
	return m
}

func (m *ModelCallRecorder) Observe(s ModelCallSample) {
	if m == nil {
		return
	}
	model := []string{labelOr(s.APIFormat), labelOr(s.APIVariant), labelOr(s.Model)}
	cache := labelOr(s.Cache)
	m.attemptsTotal.WithLabelValues(append(model, labelOr(s.Purpose), labelOr(s.Outcome), s.ErrorKind)...).Inc()
	for _, phase := range []struct {
		name  string
		value *time.Duration
	}{
		{"upload_complete", s.UploadComplete},
		{"headers", s.Headers},
		{"first_content", s.FirstContent},
		{"first_text", s.FirstText},
		{"stream_complete", s.StreamComplete},
		{"end", &s.Duration},
	} {
		if phase.value != nil {
			m.phaseSeconds.WithLabelValues(append(model, cache, phase.name)...).Observe(phase.value.Seconds())
		}
	}
	if s.FirstContent != nil {
		tokens := "unknown"
		if s.UsageReported && s.InputTokens > 0 {
			tokens = sizeBucket(s.InputTokens, modelCallInputTokenBounds)
		}
		m.ttftByTokens.WithLabelValues(append(model, cache, tokens)...).Observe(s.FirstContent.Seconds())
		m.ttftByBytes.WithLabelValues(append(model, cache, sizeBucket(s.RequestBytes, modelCallRequestByteBounds))...).
			Observe(s.FirstContent.Seconds())
	}
	m.requestBytes.WithLabelValues(model...).Observe(float64(s.RequestBytes))
	if !s.UsageReported {
		m.usageMissedTotal.WithLabelValues(append(model, labelOr(s.Outcome))...).Inc()
		return
	}
	m.inputTokens.WithLabelValues(append(model, cache)...).Observe(float64(s.InputTokens))
	for kind, count := range map[string]int{
		"input":             s.InputTokens,
		"cached_input":      s.CachedInputTokens,
		"cache_write_input": s.CacheWriteTokens,
		"output":            s.OutputTokens,
		"reasoning":         s.ReasoningTokens,
	} {
		if count > 0 {
			m.tokensTotal.WithLabelValues(append(model, kind)...).Add(float64(count))
		}
	}
}

type sizeBounds struct {
	units  []int
	format string
}

func sizeBucket(value int, bounds sizeBounds) string {
	for _, unit := range bounds.units {
		if value <= unit*1024 {
			return fmt.Sprintf(bounds.format, unit)
		}
	}
	return "inf"
}

func labelOr(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

package metrics

import (
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const SubsystemModelCall = "model_call"

// ModelCallSample is one model provider attempt. A nil TTFT means no token was
// streamed and is not observed. Labels carry no call or request IDs.
type ModelCallSample struct {
	APIFormat  string
	APIVariant string
	Provider   string
	Model      string
	Purpose    string
	Outcome    string
	ErrorKind  string

	RequestBytes      int
	UsageReported     bool
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	OutputTokens      int
	ReasoningTokens   int

	TTFT     *time.Duration
	Duration time.Duration
}

type ModelCallRecorder struct {
	attemptsTotal          *prometheus.CounterVec
	ttftSeconds            *prometheus.HistogramVec
	durationSeconds        *prometheus.HistogramVec
	requestBytesTotal      *prometheus.CounterVec
	inputTokensTotal       *prometheus.CounterVec
	cachedInputTokensTotal *prometheus.CounterVec
	cacheWriteTokensTotal  *prometheus.CounterVec
	outputTokensTotal      *prometheus.CounterVec
	reasoningTokensTotal   *prometheus.CounterVec
	usageMissingTotal      *prometheus.CounterVec
}

var modelCallLatencyBuckets = []float64{0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8, 13, 21, 34, 60, 120}

// NewModelCallRecorder registers per-attempt model call series. Averages come
// from dividing a counter by attempts_total (or by attempts that reported
// usage, attempts_total minus usage_missing_total, for tokens); the cache hit
// ratio is cached_input_tokens_total over input_tokens_total. The provider
// label is the endpoint host, which separates providers that share an API
// variant.
func NewModelCallRecorder(set *Set) *ModelCallRecorder {
	namespace := set.Namespace()
	model := []string{"api_format", "api_variant", "provider", "model"}
	counter := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: name, Help: help,
		}, append(slices.Clone(model), labels...))
	}
	histogram := func(name, help string) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: SubsystemModelCall, Name: name, Help: help,
			Buckets: modelCallLatencyBuckets,
		}, model)
	}
	m := &ModelCallRecorder{
		attemptsTotal: counter("attempts_total",
			"Model provider attempts, including retries and failures.", "purpose", "outcome", "error_kind"),
		ttftSeconds: histogram("ttft_seconds",
			"Seconds from request start to the first streamed token. Attempts without one are not observed."),
		durationSeconds:   histogram("duration_seconds", "Seconds from request start to the end of the attempt."),
		requestBytesTotal: counter("request_bytes_total", "Model provider request body bytes."),
		inputTokensTotal: counter("input_tokens_total",
			"Provider-reported input tokens, including cached and cache-write tokens."),
		cachedInputTokensTotal: counter("cached_input_tokens_total", "Provider-reported cache-read input tokens."),
		cacheWriteTokensTotal:  counter("cache_write_input_tokens_total", "Provider-reported cache-write input tokens."),
		outputTokensTotal: counter("output_tokens_total",
			"Provider-reported output tokens, including reasoning tokens."),
		reasoningTokensTotal: counter("reasoning_tokens_total", "Provider-reported reasoning output tokens."),
		usageMissingTotal: counter("usage_missing_total",
			"Attempts that ended without provider-reported usage."),
	}
	set.MustRegister(
		m.attemptsTotal, m.ttftSeconds, m.durationSeconds, m.requestBytesTotal,
		m.inputTokensTotal, m.cachedInputTokensTotal, m.cacheWriteTokensTotal,
		m.outputTokensTotal, m.reasoningTokensTotal, m.usageMissingTotal,
	)
	return m
}

func (m *ModelCallRecorder) Observe(s ModelCallSample) {
	if m == nil {
		return
	}
	model := []string{labelOr(s.APIFormat), labelOr(s.APIVariant), labelOr(s.Provider), labelOr(s.Model)}
	m.attemptsTotal.WithLabelValues(append(model, labelOr(s.Purpose), labelOr(s.Outcome), s.ErrorKind)...).Inc()
	if s.TTFT != nil {
		m.ttftSeconds.WithLabelValues(model...).Observe(s.TTFT.Seconds())
	}
	m.durationSeconds.WithLabelValues(model...).Observe(s.Duration.Seconds())
	m.requestBytesTotal.WithLabelValues(model...).Add(float64(s.RequestBytes))
	if !s.UsageReported {
		m.usageMissingTotal.WithLabelValues(model...).Inc()
		return
	}
	m.inputTokensTotal.WithLabelValues(model...).Add(float64(s.InputTokens))
	m.cachedInputTokensTotal.WithLabelValues(model...).Add(float64(s.CachedInputTokens))
	m.cacheWriteTokensTotal.WithLabelValues(model...).Add(float64(s.CacheWriteTokens))
	m.outputTokensTotal.WithLabelValues(model...).Add(float64(s.OutputTokens))
	m.reasoningTokensTotal.WithLabelValues(model...).Add(float64(s.ReasoningTokens))
}

func labelOr(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// Package calltrace records one structured observation per model provider
// attempt: request size, token usage, outcome, and the latency of each phase
// from request start to stream completion. It never holds prompt or response
// content, headers, or credentials.
package calltrace

import (
	"context"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/observability/metrics"
)

type Purpose string

const (
	PurposeAgentTurn  Purpose = "agent_turn"
	PurposeCompaction Purpose = "compaction"
	PurposeUnknown    Purpose = "unknown"
)

// Scope identifies the logical model call that an attempt belongs to. A retry
// is a new attempt with its own record.
type Scope struct {
	Purpose            Purpose
	ModelCallContextID string
	Attempt            int
}

type scopeKey struct{}

func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

func ScopeFromContext(ctx context.Context) Scope {
	scope, _ := ctx.Value(scopeKey{}).(Scope)
	if scope.Purpose == "" {
		scope.Purpose = PurposeUnknown
	}
	return scope
}

type recorderKey struct{}

// WithRecorder makes model calls under ctx report to recorder.
func WithRecorder(ctx context.Context, recorder *metrics.ModelCallRecorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderKey{}, recorder)
}

func RecorderFromContext(ctx context.Context) *metrics.ModelCallRecorder {
	recorder, _ := ctx.Value(recorderKey{}).(*metrics.ModelCallRecorder)
	return recorder
}

type Outcome string

const (
	OutcomeSuccess  Outcome = "success"
	OutcomeError    Outcome = "error"
	OutcomeCanceled Outcome = "canceled"
)

// TokenKind is the kind of model output that carried the first token.
type TokenKind string

const (
	TokenText      TokenKind = "text"
	TokenReasoning TokenKind = "reasoning"
	TokenToolCall  TokenKind = "tool_call"
)

// Usage holds provider-reported token counts. InputTokens includes cached
// and cache-write input tokens.
type Usage struct {
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	OutputTokens      int
	ReasoningTokens   int
}

// Record is one provider attempt. Timings are offsets from StartedAt; a nil
// timing means the phase was never reached. TTFT is the offset of the first
// stream frame that carries model-generated output; it stays nil for a
// non-streamed body, where every token arrives at once.
type Record struct {
	CallID            string
	Scope             Scope
	APIFormat         string
	APIVariant        string
	Model             string
	ServedModel       string
	EndpointHost      string
	ProviderRequestID string
	Streamed          bool
	StatusCode        int
	Outcome           Outcome
	ErrorKind         string
	ErrorCode         string
	RequestBytes      int
	Usage             *Usage

	StartedAt      time.Time
	UploadComplete *time.Duration
	Headers        *time.Duration
	TTFT           *time.Duration
	FirstTokenKind TokenKind
	StreamComplete *time.Duration
	Duration       time.Duration
}

// CacheRatio is the share of input tokens read from the provider's prompt
// cache. It is unknown without reported input tokens.
func (r Record) CacheRatio() (float64, bool) {
	if r.Usage == nil || r.Usage.InputTokens <= 0 {
		return 0, false
	}
	return float64(r.Usage.CachedInputTokens) / float64(r.Usage.InputTokens), true
}

func (r Record) MetricsSample() metrics.ModelCallSample {
	sample := metrics.ModelCallSample{
		APIFormat:    r.APIFormat,
		APIVariant:   r.APIVariant,
		Provider:     r.EndpointHost,
		Model:        r.Model,
		Purpose:      string(r.Scope.Purpose),
		Outcome:      string(r.Outcome),
		ErrorKind:    r.ErrorKind,
		RequestBytes: r.RequestBytes,
		TTFT:         r.TTFT,
		Duration:     r.Duration,
	}
	if r.Usage != nil {
		sample.UsageReported = true
		sample.InputTokens = r.Usage.InputTokens
		sample.CachedInputTokens = r.Usage.CachedInputTokens
		sample.CacheWriteTokens = r.Usage.CacheWriteTokens
		sample.OutputTokens = r.Usage.OutputTokens
		sample.ReasoningTokens = r.Usage.ReasoningTokens
	}
	return sample
}

// Tracker captures phase timings for one attempt. Upload completion arrives on
// the HTTP transport's goroutine, so every mark is guarded.
type Tracker struct {
	mu      sync.Mutex
	now     func() time.Time
	started time.Time

	upload     *time.Duration
	headers    *time.Duration
	firstToken *time.Duration
	tokenKind  TokenKind
	complete   *time.Duration
}

func Start(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{now: now, started: now()}
}

func (t *Tracker) StartedAt() time.Time {
	return t.started
}

func (t *Tracker) elapsed() *time.Duration {
	d := t.now().Sub(t.started)
	return &d
}

// WithClientTrace marks upload completion when the transport finishes writing
// the request. A transport that rewrites the request keeps the last write.
func (t *Tracker) WithClientTrace(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				return
			}
			t.mu.Lock()
			t.upload = t.elapsed()
			t.mu.Unlock()
		},
	})
}

func (t *Tracker) MarkHeaders() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.headers == nil {
		t.headers = t.elapsed()
	}
}

// MarkToken records the first token: the first frame whose payload is model
// output of any kind, whitespace included. An empty payload is stream
// metadata (role, heartbeat, block markers) and does not count.
func (t *Tracker) MarkToken(kind TokenKind, payload string) {
	if payload == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstToken == nil {
		t.firstToken = t.elapsed()
		t.tokenKind = kind
	}
}

func (t *Tracker) MarkComplete() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.complete == nil {
		t.complete = t.elapsed()
	}
}

// Fill copies the captured timings into record and stamps its total duration.
func (t *Tracker) Fill(record *Record) {
	t.mu.Lock()
	defer t.mu.Unlock()
	record.StartedAt = t.started
	record.UploadComplete = t.upload
	record.Headers = t.headers
	record.TTFT = t.firstToken
	record.FirstTokenKind = t.tokenKind
	record.StreamComplete = t.complete
	record.Duration = *t.elapsed()
}

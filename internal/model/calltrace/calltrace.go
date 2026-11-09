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

type Usage struct {
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	CacheReported     bool
	OutputTokens      int
	ReasoningTokens   int
}

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
	StreamComplete *time.Duration
	Duration       time.Duration
}

func (r Record) CacheRatio() (float64, bool) {
	if r.Usage == nil || !r.Usage.CacheReported || r.Usage.InputTokens <= 0 {
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
		sample.CacheReported = r.Usage.CacheReported
		sample.OutputTokens = r.Usage.OutputTokens
		sample.ReasoningTokens = r.Usage.ReasoningTokens
	}
	return sample
}

type trackerKey struct{}

func WithTracker(ctx context.Context, tracker *Tracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, tracker)
}

func MarkToken(ctx context.Context, payload string) {
	if tracker, ok := ctx.Value(trackerKey{}).(*Tracker); ok {
		tracker.MarkToken(payload)
	}
}

type Tracker struct {
	// Upload completion arrives on the transport's goroutine.
	mu      sync.Mutex
	now     func() time.Time
	started time.Time

	upload     *time.Duration
	headers    *time.Duration
	firstToken *time.Duration
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

func (t *Tracker) MarkToken(payload string) {
	if payload == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstToken == nil {
		t.firstToken = t.elapsed()
	}
}

func (t *Tracker) MarkComplete() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.complete == nil {
		t.complete = t.elapsed()
	}
}

func (t *Tracker) Fill(record *Record) {
	t.mu.Lock()
	defer t.mu.Unlock()
	record.StartedAt = t.started
	record.UploadComplete = t.upload
	record.Headers = t.headers
	record.TTFT = t.firstToken
	record.StreamComplete = t.complete
	record.Duration = *t.elapsed()
}

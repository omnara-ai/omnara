package calltrace

import (
	"context"
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

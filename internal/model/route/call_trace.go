package route

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/observability/metrics"
)

type callAttempt struct {
	record logent.ModelCallRecord
	sent   bool
}

type callAttemptKey struct{}

type modelCallMetricsKey struct{}

// WithModelCallMetrics makes model calls under ctx report to recorder.
func WithModelCallMetrics(ctx context.Context, recorder *metrics.ModelCallRecorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, modelCallMetricsKey{}, recorder)
}

// MarkToken lets a decoder that holds output back from the stream sink mark
// the first token when it arrives.
func MarkToken(ctx context.Context, payload string) {
	if attempt, ok := ctx.Value(callAttemptKey{}).(*callAttempt); ok {
		attempt.markToken(payload)
	}
}

func (a *callAttempt) since() *time.Duration {
	elapsed := time.Since(a.record.StartedAt)
	return &elapsed
}

func (a *callAttempt) markHeaders() {
	a.record.Headers = a.since()
}

func (a *callAttempt) markToken(payload string) {
	if payload != "" && a.record.TTFT == nil {
		a.record.TTFT = a.since()
	}
}

func (a *callAttempt) markComplete() {
	a.record.StreamComplete = a.since()
}

func newCallAttempt(ctx context.Context, c Client, input model.Request) *callAttempt {
	scope := model.CallScopeFromContext(ctx)
	attempt := &callAttempt{
		record: logent.ModelCallRecord{
			StartedAt:          time.Now(),
			CallID:             uuid.NewString(),
			Purpose:            string(scope.Purpose),
			ModelCallContextID: scope.ModelCallContextID,
			Attempt:            scope.Attempt,
			APIFormat:          string(c.APIFormat()),
			APIVariant:         string(c.ModelAPIVariant()),
			Model:              c.ProviderModelSlug,
			RequestBytes:       len(input.ProviderRequest),
		},
	}
	if c.Endpoint != nil {
		if endpoint, err := c.Endpoint.URL(); err == nil {
			if parsed, err := url.Parse(endpoint); err == nil {
				attempt.record.EndpointHost = parsed.Hostname()
			}
		}
	}
	return attempt
}

func (a *callAttempt) sink(next model.StreamSink) model.StreamSink {
	return timingSink{next: next, attempt: a}
}

func (a *callAttempt) finish(ctx context.Context, response model.Response, err error) {
	if !a.sent {
		return
	}
	record := a.record
	record.Duration = *a.since()
	record.ServedModel = response.ServedProviderModelSlug
	record.ProviderRequestID = response.ProviderRequestID
	if usage := response.Usage; usage.InputTokens > 0 || usage.OutputTokens > 0 {
		record.Usage = &logent.ModelCallUsage{
			InputTokens:       usage.InputTokens,
			CachedInputTokens: usage.CacheReadTokens,
			CacheWriteTokens:  usage.CacheWriteTokens,
			CacheReported:     usage.CacheReported,
			OutputTokens:      usage.OutputTokens,
			ReasoningTokens:   usage.ReasoningTokens,
		}
	}
	switch {
	case err == nil:
		record.Outcome = logent.ModelCallSuccess
	case ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		record.Outcome = logent.ModelCallCanceled
	default:
		record.Outcome = logent.ModelCallError
		record.ErrorKind = string(model.ErrorKindUnknown)
		if providerErr, ok := model.ClassifyError(err); ok {
			record.ErrorKind = string(providerErr.Kind)
			record.ErrorCode = providerErr.Code
			if record.StatusCode == 0 {
				record.StatusCode = providerErr.StatusCode
			}
			if record.ProviderRequestID == "" {
				record.ProviderRequestID = providerErr.RequestID
			}
		}
	}
	logent.ModelCall(ctx, record)
	if recorder, ok := ctx.Value(modelCallMetricsKey{}).(*metrics.ModelCallRecorder); ok {
		recorder.Observe(metricsSample(record))
	}
}

func metricsSample(r logent.ModelCallRecord) metrics.ModelCallSample {
	sample := metrics.ModelCallSample{
		APIFormat:    r.APIFormat,
		APIVariant:   r.APIVariant,
		Provider:     r.EndpointHost,
		Model:        r.Model,
		Purpose:      r.Purpose,
		Outcome:      string(r.Outcome),
		ErrorKind:    r.ErrorKind,
		RequestBytes: r.RequestBytes,
		TTFT:         r.TTFT,
		Duration:     r.Duration,
	}
	if u := r.Usage; u != nil {
		sample.UsageReported = true
		sample.InputTokens = u.InputTokens
		sample.CachedInputTokens = u.CachedInputTokens
		sample.CacheWriteTokens = u.CacheWriteTokens
		sample.CacheReported = u.CacheReported
		sample.OutputTokens = u.OutputTokens
		sample.ReasoningTokens = u.ReasoningTokens
	}
	return sample
}

// A tool call's name counts as its first token: the model generates it before the arguments.
type timingSink struct {
	next    model.StreamSink
	attempt *callAttempt
}

func (s timingSink) Emit(ctx context.Context, event model.StreamEvent) {
	switch event.Kind {
	case model.StreamEventTextDelta, model.StreamEventThinkingDelta, model.StreamEventToolArgsDelta:
		s.attempt.markToken(event.Delta)
	case model.StreamEventBlockStart:
		if event.Block != nil && event.Block.Kind == model.StreamBlockToolUse {
			s.attempt.markToken(event.Block.ToolName)
		}
	case model.StreamEventBlockStop, model.StreamEventMessageStop, model.StreamEventError:
	}
	if s.next != nil {
		s.next.Emit(ctx, event)
	}
}

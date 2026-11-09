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
	callID       string
	scope        model.CallScope
	requestBytes int
	started      time.Time
	sent         bool
	streamed     bool
	statusCode   int

	headers        *time.Duration
	ttft           *time.Duration
	streamComplete *time.Duration
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

func newCallAttempt(ctx context.Context, input model.Request) *callAttempt {
	return &callAttempt{
		callID:       uuid.NewString(),
		scope:        model.CallScopeFromContext(ctx),
		requestBytes: len(input.ProviderRequest),
		started:      time.Now(),
	}
}

func (a *callAttempt) since() *time.Duration {
	elapsed := time.Since(a.started)
	return &elapsed
}

func (a *callAttempt) markHeaders() {
	a.headers = a.since()
}

func (a *callAttempt) markToken(payload string) {
	if payload != "" && a.ttft == nil {
		a.ttft = a.since()
	}
}

func (a *callAttempt) markComplete() {
	a.streamComplete = a.since()
}

func (a *callAttempt) sink(next model.StreamSink) model.StreamSink {
	return timingSink{next: next, attempt: a}
}

func (a *callAttempt) finish(ctx context.Context, c Client, response model.Response, err error) {
	if !a.sent {
		return
	}
	duration := *a.since()
	outcome, errorKind, errorCode := logent.ModelCallSuccess, "", ""
	statusCode, requestID := a.statusCode, response.ProviderRequestID
	switch {
	case err == nil:
	case ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		outcome = logent.ModelCallCanceled
	default:
		outcome, errorKind = logent.ModelCallError, string(model.ErrorKindUnknown)
		if providerErr, ok := model.ClassifyError(err); ok {
			errorKind, errorCode = string(providerErr.Kind), providerErr.Code
			if statusCode == 0 {
				statusCode = providerErr.StatusCode
			}
			if requestID == "" {
				requestID = providerErr.RequestID
			}
		}
	}
	endpointHost := ""
	if c.Endpoint != nil {
		if endpoint, err := c.Endpoint.URL(); err == nil {
			if parsed, err := url.Parse(endpoint); err == nil {
				endpointHost = parsed.Hostname()
			}
		}
	}
	usage := response.Usage
	usageReported := usage.InputTokens > 0 || usage.OutputTokens > 0
	var loggedUsage *logent.ModelCallUsage
	if usageReported {
		loggedUsage = &logent.ModelCallUsage{
			InputTokens:       usage.InputTokens,
			CachedInputTokens: usage.CacheReadTokens,
			CacheWriteTokens:  usage.CacheWriteTokens,
			CacheReported:     usage.CacheReported,
			OutputTokens:      usage.OutputTokens,
			ReasoningTokens:   usage.ReasoningTokens,
		}
	}
	logent.ModelCall(ctx, logent.ModelCallRecord{
		CallID:             a.callID,
		Purpose:            string(a.scope.Purpose),
		ModelCallContextID: a.scope.ModelCallContextID,
		Attempt:            a.scope.Attempt,
		APIFormat:          string(c.APIFormat()),
		APIVariant:         string(c.ModelAPIVariant()),
		Model:              c.ProviderModelSlug,
		ServedModel:        response.ServedProviderModelSlug,
		EndpointHost:       endpointHost,
		ProviderRequestID:  requestID,
		Streamed:           a.streamed,
		StatusCode:         statusCode,
		Outcome:            outcome,
		ErrorKind:          errorKind,
		ErrorCode:          errorCode,
		RequestBytes:       a.requestBytes,
		Usage:              loggedUsage,
		StartedAt:          a.started,
		Headers:            a.headers,
		TTFT:               a.ttft,
		StreamComplete:     a.streamComplete,
		Duration:           duration,
	})
	if recorder, ok := ctx.Value(modelCallMetricsKey{}).(*metrics.ModelCallRecorder); ok {
		recorder.Observe(metrics.ModelCallSample{
			APIFormat:         string(c.APIFormat()),
			APIVariant:        string(c.ModelAPIVariant()),
			Provider:          endpointHost,
			Model:             c.ProviderModelSlug,
			Purpose:           string(a.scope.Purpose),
			Outcome:           string(outcome),
			ErrorKind:         errorKind,
			RequestBytes:      a.requestBytes,
			UsageReported:     usageReported,
			InputTokens:       usage.InputTokens,
			CachedInputTokens: usage.CacheReadTokens,
			CacheWriteTokens:  usage.CacheWriteTokens,
			CacheReported:     usage.CacheReported,
			OutputTokens:      usage.OutputTokens,
			ReasoningTokens:   usage.ReasoningTokens,
			TTFT:              a.ttft,
			Duration:          duration,
		})
	}
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

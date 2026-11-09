package route

import (
	"context"
	"errors"
	"net/url"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/calltrace"
)

// callAttempt accumulates the model.call record for one RespondStream. Only
// sizes, counts, and timings are kept; request and response content are not.
type callAttempt struct {
	tracker *calltrace.Tracker
	record  calltrace.Record
	sent    bool
}

func newCallAttempt(ctx context.Context, c Client, input model.Request) *callAttempt {
	attempt := &callAttempt{
		tracker: calltrace.Start(nil),
		record: calltrace.Record{
			CallID:       uuid.NewString(),
			Scope:        calltrace.ScopeFromContext(ctx),
			APIFormat:    string(c.APIFormat()),
			APIVariant:   string(c.ModelAPIVariant()),
			Model:        c.ProviderModelSlug,
			RequestBytes: len(input.ProviderRequest),
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
	return timingSink{next: next, tracker: a.tracker}
}

func (a *callAttempt) finish(ctx context.Context, response model.Response, err error) {
	if !a.sent {
		return
	}
	record := a.record
	a.tracker.Fill(&record)
	record.ServedModel = response.ServedProviderModelSlug
	record.ProviderRequestID = response.ProviderRequestID
	if usage := response.Usage; usage.InputTokens > 0 || usage.OutputTokens > 0 {
		record.Usage = &calltrace.Usage{
			InputTokens:       usage.InputTokens,
			CachedInputTokens: usage.CacheReadTokens,
			CacheWriteTokens:  usage.CacheWriteTokens,
			OutputTokens:      usage.OutputTokens,
			ReasoningTokens:   usage.ReasoningTokens,
		}
	}
	switch {
	case err == nil:
		record.Outcome = calltrace.OutcomeSuccess
	case ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		record.Outcome = calltrace.OutcomeCanceled
	default:
		record.Outcome = calltrace.OutcomeError
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
	calltrace.RecorderFromContext(ctx).Observe(record.MetricsSample())
}

// timingSink marks the first token before forwarding events. A token is any
// nonempty text, reasoning, or tool-argument delta, or a tool call's name,
// which the model generates before its arguments. Text and thinking block
// starts, stops, and empty deltas are stream metadata.
type timingSink struct {
	next    model.StreamSink
	tracker *calltrace.Tracker
}

func (s timingSink) Emit(ctx context.Context, event model.StreamEvent) {
	switch event.Kind {
	case model.StreamEventTextDelta, model.StreamEventThinkingDelta, model.StreamEventToolArgsDelta:
		s.tracker.MarkToken(event.Delta)
	case model.StreamEventBlockStart:
		if event.Block != nil && event.Block.Kind == model.StreamBlockToolUse {
			s.tracker.MarkToken(event.Block.ToolName)
		}
	case model.StreamEventBlockStop, model.StreamEventMessageStop, model.StreamEventError:
	}
	if s.next != nil {
		s.next.Emit(ctx, event)
	}
}

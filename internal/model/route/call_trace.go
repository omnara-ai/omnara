package route

import (
	"context"
	"errors"
	"net/url"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/calltrace"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
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

// markBufferedContent times content from a non-streamed body, which becomes
// available all at once when the body has been read.
func (a *callAttempt) markBufferedContent(response model.Response) {
	for _, part := range response.Content {
		switch part.Type {
		case model.ResponsePartTypeText:
			a.tracker.MarkContent(calltrace.ContentText, part.Text)
		case model.ResponsePartTypeReasoning:
			a.tracker.MarkContent(calltrace.ContentReasoning, part.Text)
		case model.ResponsePartTypeToolCall:
			a.tracker.MarkContent(calltrace.ContentToolArguments, string(part.ToolInput))
		case modelenvelope.ResponsePartTypeError:
		}
	}
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

// timingSink times content deltas before forwarding them. Block starts, stops,
// and empty deltas are stream metadata and do not count as content.
type timingSink struct {
	next    model.StreamSink
	tracker *calltrace.Tracker
}

func (s timingSink) Emit(ctx context.Context, event model.StreamEvent) {
	switch event.Kind {
	case model.StreamEventTextDelta:
		s.tracker.MarkContent(calltrace.ContentText, event.Delta)
	case model.StreamEventThinkingDelta:
		s.tracker.MarkContent(calltrace.ContentReasoning, event.Delta)
	case model.StreamEventToolArgsDelta:
		s.tracker.MarkContent(calltrace.ContentToolArguments, event.Delta)
	case model.StreamEventBlockStart, model.StreamEventBlockStop, model.StreamEventMessageStop, model.StreamEventError:
	}
	if s.next != nil {
		s.next.Emit(ctx, event)
	}
}

package anthropicmessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
)

type recordingSink struct {
	events []model.StreamEvent
}

func (s *recordingSink) Emit(_ context.Context, event model.StreamEvent) {
	s.events = append(s.events, event)
}

func (s *recordingSink) kinds() []model.StreamEventKind {
	kinds := make([]model.StreamEventKind, 0, len(s.events))
	for _, event := range s.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func anthropicSSE(events ...[2]string) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString("event: " + ev[0] + "\n")
		b.WriteString("data: " + ev[1] + "\n\n")
	}
	return b.String()
}

func consumeAnthropicStream(t *testing.T, stream string, sink model.StreamSink) (model.Response, error) {
	t.Helper()
	p := protocol{client: Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}}
	return p.ConsumeStream(context.Background(), strings.NewReader(stream), http.StatusOK, http.Header{}, sink)
}

func assertAnthropicStreamClosedBeforeError(t *testing.T, sink *recordingSink) {
	t.Helper()
	kinds := sink.kinds()
	if len(kinds) < 2 ||
		kinds[len(kinds)-2] != model.StreamEventBlockStop ||
		kinds[len(kinds)-1] != model.StreamEventError {
		t.Fatalf("terminal stream events = %v, want block_stop followed by error", kinds)
	}
}

func TestAnthropicConsumeStreamParsesViaParseResponse(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test-served","usage":{"input_tokens":10}}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"thinking","thinking":""}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`},
		[2]string{"content_block_stop", `{"index":0}`},
		[2]string{"content_block_start", `{"index":1,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"index":1,"delta":{"type":"text_delta","text":"Hello "}}`},
		[2]string{"content_block_delta", `{"index":1,"delta":{"type":"text_delta","text":"world"}}`},
		[2]string{"content_block_stop", `{"index":1}`},
		[2]string{
			"content_block_start",
			`{"index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		},
		[2]string{"content_block_delta", `{"index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`},
		[2]string{"content_block_delta", `{"index":2,"delta":{"type":"input_json_delta","partial_json":"\"sf\"}"}}`},
		[2]string{"content_block_stop", `{"index":2}`},
		[2]string{"message_delta", `{"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":25}}`},
		[2]string{"message_stop", `{}`},
	)
	sink := &recordingSink{}
	resp, err := consumeAnthropicStream(t, stream, sink)
	if err != nil {
		t.Fatalf("ConsumeStream: %v", err)
	}
	if resp.ID != "msg_1" || resp.ServedProviderModelSlug != "claude-test-served" {
		t.Fatalf("unexpected identity: %+v", resp)
	}
	if resp.Text() != "Hello world" {
		t.Fatalf("unexpected text: %q", resp.Text())
	}
	if resp.StopReason != model.StopReasonToolUse {
		t.Fatalf("unexpected stop reason: %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 25 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(calls))
	}
	call := calls[0]
	if call.ID != "toolu_1" || call.Name != "get_weather" || string(call.Input) != `{"city":"sf"}` {
		t.Fatalf("unexpected tool call: %+v", call)
	}
	if !strings.Contains(string(resp.ProviderReplay), "sig-abc") {
		t.Fatalf("expected thinking signature in replay batch, got %s", resp.ProviderReplay)
	}
	var reasoning string
	for _, part := range resp.Content {
		if part.Type == model.ResponsePartTypeReasoning {
			reasoning = part.Text
		}
	}
	if reasoning != "pondering" {
		t.Fatalf("expected accumulated reasoning part, got %q", reasoning)
	}
	wantKinds := []model.StreamEventKind{
		model.StreamEventBlockStart,
		model.StreamEventThinkingDelta,
		model.StreamEventBlockStop,
		model.StreamEventBlockStart,
		model.StreamEventTextDelta,
		model.StreamEventTextDelta,
		model.StreamEventBlockStop,
		model.StreamEventBlockStart,
		model.StreamEventToolArgsDelta,
		model.StreamEventToolArgsDelta,
		model.StreamEventBlockStop,
		model.StreamEventMessageStop,
	}
	got := sink.kinds()
	if len(got) != len(wantKinds) {
		t.Fatalf("unexpected event kinds: %v", got)
	}
	for i, kind := range wantKinds {
		if got[i] != kind {
			t.Fatalf("event %d: got %q want %q (all: %v)", i, got[i], kind, got)
		}
	}
}

func TestAnthropicConsumeStreamTruncatedIsError(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":13}}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"partial"}}`},
	)
	sink := &recordingSink{}
	resp, err := consumeAnthropicStream(t, stream, sink)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != model.ErrorKindTransient {
		t.Fatalf("expected transient provider error, got %v", err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("truncated stream = %T %v, want ambiguous outcome", err, err)
	}
	if resp.ID != "msg_1" || resp.ServedProviderModelSlug != "claude-test" ||
		resp.Usage.InputTokens != 13 {
		t.Fatalf("truncated stream evidence = %+v", resp)
	}
	assertAnthropicStreamClosedBeforeError(t, sink)
}

func TestAnthropicConsumeMalformedStreamClosesOpenBlock(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":`},
	)
	sink := &recordingSink{}
	_, err := consumeAnthropicStream(t, stream, sink)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("malformed stream = %T %v, want ambiguous outcome", err, err)
	}
	assertAnthropicStreamClosedBeforeError(t, sink)
}

func TestAnthropicConsumeStreamRejectsUnopenedBlockEvents(t *testing.T) {
	for _, test := range []struct {
		name  string
		event [2]string
	}{
		{
			name:  "delta",
			event: [2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"lost"}}`},
		},
		{
			name:  "stop",
			event: [2]string{"content_block_stop", `{"index":0}`},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := anthropicSSE(
				[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
				test.event,
				[2]string{"message_delta", `{"delta":{"stop_reason":"end_turn"}}`},
				[2]string{"message_stop", `{}`},
			)
			sink := &recordingSink{}
			_, err := consumeAnthropicStream(t, stream, sink)
			if !model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("unopened block %s = %T %v, want ambiguous outcome", test.name, err, err)
			}
			if kinds := sink.kinds(); len(kinds) != 1 || kinds[0] != model.StreamEventError {
				t.Fatalf("unopened block %s events = %v, want error only", test.name, kinds)
			}
		})
	}
}

func TestAnthropicConsumeStreamRejectsDuplicateBlockStart(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":"first"}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":"replacement"}}`},
	)
	sink := &recordingSink{}
	_, err := consumeAnthropicStream(t, stream, sink)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("duplicate block start = %T %v, want ambiguous outcome", err, err)
	}
	want := []model.StreamEventKind{
		model.StreamEventBlockStart,
		model.StreamEventTextDelta,
		model.StreamEventBlockStop,
		model.StreamEventError,
	}
	got := sink.kinds()
	if len(got) != len(want) {
		t.Fatalf("duplicate block events = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("duplicate block event %d = %q, want %q (all: %v)", index, got[index], want[index], got)
		}
	}
}

func TestAnthropicConsumeStreamRejectsOverlappingBlocks(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":"first"}}`},
		[2]string{"content_block_start", `{"index":1,"content_block":{"type":"text","text":"second"}}`},
		[2]string{"content_block_stop", `{"index":1}`},
		[2]string{"content_block_stop", `{"index":0}`},
		[2]string{"message_delta", `{"delta":{"stop_reason":"end_turn"}}`},
		[2]string{"message_stop", `{}`},
	)
	sink := &recordingSink{}
	_, err := consumeAnthropicStream(t, stream, sink)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("overlapping blocks = %T %v, want ambiguous outcome", err, err)
	}
	assertAnthropicStreamClosedBeforeError(t, sink)
}

func TestAnthropicConsumeStreamRejectsInvalidStartIndexes(t *testing.T) {
	for _, test := range []struct {
		name  string
		start string
	}{
		{name: "missing", start: `{"content_block":{"type":"text","text":"lost"}}`},
		{name: "negative", start: `{"index":-1,"content_block":{"type":"text","text":"lost"}}`},
		{name: "skipped", start: `{"index":1,"content_block":{"type":"text","text":"lost"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := anthropicSSE(
				[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
				[2]string{"content_block_start", test.start},
			)
			sink := &recordingSink{}
			_, err := consumeAnthropicStream(t, stream, sink)
			if !model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("%s start index = %T %v, want ambiguous outcome", test.name, err, err)
			}
			if kinds := sink.kinds(); len(kinds) != 1 || kinds[0] != model.StreamEventError {
				t.Fatalf("%s start index events = %v, want error only", test.name, kinds)
			}
		})
	}
}

func TestAnthropicConsumeStreamRejectsInvalidActiveBlockIndexes(t *testing.T) {
	for _, test := range []struct {
		name  string
		event [2]string
	}{
		{name: "missing delta", event: [2]string{"content_block_delta", `{"delta":{"type":"text_delta","text":"lost"}}`}},
		{
			name:  "negative delta",
			event: [2]string{"content_block_delta", `{"index":-1,"delta":{"type":"text_delta","text":"lost"}}`},
		},
		{name: "missing stop", event: [2]string{"content_block_stop", `{}`}},
		{name: "negative stop", event: [2]string{"content_block_stop", `{"index":-1}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := anthropicSSE(
				[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
				[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":"open"}}`},
				test.event,
			)
			sink := &recordingSink{}
			_, err := consumeAnthropicStream(t, stream, sink)
			if !model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("%s = %T %v, want ambiguous outcome", test.name, err, err)
			}
			assertAnthropicStreamClosedBeforeError(t, sink)
		})
	}
}

func TestAnthropicConsumeStreamRejectsNoncontiguousNextBlock(t *testing.T) {
	for _, nextIndex := range []int{0, 2} {
		t.Run(fmt.Sprintf("index_%d", nextIndex), func(t *testing.T) {
			stream := anthropicSSE(
				[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
				[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":"first"}}`},
				[2]string{"content_block_stop", `{"index":0}`},
				[2]string{
					"content_block_start",
					fmt.Sprintf(`{"index":%d,"content_block":{"type":"text","text":"invalid"}}`, nextIndex),
				},
			)
			sink := &recordingSink{}
			_, err := consumeAnthropicStream(t, stream, sink)
			if !model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("next block index %d = %T %v, want ambiguous outcome", nextIndex, err, err)
			}
			kinds := sink.kinds()
			if len(kinds) < 2 || kinds[len(kinds)-2] != model.StreamEventBlockStop ||
				kinds[len(kinds)-1] != model.StreamEventError {
				t.Fatalf("next block index %d events = %v, want completed block then error", nextIndex, kinds)
			}
		})
	}
}

func TestAnthropicConsumeStreamMessageStopWithoutStopReasonIsAmbiguous(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
		[2]string{"message_stop", `{}`},
	)
	_, err := consumeAnthropicStream(t, stream, &recordingSink{})
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("message_stop without stop reason = %T %v, want ambiguous outcome", err, err)
	}
}

func TestAnthropicConsumeStreamClassifiesInStreamError(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":19}}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"partial"}}`},
		[2]string{
			"error",
			`{"request_id":"req_stream","retry_after":43,` +
				`"error":{"type":"api_error","error_type":"rate_limit_exceeded","message":"slow down"}}`,
		},
		[2]string{"message_start", `{"message":`},
	)
	sink := &recordingSink{}
	resp, err := consumeAnthropicStream(t, stream, sink)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
	if providerErr.Kind != model.ErrorKindRateLimit || providerErr.RequestID != "req_stream" ||
		providerErr.RetryAfter != nil {
		t.Fatalf("unexpected streamed provider error: %+v", providerErr)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("typed stream error must be explicit: %T %v", err, err)
	}
	if resp.ID != "msg_1" || resp.ServedProviderModelSlug != "claude-test" ||
		resp.Usage.InputTokens != 19 {
		t.Fatalf("failed stream evidence = %+v", resp)
	}
	assertAnthropicStreamClosedBeforeError(t, sink)
}

func TestAnthropicConsumeStreamClassifiesWrappedContextOverflow(t *testing.T) {
	stream := anthropicSSE([2]string{
		"error",
		`{"error":{"type":"api_error",` +
			`"message":"Your input exceeds the context window of this model."}}`,
	})
	_, err := consumeAnthropicStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindContextWindow ||
		providerErr.Code != "api_error" {
		t.Fatalf("wrapped streamed context overflow = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestAnthropicConsumeStreamClassifiesWrappedPayloadOverflow(t *testing.T) {
	stream := anthropicSSE([2]string{
		"error",
		`{"error":{"type":"api_error",` +
			`"message":"Request entity too large for the upstream provider."}}`,
	})
	_, err := consumeAnthropicStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindPayloadTooLarge ||
		providerErr.Code != "api_error" {
		t.Fatalf("wrapped streamed payload overflow = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestAnthropicConsumeMalformedExplicitErrorPreservesEvidence(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"error", `{"error":`},
	)
	sink := &recordingSink{}
	_, err := consumeAnthropicStream(t, stream, sink)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Code != "malformed_error_event" {
		t.Fatalf("malformed explicit error = %T %v, want classified provider error", err, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("explicit error event must not become an ambiguous transport failure: %T %v", err, err)
	}
	var metadata map[string]any
	if json.Unmarshal(providerErr.Metadata, &metadata) != nil ||
		metadata["event"] != "error" || metadata["raw_event_bytes"] != float64(len(`{"error":`)) {
		t.Fatalf("malformed error metadata = %s", providerErr.Metadata)
	}
	assertAnthropicStreamClosedBeforeError(t, sink)
}

func TestAnthropicConsumeStreamMaxTokensIsSuccessful(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_partial","model":"claude-test"}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"partial"}}`},
		[2]string{"content_block_stop", `{"index":0}`},
		[2]string{"message_delta", `{"delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":5}}`},
		[2]string{"message_stop", `{}`},
	)
	resp, err := consumeAnthropicStream(t, stream, &recordingSink{})
	if err != nil {
		t.Fatalf("max_tokens stream: %v", err)
	}
	if resp.StopReason != model.StopReasonMaxTokens || resp.Text() != "partial" {
		t.Fatalf("max_tokens stream response = %+v", resp)
	}
}

func TestAnthropicConsumeStreamPreservesMalformedToolInput(t *testing.T) {
	for _, stopReason := range []string{"tool_use", "max_tokens"} {
		t.Run(stopReason, func(t *testing.T) {
			stream := anthropicSSE(
				[2]string{"message_start", `{"message":{"id":"msg_bad_tool","model":"claude-test"}}`},
				[2]string{
					"content_block_start",
					`{"index":0,"content_block":{"type":"tool_use","id":"toolu_bad","name":"get_weather","input":{}}}`,
				},
				[2]string{"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`},
				[2]string{"content_block_stop", `{"index":0}`},
				[2]string{"message_delta", `{"delta":{"stop_reason":"` + stopReason + `"},"usage":{"output_tokens":4}}`},
				[2]string{"message_stop", `{}`},
			)
			resp, err := consumeAnthropicStream(t, stream, &recordingSink{})
			if err != nil {
				t.Fatalf("ConsumeStream: %v", err)
			}
			calls := resp.ToolCalls()
			if len(calls) != 1 || string(calls[0].Input) != `{"city":` {
				t.Fatalf("malformed tool input = %+v, want preserved raw fragment", calls)
			}
			if json.Valid(calls[0].Input) {
				t.Fatalf("malformed tool input was normalized into executable JSON: %s", calls[0].Input)
			}
		})
	}
}

func TestAnthropicConsumeStreamPreservesRedactedThinkingBlocks(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"redacted_thinking","data":"opaque"}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"must not stream"}}`},
		[2]string{"content_block_stop", `{"index":0}`},
		[2]string{
			"content_block_start",
			`{"index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		},
		[2]string{"content_block_stop", `{"index":1}`},
		[2]string{"message_delta", `{"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`},
		[2]string{"message_stop", `{}`},
	)
	sink := &recordingSink{}
	resp, err := consumeAnthropicStream(t, stream, sink)
	if err != nil {
		t.Fatalf("ConsumeStream: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(calls))
	}
	if !strings.Contains(string(resp.ProviderReplay), "redacted_thinking") {
		t.Fatalf("expected redacted_thinking preserved in replay batch: %s", resp.ProviderReplay)
	}
	for _, event := range sink.events {
		if event.Kind == model.StreamEventBlockStart || event.Kind == model.StreamEventBlockStop ||
			event.Kind == model.StreamEventTextDelta || event.Kind == model.StreamEventThinkingDelta {
			if event.BlockIndex == 0 {
				t.Fatalf("unexpected block event for unknown block: %+v", event)
			}
		}
	}
}

func TestAnthropicConsumeStreamRejectsUnsupportedContentBlock(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test"}}`},
		[2]string{
			"content_block_start",
			`{"index":0,"content_block":{"type":"server_tool_use","id":"srv_1"}}`,
		},
		[2]string{"content_block_stop", `{"index":0}`},
		[2]string{"message_delta", `{"delta":{"stop_reason":"end_turn"}}`},
		[2]string{"message_stop", `{}`},
	)
	_, err := consumeAnthropicStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("unsupported streamed content block = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

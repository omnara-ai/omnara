package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
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

func openAISSE(events ...[2]string) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString("event: " + ev[0] + "\n")
		b.WriteString("data: " + ev[1] + "\n\n")
	}
	return b.String()
}

func openAIDataOnlySSE(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString("data: " + event + "\n\n")
	}
	return b.String()
}

func consumeOpenAIStream(t *testing.T, stream string, sink model.StreamSink) (model.Response, error) {
	t.Helper()
	p := protocol{client: Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}}
	return p.ConsumeStream(context.Background(), strings.NewReader(stream), http.StatusOK, http.Header{}, sink)
}

func TestOpenAIConsumeStreamEmitsDeltasAndParsesTerminal(t *testing.T) {
	completed := `{"id":"resp_1","model":"gpt-test-served","status":"completed",` +
		`"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Hello world"}]},` +
		`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"sf\"}"}],` +
		`"usage":{"input_tokens":10,"output_tokens":25}}`
	stream := openAISSE(
		[2]string{"response.output_item.added", `{"item":{"id":"msg_1","type":"message"}}`},
		[2]string{"response.content_part.added", `{"item_id":"msg_1","content_index":0,"part":{"type":"output_text"}}`},
		[2]string{"response.output_text.delta", `{"item_id":"msg_1","content_index":0,"delta":"Hello "}`},
		[2]string{"response.output_text.delta", `{"item_id":"msg_1","content_index":0,"delta":"world"}`},
		[2]string{"response.content_part.done", `{"item_id":"msg_1","content_index":0}`},
		[2]string{
			"response.output_item.added",
			`{"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather"}}`,
		},
		[2]string{"response.function_call_arguments.delta", `{"item_id":"fc_1","delta":"{\"city\":\"sf\"}"}`},
		[2]string{"response.output_item.done", `{"item":{"id":"fc_1","type":"function_call"}}`},
		[2]string{"response.completed", `{"response":` + completed + `}`},
	)
	sink := &recordingSink{}
	resp, err := consumeOpenAIStream(t, stream, sink)
	if err != nil {
		t.Fatalf("ConsumeStream: %v", err)
	}
	if resp.Text() != "Hello world" {
		t.Fatalf("unexpected text: %q", resp.Text())
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("unexpected tool calls: %+v", calls)
	}
	if resp.StopReason != model.StopReasonToolUse {
		t.Fatalf("unexpected stop reason: %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 25 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	last := sink.events[len(sink.events)-1]
	if last.Kind != model.StreamEventMessageStop {
		t.Fatalf("expected terminal message stop event, got %q", last.Kind)
	}
	deltas := 0
	for _, event := range sink.events {
		if event.Kind == model.StreamEventTextDelta || event.Kind == model.StreamEventToolArgsDelta {
			deltas++
		}
	}
	if deltas != 3 {
		t.Fatalf("expected 3 delta events, got %d", deltas)
	}
}

func TestOpenAIConsumeStreamUsesPayloadTypeForDataOnlyFrames(t *testing.T) {
	completed := `{"id":"resp_azure","model":"gpt-test","status":"completed",` +
		`"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hello"}]}]}`
	stream := openAIDataOnlySSE(
		`{"type":"response.created","response":{"id":"resp_azure","status":"in_progress"}}`,
		`{"type":"response.completed","response":`+completed+`}`,
	)

	resp, err := consumeOpenAIStream(t, stream, &recordingSink{})
	if err != nil {
		t.Fatalf("ConsumeStream data-only frames: %v", err)
	}
	if resp.ID != "resp_azure" || resp.Text() != "hello" {
		t.Fatalf("data-only response = %+v", resp)
	}
}

func TestOpenAIConsumeStreamUsesPayloadTypeForDataOnlyError(t *testing.T) {
	stream := openAIDataOnlySSE(
		`{"type":"error","code":"server_error","message":"upstream unavailable",` +
			`"request_id":"req_data_only"}`,
	)

	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindProviderUnavailable ||
		providerErr.RequestID != "" || model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("data-only stream error = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestOpenAIConsumeStreamRejectsEventTypeDisagreement(t *testing.T) {
	stream := openAISSE(
		[2]string{
			"response.completed",
			`{"type":"response.failed","response":{"id":"resp_mismatch","status":"failed"}}`,
		},
	)

	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	if !model.IsAmbiguousProviderOutcome(err) || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("mismatched event type = %T %v, want ambiguous protocol failure", err, err)
	}
}

func TestOpenAIConsumeStreamRejectsTerminalEventStatusDisagreement(t *testing.T) {
	completed := `{"id":"resp_mismatch","model":"gpt-test","status":"completed",` +
		`"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"must not accept"}]}]}`
	stream := openAISSE(
		[2]string{"response.failed", `{"response":` + completed + `}`},
	)

	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	if !model.IsAmbiguousProviderOutcome(err) || !strings.Contains(err.Error(), `want "failed"`) {
		t.Fatalf("mismatched terminal status = %T %v, want ambiguous protocol failure", err, err)
	}
}

func TestOpenAIConsumeStreamRejectsInvalidDataOnlyPayloadType(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		message string
	}{
		{name: "missing", payload: `{"response":{"id":"resp_invalid"}}`, message: "missing type"},
		{name: "empty", payload: `{"type":"","response":{"id":"resp_invalid"}}`, message: "non-empty string"},
		{name: "non-string", payload: `{"type":7,"response":{"id":"resp_invalid"}}`, message: "non-empty string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := consumeOpenAIStream(t, openAIDataOnlySSE(test.payload), &recordingSink{})
			if !model.IsAmbiguousProviderOutcome(err) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("invalid event type = %T %v, want ambiguous protocol failure", err, err)
			}
		})
	}
}

func TestOpenAIConsumeStreamIncompleteMatchesNonStreaming(t *testing.T) {
	incomplete := `{"id":"resp_1","model":"gpt-test","status":"incomplete",` +
		`"incomplete_details":{"reason":"max_output_tokens"},` +
		`"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"partial"}]}],` +
		`"usage":{"input_tokens":10,"output_tokens":25}}`
	stream := openAISSE(
		[2]string{"response.incomplete", `{"response":` + incomplete + `}`},
	)
	resp, err := consumeOpenAIStream(t, stream, &recordingSink{})
	if err != nil {
		t.Fatalf("incomplete response must parse like the non-streaming path: %v", err)
	}
	if resp.StopReason != model.StopReasonMaxTokens {
		t.Fatalf("unexpected stop reason: %q", resp.StopReason)
	}
	if resp.Text() != "partial" {
		t.Fatalf("unexpected text: %q", resp.Text())
	}
}

func TestOpenAIConsumeStreamFailedIsClassified(t *testing.T) {
	failed := `{"id":"resp_1","model":"gpt-test","status":"failed",` +
		`"request_id":"req_stream","retry_after":29,` +
		`"error":{"code":"rate_limit_exceeded","message":"slow down"},` +
		`"usage":{"input_tokens":17,"output_tokens":3}}`
	stream := openAISSE(
		[2]string{"response.failed", `{"response":` + failed + `}`},
	)
	sink := &recordingSink{}
	resp, err := consumeOpenAIStream(t, stream, sink)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
	if providerErr.Kind != model.ErrorKindRateLimit || providerErr.RequestID != "" ||
		providerErr.RetryAfter != nil {
		t.Fatalf("unexpected streamed provider error: %+v", providerErr)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("complete response.failed event must be explicit: %T %v", err, err)
	}
	if resp.ID != "resp_1" || resp.ServedProviderModelSlug != "gpt-test" ||
		resp.Usage.InputTokens != 17 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("failed stream evidence = %+v", resp)
	}
	last := sink.events[len(sink.events)-1]
	if last.Kind != model.StreamEventError {
		t.Fatalf("expected terminal error event, got %q", last.Kind)
	}
}

func TestOpenAIConsumeStreamFailedUsesTopLevelErrorType(t *testing.T) {
	tests := []struct {
		name      string
		errorType string
		code      string
		message   string
		want      model.ErrorKind
	}{
		{
			name:      "context behind lossy invalid prompt",
			errorType: "provider_unavailable",
			code:      "invalid_prompt",
			message:   "Your input exceeds the context window of this model.",
			want:      model.ErrorKindContextWindow,
		},
		{
			name:      "payload behind lossy invalid prompt",
			errorType: "provider_unavailable",
			code:      "invalid_prompt",
			message:   "Request body too large for the upstream provider.",
			want:      model.ErrorKindPayloadTooLarge,
		},
		{
			name:      "authentication behind lossy server error",
			errorType: "authentication",
			code:      "server_error",
			message:   "Invalid credentials",
			want:      model.ErrorKindAuth,
		},
		{
			name:      "content policy behind lossy server error",
			errorType: "content_policy_violation",
			code:      "server_error",
			message:   "Output blocked",
			want:      model.ErrorKindInvalidRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failed := `{"id":"resp_failed","model":"gpt-test","status":"failed",` +
				`"error_type":"` + test.errorType + `",` +
				`"error":{"code":"` + test.code + `","message":"` + test.message + `"}}`
			stream := openAISSE([2]string{"response.failed", `{"response":` + failed + `}`})
			_, err := consumeOpenAIStream(t, stream, &recordingSink{})
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != test.want || providerErr.Code != test.errorType {
				t.Fatalf("top-level failed error type = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestOpenAIConsumeStreamTruncatedIsError(t *testing.T) {
	stream := openAISSE(
		[2]string{
			"response.created",
			`{"response":{"id":"resp_partial","model":"gpt-partial","status":"in_progress",` +
				`"usage":{"input_tokens":23,"output_tokens":2}}}`,
		},
		[2]string{"response.output_item.added", `{"item":{"id":"msg_1","type":"message"}}`},
	)
	resp, err := consumeOpenAIStream(t, stream, &recordingSink{})
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != model.ErrorKindTransient {
		t.Fatalf("expected transient provider error, got %v", err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("truncated stream = %T %v, want ambiguous outcome", err, err)
	}
	if resp.ID != "resp_partial" || resp.ServedProviderModelSlug != "gpt-partial" ||
		resp.Usage.InputTokens != 23 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("truncated stream evidence = %+v", resp)
	}
}

func TestOpenAIConsumeStreamRejectsDuplicateOutputItemID(t *testing.T) {
	stream := openAISSE(
		[2]string{
			"response.output_item.added",
			`{"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather"}}`,
		},
		[2]string{
			"response.output_item.added",
			`{"item":{"id":"fc_1","type":"function_call","call_id":"call_2","name":"replacement"}}`,
		},
	)
	sink := &recordingSink{}
	_, err := consumeOpenAIStream(t, stream, sink)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("duplicate output item = %T %v, want ambiguous outcome", err, err)
	}
	want := []model.StreamEventKind{
		model.StreamEventBlockStart,
		model.StreamEventBlockStop,
		model.StreamEventError,
	}
	if len(sink.events) != len(want) {
		t.Fatalf("duplicate output item events = %+v, want %v", sink.events, want)
	}
	for index, kind := range want {
		if sink.events[index].Kind != kind {
			t.Fatalf("duplicate output item event %d = %q, want %q", index, sink.events[index].Kind, kind)
		}
	}
}

func TestOpenAIConsumeStreamRejectsFunctionCallWithoutCallID(t *testing.T) {
	stream := openAISSE(
		[2]string{
			"response.output_item.added",
			`{"item":{"id":"fc_1","type":"function_call","name":"get_weather"}}`,
		},
	)
	sink := &recordingSink{}
	_, err := consumeOpenAIStream(t, stream, sink)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("missing function call id = %T %v, want ambiguous outcome", err, err)
	}
	if len(sink.events) != 1 || sink.events[0].Kind != model.StreamEventError {
		t.Fatalf("missing function call id events = %+v", sink.events)
	}
}

func TestOpenAIConsumeStreamRejectsUnsupportedTerminalOutputItem(t *testing.T) {
	completed := `{"id":"resp_unsupported","model":"gpt-test","status":"completed",` +
		`"output":[{"id":"search_1","type":"future_search_call"}]}`
	stream := openAISSE(
		[2]string{"response.output_item.added", `{"item":{"id":"search_1","type":"future_search_call"}}`},
		[2]string{"response.output_item.done", `{"item":{"id":"search_1","type":"future_search_call"}}`},
		[2]string{"response.completed", `{"response":` + completed + `}`},
	)

	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" ||
		!model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("unsupported streamed output item = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestOpenAIConsumeStreamStandaloneErrorEventIsExplicit(t *testing.T) {
	stream := openAISSE(
		[2]string{"response.output_item.added", `{"item":{"id":"msg_1","type":"message"}}`},
		[2]string{"response.content_part.added", `{"item_id":"msg_1","content_index":0,"part":{"type":"output_text"}}`},
		[2]string{
			"response.error",
			`{"type":"response.error","request_id":"req_event","retry_after":"11",` +
				`"error":{"code":"rate_limit_exceeded","message":"slow down"}}`,
		},
	)
	sink := &recordingSink{}
	_, err := consumeOpenAIStream(t, stream, sink)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindRateLimit || providerErr.RequestID != "" ||
		providerErr.RetryAfter != nil {
		t.Fatalf("standalone stream error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("complete response.error event must be explicit: %T %v", err, err)
	}
	got := sink.events
	if len(got) < 2 || got[len(got)-2].Kind != model.StreamEventBlockStop ||
		got[len(got)-1].Kind != model.StreamEventError {
		t.Fatalf("open block must close before stream error: %+v", got)
	}
}

func TestOpenAIConsumeStreamStandaloneErrorUsesTopLevelErrorType(t *testing.T) {
	stream := openAISSE([2]string{
		"response.error",
		`{"type":"response.error","error_type":"provider_unavailable",` +
			`"code":502,"message":"Your input exceeds the context window of this model."}`,
	})
	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindContextWindow ||
		providerErr.Code != "provider_unavailable" {
		t.Fatalf("standalone top-level error type = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestOpenAIConsumeStreamStandalonePayloadErrorUsesTopLevelErrorType(t *testing.T) {
	stream := openAISSE([2]string{
		"response.error",
		`{"type":"response.error","error_type":"provider_unavailable",` +
			`"code":502,"message":"Request entity too large for the upstream provider."}`,
	})
	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindPayloadTooLarge ||
		providerErr.Code != "provider_unavailable" {
		t.Fatalf("standalone top-level payload error = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestOpenAIConsumeStreamTopLevelErrorEventIsExplicit(t *testing.T) {
	stream := openAISSE(
		[2]string{
			"error",
			`{"type":"error","code":"server_error","message":"upstream unavailable",` +
				`"request_id":"req_top_level","retry_after":13}`,
		},
		[2]string{"response.output_text.delta", `{"item_id":`},
	)
	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindProviderUnavailable ||
		providerErr.Code != "server_error" || providerErr.RequestID != "" ||
		providerErr.RetryAfter != nil {
		t.Fatalf("top-level stream error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("complete top-level error event must be explicit: %T %v", err, err)
	}
}

func TestOpenAIConsumeStreamDoesNotInferStatusFromNumericErrorCode(t *testing.T) {
	stream := openAISSE(
		[2]string{
			"error",
			`{"type":"error","code":429,"message":"slow down","request_id":"req_numeric"}`,
		},
	)
	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown ||
		providerErr.StatusCode != http.StatusOK || providerErr.Code != "429" ||
		providerErr.RequestID != "" {
		t.Fatalf("numeric stream error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("numeric error event must be explicit: %T %v", err, err)
	}
}

func TestOpenAIConsumeStreamMalformedErrorEventIsExplicit(t *testing.T) {
	rawEvent := `{"error":`
	stream := openAISSE([2]string{"response.error", rawEvent})
	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown || providerErr.Code != "malformed_error_event" {
		t.Fatalf("malformed error event = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("labeled error event must be explicit even when malformed: %T %v", err, err)
	}
	var metadata map[string]any
	if json.Unmarshal(providerErr.Metadata, &metadata) != nil ||
		metadata["event"] != "response.error" || metadata["raw_event_bytes"] != float64(len(rawEvent)) {
		t.Fatalf("malformed error metadata = %s", providerErr.Metadata)
	}
}

func TestOpenAIConsumeStreamMalformedEventIsAmbiguous(t *testing.T) {
	stream := openAISSE(
		[2]string{"response.output_text.delta", `{"item_id":`},
	)
	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("malformed unterminated stream = %T %v, want ambiguous outcome", err, err)
	}
}

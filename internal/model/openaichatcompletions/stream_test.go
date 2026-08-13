package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type chatRecordingSink struct {
	events []model.StreamEvent
}

func (s *chatRecordingSink) Emit(_ context.Context, event model.StreamEvent) {
	s.events = append(s.events, event)
}

func (s *chatRecordingSink) kinds() []model.StreamEventKind {
	kinds := make([]model.StreamEventKind, 0, len(s.events))
	for _, event := range s.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func chatCompletionsSSE(data ...string) string {
	var b strings.Builder
	for _, item := range data {
		b.WriteString("data: " + item + "\n\n")
	}
	return b.String()
}

func consumeChatCompletionsStream(
	t *testing.T,
	stream string,
	sink model.StreamSink,
	apiVariant modelprotocol.APIVariant,
) (model.Response, error) {
	t.Helper()
	p := protocol{client: Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "anthropic/claude-sonnet-4",
		APIVariant:        apiVariant,
	}}
	return p.ConsumeStream(context.Background(), strings.NewReader(stream), http.StatusOK, http.Header{}, sink)
}

func assertChatStreamClosedBeforeError(t *testing.T, sink *chatRecordingSink) {
	t.Helper()
	kinds := sink.kinds()
	if len(kinds) < 2 ||
		kinds[len(kinds)-2] != model.StreamEventBlockStop ||
		kinds[len(kinds)-1] != model.StreamEventError {
		t.Fatalf("terminal stream events = %v, want block_stop followed by error", kinds)
	}
}

func TestChatCompletionsBuildStreamRequestSetsStream(t *testing.T) {
	p := protocol{client: Client{EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test"}}
	body, err := p.BuildStreamRequest(
		json.RawMessage(`{"model":"gpt-test","stream":false,"stream_options":{"include_usage":false,"extra":"kept"}}`),
	)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode stream request: %v", err)
	}
	if payload["stream"] != true {
		t.Fatalf("stream request = %s, want stream true", body)
	}
	streamOptions, ok := payload["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options = %T, want object in %s", payload["stream_options"], body)
	}
	if streamOptions["include_usage"] != true {
		t.Fatalf("stream_options.include_usage = %v, want true in %s", streamOptions["include_usage"], body)
	}
	if streamOptions["extra"] != "kept" {
		t.Fatalf("stream_options.extra = %v, want kept in %s", streamOptions["extra"], body)
	}
}

func TestChatCompletionsBuildStreamRequestLeavesOpenRouterUsageOptionsAlone(t *testing.T) {
	p := protocol{client: Client{EndpointPath: testEndpointPath,
		ProviderModelSlug: "anthropic/claude-sonnet-4",
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}}
	body, err := p.BuildStreamRequest(json.RawMessage(`{"model":"anthropic/claude-sonnet-4"}`))
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode stream request: %v", err)
	}
	if string(payload["stream"]) != "true" {
		t.Fatalf("stream request = %s, want stream true", body)
	}
	if _, ok := payload["stream_options"]; ok {
		t.Fatalf("openrouter stream request should not inject stream_options: %s", body)
	}
}

func TestChatCompletionsConsumeStreamEmitsDeltasAndParsesResponse(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"reasoning":"Checked ","reasoning_details":[{"type":"reasoning.text","text":"Checked ","id":"rs_1","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"reasoning":"the command plan","reasoning_details":[{"type":"reasoning.text","text":"the command plan","id":"rs_1","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"","signature":"sig_1","id":"rs_1","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"content":"Hello "}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"content":"world"}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"run_command","arguments":"{\"command\":"}}]}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pwd\"}"}}]}}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl_1","model":"anthropic/claude-sonnet-4","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.0000125,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`,
		`[DONE]`,
	)
	sink := &chatRecordingSink{}
	resp, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantOpenRouter)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if resp.ID != "chatcmpl_1" || resp.ServedProviderModelSlug != "anthropic/claude-sonnet-4" {
		t.Fatalf("unexpected response identity: %+v", resp)
	}
	if resp.Text() != "Hello world" {
		t.Fatalf("unexpected response text: %q", resp.Text())
	}
	if resp.StopReason != model.StopReasonToolUse {
		t.Fatalf("unexpected stop reason: %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 ||
		resp.Usage.OutputTokens != 5 ||
		resp.Usage.CacheReadTokens != 2 ||
		resp.Usage.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if resp.ProviderReportedCostUSD != "0.0000125" {
		t.Fatalf("provider-reported cost = %q, want 0.0000125", resp.ProviderReportedCostUSD)
	}
	if len(resp.Content) != 3 ||
		resp.Content[0].Type != model.ResponsePartTypeReasoning ||
		resp.Content[0].Text != "Checked the command plan" ||
		resp.Content[1].Type != model.ResponsePartTypeText ||
		resp.Content[2].Type != model.ResponsePartTypeToolCall {
		t.Fatalf("unexpected content parts: %+v", resp.Content)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 ||
		calls[0].ID != "call_1" ||
		calls[0].Name != "run_command" ||
		string(calls[0].Input) != `{"command":"pwd"}` {
		t.Fatalf("unexpected tool calls: %+v", calls)
	}
	replayItem := resp.ProviderReplay
	if !json.Valid(replayItem) {
		t.Fatalf("expected request-valid OpenRouter assistant replay, got %s", replayItem)
	}
	var replay struct {
		ReasoningDetails []json.RawMessage `json:"reasoning_details"`
	}
	if err := json.Unmarshal(replayItem, &replay); err != nil {
		t.Fatalf("decode assistant replay: %v", err)
	}
	if len(replay.ReasoningDetails) != 1 {
		t.Fatalf("expected merged reasoning_details entry, got %s", replayItem)
	}
	var detail struct {
		Text      string `json:"text"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(replay.ReasoningDetails[0], &detail); err != nil {
		t.Fatalf("decode reasoning detail: %v", err)
	}
	if detail.Text != "Checked the command plan" || detail.Signature != "sig_1" {
		t.Fatalf("reasoning detail was not merged canonically: %s", replay.ReasoningDetails[0])
	}
	var thinkingDeltas strings.Builder
	for _, event := range sink.events {
		if event.Kind == model.StreamEventThinkingDelta {
			thinkingDeltas.WriteString(event.Delta)
		}
	}
	if thinkingDeltas.String() != "Checked the command plan" {
		t.Fatalf("thinking deltas = %q, want single mirrored reasoning stream", thinkingDeltas.String())
	}
	wantKinds := []model.StreamEventKind{
		model.StreamEventBlockStart,
		model.StreamEventThinkingDelta,
		model.StreamEventThinkingDelta,
		model.StreamEventBlockStart,
		model.StreamEventTextDelta,
		model.StreamEventTextDelta,
		model.StreamEventBlockStart,
		model.StreamEventToolArgsDelta,
		model.StreamEventToolArgsDelta,
		model.StreamEventBlockStop,
		model.StreamEventBlockStop,
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
	if sink.events[6].Block == nil ||
		sink.events[6].Block.ToolCallID != "call_1" ||
		sink.events[6].Block.ToolName != "run_command" {
		t.Fatalf("tool block metadata missing: %+v", sink.events[6])
	}
}

func TestChatCompletionsConsumeStreamIgnoresUnrecognizedCost(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_cost","model":"gpt-test","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl_cost","model":"gpt-test","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":{"total":0.01}}}`,
		`[DONE]`,
	)
	for _, variant := range []modelprotocol.APIVariant{
		modelprotocol.APIVariantDefault,
		modelprotocol.APIVariantOpenRouter,
	} {
		t.Run(string(variant), func(t *testing.T) {
			response, err := consumeChatCompletionsStream(
				t,
				stream,
				&chatRecordingSink{},
				variant,
			)
			if err != nil {
				t.Fatalf("consume stream with unrecognized cost extension: %v", err)
			}
			if response.ProviderReportedCostUSD != "" {
				t.Fatalf("provider-reported cost = %q, want unknown", response.ProviderReportedCostUSD)
			}
		})
	}
}

func TestChatCompletionsConsumeStreamBuffersToolArgsUntilMetadata(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}`,
		`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{}}]}}]}`,
		`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"run_command"}}]}}]}`,
		`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	sink := &chatRecordingSink{}
	resp, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantDefault)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 ||
		calls[0].ID != "call_1" ||
		calls[0].Name != "run_command" ||
		string(calls[0].Input) != `{"command":"pwd"}` {
		t.Fatalf("unexpected tool calls: %+v", calls)
	}
	wantKinds := []model.StreamEventKind{
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
	if sink.events[0].Block == nil ||
		sink.events[0].Block.ToolCallID != "call_1" ||
		sink.events[0].Block.ToolName != "run_command" {
		t.Fatalf("tool metadata was not applied before buffered args flushed: %+v", sink.events[0])
	}
	if sink.events[1].Delta != `{"command":` || sink.events[2].Delta != `"pwd"}` {
		t.Fatalf("unexpected tool argument deltas: %+v", sink.events)
	}
}

func TestChatCompletionsClientRespondStreamsWhenSinkPresent(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chatCompletionsSSE(
			`{"id":"chatcmpl_stream","model":"gpt-test","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)))
	}))
	defer server.Close()

	client := Client{EndpointPath: testEndpointPath,
		Auth:              route.BearerToken{Token: "test-key"},
		BaseURL:           server.URL,
		ProviderModelSlug: "gpt-test",
		HTTPClient:        server.Client(),
	}
	sink := &chatRecordingSink{}
	resp, err := client.Respond(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(
			`{"model":"gpt-test","messages":[],"stream":true,"stream_options":{"include_usage":true}}`,
		),
		DeltaSink: sink,
	})
	if err != nil {
		t.Fatalf("respond stream: %v", err)
	}
	if sent["stream"] != true {
		t.Fatalf("stream request body = %+v, want stream true", sent)
	}
	streamOptions, ok := sent["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %+v, want include_usage true", sent["stream_options"])
	}
	if resp.Text() != "ok" {
		t.Fatalf("response text = %q, want ok", resp.Text())
	}
	if len(sink.events) == 0 || sink.events[len(sink.events)-1].Kind != model.StreamEventMessageStop {
		t.Fatalf("expected streamed message stop event, got %+v", sink.events)
	}
}

func TestChatCompletionsConsumeStreamTruncatedIsError(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_partial","model":"gpt-test","choices":[{"index":0,"delta":{"content":"partial"}}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":2}}`,
	)
	sink := &chatRecordingSink{}
	resp, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantDefault)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != model.ErrorKindTransient {
		t.Fatalf("expected transient provider error, got %v", err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("truncated stream = %T %v, want ambiguous outcome", err, err)
	}
	if resp.ID != "chatcmpl_partial" || resp.ServedProviderModelSlug != "gpt-test" ||
		resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("truncated stream evidence = %+v", resp)
	}
	assertChatStreamClosedBeforeError(t, sink)
}

func TestChatCompletionsConsumeMalformedStreamClosesOpenBlock(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_partial","model":"gpt-test","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"choices":`,
	)
	sink := &chatRecordingSink{}
	_, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantDefault)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("malformed stream = %T %v, want ambiguous outcome", err, err)
	}
	assertChatStreamClosedBeforeError(t, sink)
}

func TestChatCompletionsConsumeStreamRejectsNonStringDeltasBeforeEmission(t *testing.T) {
	tests := []struct {
		name  string
		delta string
	}{
		{name: "content", delta: `{"content":{"text":"answer"}}`},
		{name: "refusal", delta: `{"refusal":["no"]}`},
		{name: "reasoning", delta: `{"content":"must not emit","reasoning":{"text":"thought"}}`},
		{name: "reasoning content", delta: `{"reasoning_content":7}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := chatCompletionsSSE(
				`{"id":"chatcmpl_invalid","model":"gpt-test","choices":[{"index":0,"delta":` + test.delta + `}]}`,
			)
			sink := &chatRecordingSink{}
			response, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantDefault)
			if !model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("non-string delta = %T %v, want ambiguous outcome", err, err)
			}
			if response.ID != "chatcmpl_invalid" || response.ServedProviderModelSlug != "gpt-test" {
				t.Fatalf("safe response evidence = %+v", response)
			}
			if len(sink.events) != 1 || sink.events[0].Kind != model.StreamEventError {
				t.Fatalf("events before malformed delta rejection = %+v", sink.events)
			}
		})
	}
}

func TestChatCompletionsConsumeStreamRejectsAlternativeChoiceBeforeEmission(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_alternative","model":"gpt-test","choices":[{"index":1,"delta":{"content":"must not emit"},"finish_reason":"stop"}]}`,
	)
	sink := &chatRecordingSink{}
	response, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantDefault)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("alternative choice = %T %v, want ambiguous outcome", err, err)
	}
	if response.ID != "chatcmpl_alternative" || response.ServedProviderModelSlug != "gpt-test" {
		t.Fatalf("safe response evidence = %+v", response)
	}
	if len(sink.events) != 1 || sink.events[0].Kind != model.StreamEventError {
		t.Fatalf("events before alternative rejection = %+v", sink.events)
	}
}

func TestChatCompletionsConsumeStreamRejectsMultipleChoicesBeforeEmission(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_multiple","model":"gpt-test","choices":[{"index":0,"delta":{"content":"must not emit"}},{"index":1,"delta":{"content":"alternative"}}]}`,
	)
	sink := &chatRecordingSink{}
	response, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantDefault)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("multiple choices = %T %v, want ambiguous outcome", err, err)
	}
	if response.ID != "chatcmpl_multiple" || response.ServedProviderModelSlug != "gpt-test" {
		t.Fatalf("safe response evidence = %+v", response)
	}
	if len(sink.events) != 1 || sink.events[0].Kind != model.StreamEventError {
		t.Fatalf("events before multiple-choice rejection = %+v", sink.events)
	}
}

func TestChatCompletionsConsumeStreamMissingDoneAfterFinishIsError(t *testing.T) {
	for _, apiVariant := range []modelprotocol.APIVariant{
		modelprotocol.APIVariantDefault,
		modelprotocol.APIVariantOpenRouter,
	} {
		t.Run(string(apiVariant), func(t *testing.T) {
			stream := chatCompletionsSSE(
				`{"id":"chatcmpl_partial","model":"gpt-test","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"stop"}]}`,
			)
			sink := &chatRecordingSink{}
			_, err := consumeChatCompletionsStream(t, stream, sink, apiVariant)
			var providerErr model.ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != model.ErrorKindTransient {
				t.Fatalf("expected transient provider error, got %v", err)
			}
			if !model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("stream missing DONE = %T %v, want ambiguous outcome", err, err)
			}
			last := sink.events[len(sink.events)-1]
			if last.Kind != model.StreamEventError {
				t.Fatalf("expected terminal error event, got %+v", last)
			}
		})
	}
}

func TestChatCompletionsConsumeBedrockStreamAllowsCleanEOFAfterFinishAndUsage(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_bedrock","model":"openai.gpt-oss-20b","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"id":"chatcmpl_bedrock","model":"openai.gpt-oss-20b","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		`{"id":"chatcmpl_bedrock","model":"openai.gpt-oss-20b","choices":[],"usage":{"prompt_tokens":29,"completion_tokens":64}}`,
	)
	sink := &chatRecordingSink{}
	resp, err := consumeChatCompletionsStream(t, stream, sink, modelprotocol.APIVariantBedrock)
	if err != nil {
		t.Fatalf("Bedrock stream: %v", err)
	}
	if resp.Text() != "partial" || resp.StopReason != model.StopReasonMaxTokens ||
		resp.Usage.InputTokens != 29 || resp.Usage.OutputTokens != 64 {
		t.Fatalf("Bedrock stream response = %+v", resp)
	}
	if len(sink.events) == 0 || sink.events[len(sink.events)-1].Kind != model.StreamEventMessageStop {
		t.Fatalf("Bedrock terminal stream events = %+v", sink.events)
	}
}

func TestChatCompletionsConsumeBedrockStreamWithoutUsageIsAmbiguous(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_bedrock","model":"openai.gpt-oss-20b","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"id":"chatcmpl_bedrock","model":"openai.gpt-oss-20b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	_, err := consumeChatCompletionsStream(
		t,
		stream,
		&chatRecordingSink{},
		modelprotocol.APIVariantBedrock,
	)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("Bedrock stream without usage = %T %v, want ambiguous outcome", err, err)
	}
}

func TestChatCompletionsConsumeBedrockStreamWithoutFinishIsAmbiguous(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_bedrock","model":"openai.gpt-oss-20b","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"id":"chatcmpl_bedrock","model":"openai.gpt-oss-20b","choices":[],"usage":{"prompt_tokens":29,"completion_tokens":12}}`,
	)
	_, err := consumeChatCompletionsStream(
		t,
		stream,
		&chatRecordingSink{},
		modelprotocol.APIVariantBedrock,
	)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("Bedrock stream without finish = %T %v, want ambiguous outcome", err, err)
	}
}

func TestChatCompletionsConsumeStreamDoneWithoutFinishIsAmbiguous(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_partial","model":"gpt-test","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`[DONE]`,
	)
	_, err := consumeChatCompletionsStream(t, stream, &chatRecordingSink{}, modelprotocol.APIVariantDefault)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("stream DONE without finish = %T %v, want ambiguous outcome", err, err)
	}
}

func TestChatCompletionsConsumeStreamClassifiesOpenRouterMidStreamError(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_err","request_id":"req_stream","retry_after":37,"object":"chat.completion.chunk",`+
			`"created":123,"model":"openai/gpt-5","provider":"openai","error":{"code":502,`+
			`"message":"Provider disconnected unexpectedly","request_id":"req_error","retry_after":38,`+
			`"metadata":{"error_type":"provider_unavailable","request_id":"req_metadata","retry_after":39}},`+
			`"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}],`+
			`"usage":{"prompt_tokens":29,"completion_tokens":4,"cost":0.0000042}}`,
		`{"id":`,
	)
	resp, err := consumeChatCompletionsStream(t, stream, &chatRecordingSink{}, modelprotocol.APIVariantOpenRouter)
	var providerErr model.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
	if providerErr.Kind != model.ErrorKindProviderUnavailable ||
		providerErr.StatusCode != http.StatusBadGateway ||
		providerErr.Code != "provider_unavailable" ||
		providerErr.RequestID != "" || providerErr.RetryAfter != nil {
		t.Fatalf("unexpected provider error: %+v", providerErr)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("typed mid-stream error must be explicit: %T %v", err, err)
	}
	if resp.ID != "chatcmpl_err" || resp.ServedProviderModelSlug != "openai/gpt-5" ||
		resp.Usage.InputTokens != 29 || resp.Usage.OutputTokens != 4 ||
		resp.ProviderReportedCostUSD != "0.0000042" {
		t.Fatalf("failed stream evidence = %+v", resp)
	}
}

func TestChatCompletionsConsumeStreamClassifiesErrorBeforeSuccessPayloadValidation(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{name: "escaped NUL", message: `quota\u0000 exhausted`},
		{name: "invalid UTF-8", message: "quota " + string([]byte{0xff}) + " exhausted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := chatCompletionsSSE(
				`{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"` +
					test.message + `"},"request_id":"req_quota"}`,
			)
			_, err := consumeChatCompletionsStream(
				t,
				stream,
				&chatRecordingSink{},
				modelprotocol.APIVariantDefault,
			)
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != model.ErrorKindBillingAccount ||
				providerErr.Code != "insufficient_quota" || providerErr.RequestID != "" ||
				model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("streamed quota error = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestChatCompletionsConsumeStreamPreservesFinishErrorEvidence(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_err","request_id":"req_finish","retry_after":41,` +
			`"choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`,
	)
	_, err := consumeChatCompletionsStream(t, stream, &chatRecordingSink{}, modelprotocol.APIVariantOpenRouter)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "finish_reason_error" || providerErr.RequestID != "" ||
		providerErr.RetryAfter != nil {
		t.Fatalf("finish error evidence = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("finish_reason error must be explicit: %T %v", err, err)
	}
}

func TestChatCompletionsConsumeStreamLengthIsSuccessfulMaxTokens(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_partial","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant",`+
			`"reasoning":"still thinking","content":"partial","tool_calls":[{"index":0,"id":"call_partial",`+
			`"type":"function","function":{"name":"run_command","arguments":"{"}}]},"finish_reason":"length"}]}`,
		`[DONE]`,
	)
	resp, err := consumeChatCompletionsStream(t, stream, &chatRecordingSink{}, modelprotocol.APIVariantDefault)
	if err != nil {
		t.Fatalf("length stream: %v", err)
	}
	if resp.StopReason != model.StopReasonMaxTokens || resp.Text() != "partial" ||
		resp.HasToolCalls() || len(resp.ProviderReplay) != 0 || len(resp.Content) != 2 ||
		resp.Content[0].Type != model.ResponsePartTypeReasoning ||
		resp.Content[0].Text != "still thinking" {
		t.Fatalf("length stream response = %+v", resp)
	}
}

func TestChatCompletionsConsumeStreamRejectsMissingToolArguments(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_bad_tool","model":"gpt-test","choices":[{"index":0,"delta":{`+
			`"role":"assistant","tool_calls":[{"index":0,"id":"call_bad","type":"function",`+
			`"function":{"name":"run_command"}}]},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	_, err := consumeChatCompletionsStream(
		t,
		stream,
		&chatRecordingSink{},
		modelprotocol.APIVariantDefault,
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" ||
		!model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("missing streamed tool arguments = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestChatCompletionsConsumeMalformedStreamIsAmbiguous(t *testing.T) {
	stream := chatCompletionsSSE(`{"id":`)
	_, err := consumeChatCompletionsStream(t, stream, &chatRecordingSink{}, modelprotocol.APIVariantDefault)
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("malformed stream = %T %v, want ambiguous outcome", err, err)
	}
}

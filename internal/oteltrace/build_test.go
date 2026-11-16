package oteltrace

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func attribute(t *testing.T, span Span, key string) string {
	t.Helper()
	for _, kv := range span.Attributes {
		if kv.Key != key {
			continue
		}
		switch {
		case kv.Value.StringValue != nil:
			return *kv.Value.StringValue
		case kv.Value.IntValue != nil:
			return *kv.Value.IntValue
		}
	}
	return ""
}

func testTurn() Turn {
	start := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	failed, first, second := uuid.MustParse("00000000-0000-7000-8000-0000000000a0"),
		uuid.MustParse("00000000-0000-7000-8000-0000000000a1"), uuid.MustParse("00000000-0000-7000-8000-0000000000a2")
	tool := uuid.MustParse("00000000-0000-7000-8000-0000000000b1")
	return Turn{
		TurnID: uuid.MustParse("01938a5c-0000-7000-8000-00000000abcd"), TurnSequence: 3, AgentName: "researcher",
		OrgID: "org_1", ProjectID: "proj_1", AgentID: "agt_1", RootAgentID: "agt_root", PublicTurnID: "trn_1",
		ModelCalls: []ModelCall{
			{ID: failed, Operation: "normal", Attempt: 1, State: "failed", APIFormat: "anthropic-messages",
				RequestModel: "claude", ErrorKind: "provider_overloaded", ErrorMessage: "overloaded",
				StartedAt: at(1), CompletedAt: at(2)},
			{ID: first, Operation: "normal", Attempt: 2, State: "succeeded", APIFormat: "anthropic-messages",
				RequestModel: "claude", ResponseModel: "claude-served", ResponseID: "msg_1", StopReason: "tool_use",
				CostUSD: "0.0125", StartedAt: at(2), CompletedAt: at(4), InputTokens: 100, CacheReadTokens: 40, OutputTokens: 20},
			{ID: second, Operation: "normal", Attempt: 1, State: "succeeded", APIFormat: "openai-responses",
				RequestModel: "claude", StopReason: "end_turn", StartedAt: at(6), CompletedAt: at(8)},
		},
		ToolCalls: []ToolCall{{ID: tool, Name: "search", ProviderCallID: "call_1", Input: json.RawMessage(`{"q":"otel"}`),
			Outcome: "succeeded", StartedAt: at(4), CompletedAt: at(6)}},
		Events: []Event{
			{Sequence: 1, Kind: EventKindAgentInput, IsOpening: true, CreatedAt: at(0),
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"find otel docs"}]`)},
			{Sequence: 2, Kind: EventKindModelOutput, ModelCallID: first, StopReason: "tool_use", CreatedAt: at(4),
				ContentBlocks: json.RawMessage(`[{"type":"tool_call","tool_call_id":"` + tool.String() +
					`","name":"search","input":{"q":"otel"}}]`)},
			{Sequence: 3, Kind: EventKindToolResult, ToolCallID: tool, CreatedAt: at(6),
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"3 results"}]`)},
			{Sequence: 4, Kind: EventKindModelOutput, ModelCallID: second, StopReason: "end_turn", CreatedAt: at(8),
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"here they are"}]`)},
		},
	}
}

func TestBuildRendersTurnAsOneTrace(t *testing.T) {
	turn := testTurn()
	spans := Build(turn, VariantOTel)
	if len(spans) != 5 {
		t.Fatalf("expected root, three chat and one tool span, got %d", len(spans))
	}
	root := spans[0]
	if root.TraceID != "01938a5c00007000800000000000abcd" || len(root.SpanID) != 16 || root.ParentSpanID != "" {
		t.Fatalf("unexpected root identity: %+v", root)
	}
	if root.Name != "invoke_agent researcher" || root.Kind != SpanKindInternal {
		t.Fatalf("unexpected root span: %s kind %d", root.Name, root.Kind)
	}
	wantInput := `[{"role":"user","parts":[{"type":"text","content":"find otel docs"}]}]`
	if got := attribute(t, root, "gen_ai.input.messages"); got != wantInput {
		t.Fatalf("unexpected root input: %s", got)
	}
	seen := map[string]bool{}
	for _, span := range spans {
		if span.TraceID != root.TraceID || seen[span.SpanID] {
			t.Fatalf("span %s has a wrong trace or duplicate ID", span.Name)
		}
		seen[span.SpanID] = true
		if span.SpanID != root.SpanID && span.ParentSpanID != root.SpanID {
			t.Fatalf("span %s is not parented to the root", span.Name)
		}
	}

	failed, chat, tool := spans[1], spans[2], spans[4]
	if failed.Status == nil || failed.Status.Code != StatusCodeError ||
		attribute(t, failed, "error.type") != "provider_overloaded" {
		t.Fatalf("unexpected failed attempt span: %+v", failed)
	}
	if chat.Name != "chat claude" || chat.Kind != SpanKindClient {
		t.Fatalf("unexpected chat span: %+v", chat)
	}
	for key, want := range map[string]string{
		"gen_ai.provider.name": "anthropic", "gen_ai.response.model": "claude-served",
		"gen_ai.usage.input_tokens": "100", "gen_ai.usage.cache_read.input_tokens": "40",
		"gen_ai.usage.output_tokens": "20", "omnara.model_call.cost_usd": "0.0125", "omnara.turn.id": "trn_1",
		"gen_ai.output.messages": `[{"role":"assistant","parts":[{"type":"tool_call","id":"call_1","name":"search","arguments":{"q":"otel"}}]}]`,
	} {
		if got := attribute(t, chat, key); got != want {
			t.Fatalf("chat %s = %q, want %q", key, got, want)
		}
	}
	if tool.Name != "execute_tool search" ||
		attribute(t, tool, "gen_ai.tool.call.id") != "call_1" ||
		attribute(t, tool, "gen_ai.tool.call.arguments") != `{"q":"otel"}` ||
		attribute(t, tool, "gen_ai.tool.call.result") != "3 results" {
		t.Fatalf("unexpected tool span: %+v", tool)
	}
	if tool.StartTimeUnixNano != "1794830404000000000" || tool.EndTimeUnixNano != "1794830406000000000" {
		t.Fatalf("unexpected tool times: %s to %s", tool.StartTimeUnixNano, tool.EndTimeUnixNano)
	}
}

func TestBuildRendersRunningTurnSoFar(t *testing.T) {
	turn := testTurn()
	turn.Events = turn.Events[:3]
	turn.ModelCalls = turn.ModelCalls[:2]
	spans := Build(turn, VariantOTel)
	if len(spans) != 4 || attribute(t, spans[0], "gen_ai.output.messages") != "" {
		t.Fatalf("a running turn has no final output on its root yet: %+v", spans[0])
	}
	// The root ends at the latest event so far.
	if spans[0].EndTimeUnixNano != spans[3].EndTimeUnixNano {
		t.Fatalf("root should end with the tool result, got %s", spans[0].EndTimeUnixNano)
	}
}

func TestVariantsAddVendorAttributes(t *testing.T) {
	turn := testTurn()
	tests := []struct {
		variant    Variant
		role       int
		key, value string
	}{
		{VariantLangfuse, 0, "langfuse.observation.type", "agent"},
		{VariantLangfuse, 0, "langfuse.session.id", "agt_root"},
		{VariantLangfuse, 2, "langfuse.observation.type", "generation"},
		{VariantLangfuse, 2, "langfuse.observation.cost_details", `{"total":0.0125}`},
		{VariantLangfuse, 4, "langfuse.observation.output", "3 results"},
		{VariantBraintrust, 2, "braintrust.span_attributes", `{"type":"llm"}`},
		{VariantBraintrust, 4, "braintrust.input_json", `{"q":"otel"}`},
		{VariantBraintrust, 0, "braintrust.output_json", `"here they are"`},
		{VariantLangSmith, 4, "langsmith.span.kind", "tool"},
		{VariantLangSmith, 0, "gen_ai.prompt", "find otel docs"},
		{VariantLangSmith, 0, "langsmith.metadata.thread_id", "agt_root"},
	}
	for _, test := range tests {
		spans := Build(turn, test.variant)
		if got := attribute(t, spans[test.role], test.key); got != test.value {
			t.Errorf("%s span %d %s = %q, want %q", test.variant, test.role, test.key, got, test.value)
		}
	}
	for _, span := range Build(turn, VariantOTel) {
		for _, kv := range span.Attributes {
			for _, prefix := range []string{"langfuse.", "braintrust.", "langsmith."} {
				if len(kv.Key) >= len(prefix) && kv.Key[:len(prefix)] == prefix {
					t.Errorf("otel variant must not add vendor attribute %s", kv.Key)
				}
			}
		}
	}
}

func TestParseVariant(t *testing.T) {
	if variant, ok := ParseVariant(""); !ok || variant != VariantOTel {
		t.Fatalf("empty variant should default to otel, got %q %v", variant, ok)
	}
	if _, ok := ParseVariant("datadog"); ok {
		t.Fatal("unknown variant must be rejected")
	}
}

func TestSampledUsesLowBitsOfTraceID(t *testing.T) {
	// The low 32 bits are 0x0000abcd = 43981, bucket 43981 % 10000 = 3981.
	turnID := uuid.MustParse("01938a5c-0000-7000-8000-00000000abcd")
	if !Sampled(turnID, 10000) || Sampled(turnID, 0) || !Sampled(turnID, 3982) || Sampled(turnID, 3981) {
		t.Fatal("sampling must compare bucket 3981 against the basis points")
	}
}

func TestNewExportIsOTLPJSON(t *testing.T) {
	encoded, err := json.Marshal(NewExport(Build(testTurn(), VariantOTel)))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []map[string]any `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ResourceSpans) != 1 || len(decoded.ResourceSpans[0].ScopeSpans[0].Spans) != 5 {
		t.Fatalf("unexpected export shape: %s", encoded)
	}
	if _, ok := decoded.ResourceSpans[0].ScopeSpans[0].Spans[0]["startTimeUnixNano"].(string); !ok {
		t.Fatal("OTLP/JSON timestamps must be strings")
	}
}

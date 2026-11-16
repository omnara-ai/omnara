package oteltrace

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
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
			{Kind: events.KindAgentInput, IsOpening: true, CreatedAt: at(0),
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"find otel docs"}]`)},
			{Kind: events.KindModelOutput, ModelCallID: first, StopReason: "tool_use", CreatedAt: at(4),
				ContentBlocks: json.RawMessage(`[{"type":"tool_call","tool_call_id":"` + tool.String() +
					`","name":"search","input":{"q":"otel"}}]`)},
			{Kind: events.KindToolResult, ToolCallID: tool, CreatedAt: at(6),
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"3 results"}]`)},
			{Kind: events.KindModelOutput, ModelCallID: second, StopReason: "end_turn", CreatedAt: at(8),
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
}

func TestBuildSkipsTurnWithNothingToPlace(t *testing.T) {
	turn := testTurn()
	turn.Events, turn.ModelCalls, turn.ToolCalls = nil, nil, nil
	if spans := Build(turn, VariantOTel); len(spans) != 0 {
		t.Fatalf("a turn with no events or calls has no spans, got %+v", spans)
	}
}

func TestBuildKeepsTruncatedTurnConsistent(t *testing.T) {
	turn := testTurn()
	// Only the final output survived truncation; the calls are all still loaded.
	turn.Events, turn.Truncated = turn.Events[3:], true
	spans := Build(turn, VariantOTel)
	if len(spans) != 5 {
		t.Fatalf("truncation must not drop call spans, got %d", len(spans))
	}
	root, tool := spans[0], spans[4]
	if len(root.Attributes) == 0 || !hasBool(root, "omnara.trace.truncated") {
		t.Fatalf("root must be marked truncated: %+v", root)
	}
	if root.StartTimeUnixNano != spans[1].StartTimeUnixNano || root.EndTimeUnixNano != spans[3].EndTimeUnixNano {
		t.Fatalf("root must cover its children, got %s to %s", root.StartTimeUnixNano, root.EndTimeUnixNano)
	}
	if tool.Name != "execute_tool search" || attribute(t, tool, "gen_ai.tool.call.result") != "" {
		t.Fatalf("tool span without its result event keeps its identity only: %+v", tool)
	}
}

func hasBool(span Span, key string) bool {
	for _, kv := range span.Attributes {
		if kv.Key == key && kv.Value.BoolValue != nil && *kv.Value.BoolValue {
			return true
		}
	}
	return false
}

func TestBuildDoesNotCloseTurnOnTruncatedOutput(t *testing.T) {
	turn := testTurn()
	turn.Events[3].StopReason = "max_tokens"
	if got := attribute(t, Build(turn, VariantOTel)[0], "gen_ai.output.messages"); got != "" {
		t.Fatalf("a max_tokens output is continued, not final: %s", got)
	}
}

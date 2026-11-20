package oteltrace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func attribute(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.AsString()
}

func buildSpans(turn Turn, variant Variant) []ptrace.Span {
	var spans []ptrace.Span
	for _, span := range Build(turn, variant).ResourceSpans().At(0).ScopeSpans().At(0).Spans().All() {
		spans = append(spans, span)
	}
	return spans
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
				CostUSD: "0.0125", StartedAt: at(2), CompletedAt: at(4),
				InputTokens: 100, CacheReadTokens: new(int64(40)), OutputTokens: 20},
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
	spans := buildSpans(turn, VariantOTel)
	if len(spans) != 5 {
		t.Fatalf("expected root, three chat and one tool span, got %d", len(spans))
	}
	root := spans[0]
	if root.TraceID().String() != "01938a5c00007000800000000000abcd" || root.SpanID().IsEmpty() ||
		!root.ParentSpanID().IsEmpty() {
		t.Fatalf("unexpected root identity: %s", root.Name())
	}
	if root.Name() != "invoke_agent researcher" || root.Kind() != ptrace.SpanKindInternal {
		t.Fatalf("unexpected root span: %s kind %d", root.Name(), root.Kind())
	}
	wantInput := `[{"role":"user","parts":[{"type":"text","content":"find otel docs"}]}]`
	if got := attribute(t, root, "gen_ai.input.messages"); got != wantInput {
		t.Fatalf("unexpected root input: %s", got)
	}
	seen := map[pcommon.SpanID]bool{}
	for _, span := range spans {
		if span.TraceID() != root.TraceID() || seen[span.SpanID()] {
			t.Fatalf("span %s has a wrong trace or duplicate ID", span.Name())
		}
		seen[span.SpanID()] = true
		if span.SpanID() != root.SpanID() && span.ParentSpanID() != root.SpanID() {
			t.Fatalf("span %s is not parented to the root", span.Name())
		}
	}

	failed, chat, tool := spans[1], spans[2], spans[4]
	if failed.Status().Code() != ptrace.StatusCodeError || failed.Status().Message() != "overloaded" ||
		attribute(t, failed, "error.type") != "provider_overloaded" {
		t.Fatalf("unexpected failed attempt span: %s", failed.Name())
	}
	if chat.Name() != "chat claude" || chat.Kind() != ptrace.SpanKindClient {
		t.Fatalf("unexpected chat span: %s", chat.Name())
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
	if tool.Name() != "execute_tool search" ||
		attribute(t, tool, "gen_ai.tool.call.id") != "call_1" ||
		attribute(t, tool, "gen_ai.tool.call.arguments") != `{"q":"otel"}` ||
		attribute(t, tool, "gen_ai.tool.call.result") != "3 results" {
		t.Fatalf("unexpected tool span: %s", tool.Name())
	}
	if tool.StartTimestamp() != 1794830404000000000 || tool.EndTimestamp() != 1794830406000000000 {
		t.Fatalf("unexpected tool times: %d to %d", tool.StartTimestamp(), tool.EndTimestamp())
	}
}

func TestBuildRendersRunningTurnSoFar(t *testing.T) {
	turn := testTurn()
	turn.Events = turn.Events[:3]
	turn.ModelCalls = turn.ModelCalls[:2]
	spans := buildSpans(turn, VariantOTel)
	if len(spans) != 4 || attribute(t, spans[0], "gen_ai.output.messages") != "" {
		t.Fatalf("a running turn has no final output on its root yet: %s", spans[0].Name())
	}
	if spans[0].EndTimestamp() != spans[3].EndTimestamp() {
		t.Fatalf("root should end with the tool result, got %d", spans[0].EndTimestamp())
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
		{VariantLangfuse, 0, "langfuse.session.id", "agt_1"},
		{VariantLangfuse, 2, "langfuse.observation.type", "generation"},
		{VariantLangfuse, 2, "langfuse.observation.cost_details", `{"total":0.0125}`},
		{VariantLangfuse, 4, "langfuse.observation.output", "3 results"},
		{VariantBraintrust, 2, "braintrust.span_attributes", `{"type":"llm"}`},
		{VariantBraintrust, 4, "braintrust.input_json", `{"q":"otel"}`},
		{VariantBraintrust, 0, "braintrust.output_json", `"here they are"`},
		{VariantLangSmith, 4, "langsmith.span.kind", "tool"},
		{VariantLangSmith, 0, "gen_ai.prompt", "find otel docs"},
		{VariantLangSmith, 0, "langsmith.metadata.thread_id", "agt_1"},
	}
	for _, test := range tests {
		spans := buildSpans(turn, test.variant)
		if got := attribute(t, spans[test.role], test.key); got != test.value {
			t.Errorf("%s span %d %s = %q, want %q", test.variant, test.role, test.key, got, test.value)
		}
	}
}

func TestBuildSkipsTurnWithNothingToPlace(t *testing.T) {
	turn := testTurn()
	turn.Events, turn.ModelCalls, turn.ToolCalls = nil, nil, nil
	if count := Build(turn, VariantOTel).SpanCount(); count != 0 {
		t.Fatalf("a turn with no events or calls has no spans, got %d", count)
	}
}

func TestBuildKeepsTruncatedTurnConsistent(t *testing.T) {
	turn := testTurn()
	// Only the final output survived truncation; the calls are all still loaded.
	turn.Events, turn.Truncated = turn.Events[3:], true
	spans := buildSpans(turn, VariantOTel)
	if len(spans) != 5 {
		t.Fatalf("truncation must not drop call spans, got %d", len(spans))
	}
	root, tool := spans[0], spans[4]
	if attribute(t, root, "omnara.trace.truncated") != "true" {
		t.Fatalf("root must be marked truncated: %s", root.Name())
	}
	if root.StartTimestamp() != spans[1].StartTimestamp() || root.EndTimestamp() != spans[3].EndTimestamp() {
		t.Fatalf("root must cover its children, got %d to %d", root.StartTimestamp(), root.EndTimestamp())
	}
	if tool.Name() != "execute_tool search" || attribute(t, tool, "gen_ai.tool.call.result") != "" {
		t.Fatalf("tool span without its result event keeps its identity only: %s", tool.Name())
	}
}

func TestBuildDoesNotCloseTurnOnTruncatedOutput(t *testing.T) {
	turn := testTurn()
	turn.Events[3].StopReason = "max_tokens"
	if got := attribute(t, buildSpans(turn, VariantOTel)[0], "gen_ai.output.messages"); got != "" {
		t.Fatalf("a max_tokens output is continued, not final: %s", got)
	}
}

func TestBuildLinksTurnsAcrossAgents(t *testing.T) {
	turn := testTurn()
	spawnCall, parentTurn := uuid.MustParse("00000000-0000-7000-8000-0000000000c1"),
		uuid.MustParse("00000000-0000-7000-8000-0000000000c2")
	childTurn := uuid.MustParse("00000000-0000-7000-8000-0000000000c3")
	turn.Links = []Link{{
		Kind: LinkSpawnedBy, AgentID: "agt_parent", TurnID: parentTurn, PublicTurnID: "trn_parent", ToolCallID: spawnCall,
	}}
	turn.ToolCalls[0].Links = []Link{{
		Kind: LinkSpawned, AgentID: "agt_child", TurnID: childTurn, PublicTurnID: "trn_child",
	}}
	spans := buildSpans(turn, VariantOTel)
	root, chat, tool := spans[0], spans[2], spans[4]
	if root.Links().Len() != 1 || chat.Links().Len() != 0 || tool.Links().Len() != 1 {
		t.Fatalf("links belong on the root and the sending tool call, got %d %d %d",
			root.Links().Len(), chat.Links().Len(), tool.Links().Len())
	}
	// A received input links to the tool call that sent it, in the sender's trace.
	received := root.Links().At(0)
	if received.TraceID() != TraceID(parentTurn) || received.SpanID() != spanID("tool_call", spawnCall) {
		t.Fatalf("unexpected link target: %s %s", received.TraceID(), received.SpanID())
	}
	if kind, _ := received.Attributes().Get("omnara.link.kind"); kind.AsString() != LinkSpawnedBy {
		t.Fatalf("unexpected link kind: %s", kind.AsString())
	}
	if agent, _ := received.Attributes().Get("omnara.link.agent.id"); agent.AsString() != "agt_parent" {
		t.Fatalf("unexpected linked agent: %s", agent.AsString())
	}
	if linkedTurn, _ := received.Attributes().Get("omnara.link.turn.id"); linkedTurn.AsString() != "trn_parent" {
		t.Fatalf("unexpected linked turn: %s", linkedTurn.AsString())
	}
	wantSummary := `[{"kind":"spawned_by","agent_id":"agt_parent","turn_id":"trn_parent","trace_id":"` + TraceID(parentTurn).String() +
		`","span_id":"` + spanID("tool_call", spawnCall).String() + `"}]`
	if got := attribute(t, root, "omnara.links"); got != wantSummary {
		t.Fatalf("links are repeated as an attribute for backends that drop them, got %s", got)
	}
	if attribute(t, chat, "omnara.links") != "" {
		t.Fatal("a span without links carries no links attribute")
	}
	// A sent input links to the root span of the turn that received it.
	sent := tool.Links().At(0)
	if sent.TraceID() != TraceID(childTurn) || sent.SpanID() != spanID("turn", childTurn) {
		t.Fatalf("unexpected sent link target: %s %s", sent.TraceID(), sent.SpanID())
	}
}

func TestVariantSessionsAreTheAgentNotItsRoot(t *testing.T) {
	turn := testTurn()
	for variant, key := range map[Variant]string{
		VariantLangfuse: "langfuse.session.id", VariantLangSmith: "langsmith.metadata.thread_id",
	} {
		if got := attribute(t, buildSpans(turn, variant)[0], key); got != "agt_1" {
			t.Errorf("%s %s = %q, want the agent's own ID", variant, key, got)
		}
	}
}

func TestBuildOmitsUsageTheProviderDidNotReport(t *testing.T) {
	chat := buildSpans(testTurn(), VariantOTel)[2]
	for _, key := range []string{"gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.reasoning.output_tokens"} {
		if _, ok := chat.Attributes().Get(key); ok {
			t.Errorf("%s was not reported, so it must not be exported as zero", key)
		}
	}
}

func TestBuildDoesNotCloseTurnOnOutputThatCallsTools(t *testing.T) {
	turn := testTurn()
	// A provider may report end_turn on an output that still calls tools.
	turn.Events, turn.ModelCalls = turn.Events[:3], turn.ModelCalls[:2]
	turn.Events[1].StopReason = "end_turn"
	if got := attribute(t, buildSpans(turn, VariantOTel)[0], "gen_ai.output.messages"); got != "" {
		t.Fatalf("an output that calls tools is followed by another model call: %s", got)
	}
}

func TestBuildKeepsStructuredAndMediaContent(t *testing.T) {
	turn := testTurn()
	turn.Events[2].ContentBlocks = json.RawMessage(
		`[{"type":"structured_data","value":{"hits":3}},{"type":"media_ref","artifact_id":"art_1"}]`)
	turn.ToolCalls[0].Outcome = "failed"
	tool := buildSpans(turn, VariantOTel)[4]
	if got := tool.Status().Message(); got != "{\"hits\":3}\n[media art_1]" {
		t.Fatalf("a failed tool keeps its structured explanation and media references: %q", got)
	}
}

func TestBuildBoundsContentAndReportsIt(t *testing.T) {
	turn := testTurn()
	long, err := json.Marshal(strings.Repeat("é", maxContentValueBytes))
	if err != nil {
		t.Fatal(err)
	}
	turn.Events[2].ContentBlocks = json.RawMessage(`[{"type":"text","text":` + string(long) + `}]`)
	spans := buildSpans(turn, VariantOTel)
	result := attribute(t, spans[4], "gen_ai.tool.call.result")
	if len(result) > maxContentValueBytes || !utf8.ValidString(result) || result == "" {
		t.Fatalf("content must be cut to a valid bounded value, got %d bytes", len(result))
	}
	if attribute(t, spans[0], "omnara.trace.content_truncated") != "true" {
		t.Fatal("cut content must be reported on the root span")
	}
	if _, ok := buildSpans(testTurn(), VariantOTel)[0].Attributes().Get("omnara.trace.content_truncated"); ok {
		t.Fatal("an uncut trace must not be marked")
	}
}

func TestBuildIncludesAllOpeningInputs(t *testing.T) {
	turn := testTurn()
	secondOpening := Event{
		Kind: events.KindAgentInput, IsOpening: true, CreatedAt: turn.Events[0].CreatedAt.Add(100 * time.Millisecond),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"also check Langfuse"}]`),
	}
	turn.Events = append([]Event{turn.Events[0], secondOpening}, turn.Events[1:]...)

	root := buildSpans(turn, VariantOTel)[0]
	want := `[{"role":"user","parts":[{"type":"text","content":"find otel docs"}]},` +
		`{"role":"user","parts":[{"type":"text","content":"also check Langfuse"}]}]`
	if got := attribute(t, root, "gen_ai.input.messages"); got != want {
		t.Fatalf("root input messages = %s, want %s", got, want)
	}
	langfuseRoot := buildSpans(turn, VariantLangfuse)[0]
	if got := attribute(t, langfuseRoot, "langfuse.observation.input"); got != "find otel docs\nalso check Langfuse" {
		t.Fatalf("langfuse root input = %q", got)
	}
	langsmithRoot := buildSpans(turn, VariantLangSmith)[0]
	if got := attribute(t, langsmithRoot, "gen_ai.prompt"); got != "find otel docs\nalso check Langfuse" {
		t.Fatalf("langsmith root input = %q", got)
	}
}

package oteltrace

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type Turn struct {
	TurnID                                                              uuid.UUID
	TurnSequence                                                        int64
	AgentName                                                           string
	OrgID, ProjectID, AgentID, ParentAgentID, RootAgentID, PublicTurnID string

	ModelCalls []ModelCall
	ToolCalls  []ToolCall
	Events     []Event
	// Links point from the turn at what another agent did to send it input.
	Links []Link
	// Truncated is set when the turn has more events than were loaded, so
	// Events holds only the most recent ones.
	Truncated bool
}

type ModelCall struct {
	ID            uuid.UUID
	Operation     string
	Attempt       int32
	State         string
	APIFormat     string
	RequestModel  string
	ResponseModel string
	ResponseID    string
	StopReason    string
	CostUSD       string
	ErrorKind     string
	ErrorMessage  string
	StartedAt     time.Time
	CompletedAt   time.Time

	InputTokens, CacheReadTokens, CacheWriteTokens int64
	OutputTokens, ReasoningTokens                  int64
}

type ToolCall struct {
	ID             uuid.UUID
	Name           string
	ProviderCallID string
	Input          json.RawMessage
	Outcome        string
	StartedAt      time.Time
	CompletedAt    time.Time
	// Links point from a call that sent another agent input at the turn that
	// received it.
	Links []Link
}

// Link kinds name how the linked span relates to the span carrying the link.
const (
	// LinkSpawnedBy is on a subagent's first turn and points at the spawn_agent call.
	LinkSpawnedBy = "spawned_by"
	// LinkMessageFrom is on a turn another agent messaged and points at that call.
	LinkMessageFrom = "message_from"
	// LinkResultFrom is on a parent's turn and points at the subagent turn that reported.
	LinkResultFrom = "result_from"
	// LinkSpawned is on a spawn_agent call and points at the subagent's first turn.
	LinkSpawned = "spawned"
	// LinkMessageTo is on a send_agent_message call and points at the turn it reached.
	LinkMessageTo = "message_to"
)

// Link is a pointer to a span in another agent's trace. Every turn is its own
// trace, so links are what connect an agent to its subagents.
type Link struct {
	Kind string
	// AgentID is the public ID of the agent the linked span belongs to.
	AgentID string
	// TurnID is the linked turn, whose ID is also its trace ID.
	TurnID uuid.UUID
	// ToolCallID is set when the link points at a tool call's span; otherwise
	// it points at the turn's root span.
	ToolCallID uuid.UUID
}

// putLinks adds the links both as OTLP span links and as one JSON attribute,
// because some backends, Langfuse among them, drop span links on ingestion.
func putLinks(span ptrace.Span, links []Link) {
	if len(links) == 0 {
		return
	}
	type linkAttribute struct {
		Kind    string `json:"kind"`
		AgentID string `json:"agent_id"`
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
	}
	summary := make([]linkAttribute, 0, len(links))
	for _, link := range links {
		target := spanID("turn", link.TurnID)
		if link.ToolCallID != uuid.Nil {
			target = spanID("tool_call", link.ToolCallID)
		}
		spanLink := span.Links().AppendEmpty()
		spanLink.SetTraceID(TraceID(link.TurnID))
		spanLink.SetSpanID(target)
		spanLink.Attributes().PutStr("omnara.link.kind", link.Kind)
		spanLink.Attributes().PutStr("omnara.link.agent.id", link.AgentID)
		summary = append(summary, linkAttribute{
			Kind: link.Kind, AgentID: link.AgentID, TraceID: TraceID(link.TurnID).String(), SpanID: target.String(),
		})
	}
	if encoded, err := json.Marshal(summary); err == nil {
		span.Attributes().PutStr("omnara.links", string(encoded))
	}
}

type Event struct {
	Kind          events.Kind
	IsOpening     bool
	ModelCallID   uuid.UUID
	ToolCallID    uuid.UUID
	StopReason    modelenvelope.StopReason
	ContentBlocks json.RawMessage
	CreatedAt     time.Time
}

const (
	scopeName   = "omnara"
	serviceName = "omnara"
)

func TraceID(turnID uuid.UUID) pcommon.TraceID {
	return pcommon.TraceID(turnID)
}

func spanID(kind string, id uuid.UUID) pcommon.SpanID {
	sum := sha256.Sum256(append([]byte(kind+":"), id[:]...))
	return pcommon.SpanID(sum[:8])
}

// Build renders the turn as one trace. A turn in which nothing has happened
// yet has no time to place a span at, so its trace holds no spans.
func Build(turn Turn, variant Variant) ptrace.Traces {
	traces := ptrace.NewTraces()
	resource := traces.ResourceSpans().AppendEmpty()
	resource.Resource().Attributes().PutStr("service.name", serviceName)
	scope := resource.ScopeSpans().AppendEmpty()
	scope.Scope().SetName(scopeName)
	spans := scope.Spans()

	traceID := TraceID(turn.TurnID)
	rootID := spanID("turn", turn.TurnID)
	outputs := make(map[uuid.UUID]Event)
	results := make(map[uuid.UUID]Event)
	providerCallIDs := make(map[string]string, len(turn.ToolCalls))
	for _, call := range turn.ToolCalls {
		providerCallIDs[call.ID.String()] = call.ProviderCallID
	}
	var opening, closing *Event
	// The root spans everything the trace holds, so no child falls outside it.
	var start, end time.Time
	extend := func(times ...time.Time) {
		for _, t := range times {
			if t.IsZero() {
				continue
			}
			if start.IsZero() || t.Before(start) {
				start = t
			}
			if t.After(end) {
				end = t
			}
		}
	}
	for _, call := range turn.ModelCalls {
		extend(call.StartedAt, call.CompletedAt)
	}
	for _, call := range turn.ToolCalls {
		extend(call.StartedAt, call.CompletedAt)
	}
	for i := range turn.Events {
		event := &turn.Events[i]
		extend(event.CreatedAt)
		switch event.Kind {
		case events.KindAgentInput:
			if event.IsOpening && opening == nil {
				opening = event
			}
		case events.KindModelOutput:
			outputs[event.ModelCallID] = *event
			// A truncated output is continued by a later model call.
			if event.StopReason != modelenvelope.StopReasonToolUse && event.StopReason != modelenvelope.StopReasonMaxTokens {
				closing = event
			}
		case events.KindToolResult:
			results[event.ToolCallID] = *event
		case events.KindContextCheckpoint:
		}
	}
	if start.IsZero() {
		return traces
	}

	spans.EnsureCapacity(1 + len(turn.ModelCalls) + len(turn.ToolCalls))
	newSpan := func(id pcommon.SpanID, name string, kind ptrace.SpanKind, start, end time.Time) ptrace.Span {
		span := spans.AppendEmpty()
		span.SetTraceID(traceID)
		span.SetSpanID(id)
		if id != rootID {
			span.SetParentSpanID(rootID)
		}
		span.SetName(strings.TrimSpace(name))
		span.SetKind(kind)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
		return span
	}

	root := newSpan(rootID, "invoke_agent "+turn.AgentName, ptrace.SpanKindInternal, start, end)
	attributes := root.Attributes()
	attributes.PutStr("gen_ai.operation.name", "invoke_agent")
	attributes.PutStr("gen_ai.agent.id", turn.AgentID)
	attributes.PutStr("gen_ai.agent.name", turn.AgentName)
	attributes.PutStr("gen_ai.conversation.id", turn.AgentID)
	turn.putCommonAttributes(attributes)
	if turn.Truncated {
		attributes.PutBool("omnara.trace.truncated", true)
	}
	rootContent := spanContent{}
	if opening != nil {
		rootContent.input = []message{{Role: "user", Parts: parseParts(opening.ContentBlocks, providerCallIDs)}}
	}
	if closing != nil {
		rootContent.output = []message{{Role: "assistant", Parts: parseParts(closing.ContentBlocks, providerCallIDs)}}
		if closing.StopReason == "error" {
			root.Status().SetCode(ptrace.StatusCodeError)
		}
	}
	variant.putAttributes(attributes, spanRoleAgent, turn, rootContent, "")
	putLinks(root, turn.Links)

	for _, call := range turn.ModelCalls {
		span := newSpan(spanID("model_call", call.ID), "chat "+call.RequestModel, ptrace.SpanKindClient,
			call.StartedAt, call.CompletedAt)
		attributes := span.Attributes()
		call.putAttributes(attributes)
		turn.putCommonAttributes(attributes)
		if call.State != "succeeded" {
			span.Status().SetCode(ptrace.StatusCodeError)
			span.Status().SetMessage(call.ErrorMessage)
		}
		content := spanContent{}
		if output, ok := outputs[call.ID]; ok {
			content.output = []message{{Role: "assistant", Parts: parseParts(output.ContentBlocks, providerCallIDs)}}
		}
		variant.putAttributes(attributes, spanRoleModel, turn, content, call.CostUSD)
	}

	for _, call := range turn.ToolCalls {
		// The result event is absent only when the turn's events were truncated.
		result := results[call.ID]
		span := newSpan(spanID("tool_call", call.ID), "execute_tool "+call.Name, ptrace.SpanKindInternal,
			call.StartedAt, call.CompletedAt)
		attributes := span.Attributes()
		attributes.PutStr("gen_ai.operation.name", "execute_tool")
		attributes.PutStr("gen_ai.tool.name", call.Name)
		attributes.PutStr("gen_ai.tool.type", "function")
		attributes.PutStr("gen_ai.tool.call.id", call.ProviderCallID)
		turn.putCommonAttributes(attributes)
		content := spanContent{
			toolArguments: string(call.Input),
			toolResult:    partsText(parseParts(result.ContentBlocks, providerCallIDs)),
		}
		if content.toolArguments != "" {
			attributes.PutStr("gen_ai.tool.call.arguments", content.toolArguments)
		}
		if call.Outcome == "succeeded" {
			attributes.PutStr("gen_ai.tool.call.result", content.toolResult)
		} else {
			attributes.PutStr("error.type", call.Outcome)
			span.Status().SetCode(ptrace.StatusCodeError)
			span.Status().SetMessage(content.toolResult)
		}
		variant.putAttributes(attributes, spanRoleTool, turn, content, "")
		putLinks(span, call.Links)
	}
	return traces
}

func (turn Turn) putCommonAttributes(attributes pcommon.Map) {
	attributes.PutStr("omnara.org.id", turn.OrgID)
	attributes.PutStr("omnara.project.id", turn.ProjectID)
	attributes.PutStr("omnara.agent.id", turn.AgentID)
	attributes.PutStr("omnara.root_agent.id", turn.RootAgentID)
	attributes.PutStr("omnara.turn.id", turn.PublicTurnID)
	attributes.PutInt("omnara.turn.sequence", turn.TurnSequence)
	if turn.ParentAgentID != "" {
		attributes.PutStr("omnara.parent_agent.id", turn.ParentAgentID)
	}
}

func providerName(apiFormat string) string {
	switch {
	case strings.HasPrefix(apiFormat, "anthropic"):
		return "anthropic"
	case strings.HasPrefix(apiFormat, "openai"):
		return "openai"
	default:
		return apiFormat
	}
}

func (call ModelCall) putAttributes(attributes pcommon.Map) {
	attributes.PutStr("gen_ai.operation.name", "chat")
	attributes.PutStr("gen_ai.provider.name", providerName(call.APIFormat))
	attributes.PutStr("gen_ai.request.model", call.RequestModel)
	attributes.PutStr("omnara.model_call.operation", call.Operation)
	attributes.PutInt("omnara.model_call.attempt", int64(call.Attempt))
	if call.ResponseModel != "" {
		attributes.PutStr("gen_ai.response.model", call.ResponseModel)
	}
	if call.ResponseID != "" {
		attributes.PutStr("gen_ai.response.id", call.ResponseID)
	}
	if call.StopReason != "" {
		attributes.PutEmptySlice("gen_ai.response.finish_reasons").AppendEmpty().SetStr(call.StopReason)
	}
	if call.State == "succeeded" {
		attributes.PutInt("gen_ai.usage.input_tokens", call.InputTokens)
		attributes.PutInt("gen_ai.usage.output_tokens", call.OutputTokens)
		attributes.PutInt("gen_ai.usage.cache_read.input_tokens", call.CacheReadTokens)
		attributes.PutInt("gen_ai.usage.cache_write.input_tokens", call.CacheWriteTokens)
		attributes.PutInt("gen_ai.usage.reasoning.output_tokens", call.ReasoningTokens)
	} else if call.ErrorKind != "" {
		attributes.PutStr("error.type", call.ErrorKind)
	}
	if call.CostUSD != "" {
		attributes.PutStr("omnara.model_call.cost_usd", call.CostUSD)
	}
}

type message struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

type part struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func parseParts(raw json.RawMessage, providerCallIDs map[string]string) []part {
	var blocks []struct {
		Type       string          `json:"type"`
		Text       string          `json:"text"`
		ToolCallID string          `json:"tool_call_id"`
		Name       string          `json:"name"`
		Input      json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return []part{}
	}
	parts := make([]part, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "tool_call":
			id := providerCallIDs[block.ToolCallID]
			if id == "" {
				id = block.ToolCallID
			}
			parts = append(parts, part{Type: "tool_call", ID: id, Name: block.Name, Arguments: block.Input})
		default:
			parts = append(parts, part{Type: block.Type, Content: block.Text})
		}
	}
	return parts
}

func partsText(parts []part) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Content != "" && part.Type != "reasoning" {
			texts = append(texts, part.Content)
		}
	}
	return strings.Join(texts, "\n")
}

func messagesJSON(messages []message) string {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func messagesText(messages []message) string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		if text := partsText(message.Parts); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

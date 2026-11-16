package oteltrace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

type Turn struct {
	TurnID                                                              uuid.UUID
	TurnSequence                                                        int64
	AgentName                                                           string
	OrgID, ProjectID, AgentID, ParentAgentID, RootAgentID, PublicTurnID string

	ModelCalls []ModelCall
	ToolCalls  []ToolCall
	Events     []Event
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

func TraceID(turnID uuid.UUID) string {
	return hex.EncodeToString(turnID[:])
}

func spanID(kind string, id uuid.UUID) string {
	sum := sha256.Sum256(append([]byte(kind+":"), id[:]...))
	return hex.EncodeToString(sum[:8])
}

func Build(turn Turn, variant Variant) []Span {
	traceID := TraceID(turn.TurnID)
	rootID := spanID("turn", turn.TurnID)
	common := turn.commonAttributes()
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
		}
	}

	// Nothing has happened in the turn yet, so there is no time to place a span at.
	if start.IsZero() {
		return nil
	}

	spans := make([]Span, 0, 1+len(turn.ModelCalls)+len(turn.ToolCalls))
	root := Span{
		TraceID: traceID, SpanID: rootID, Kind: SpanKindInternal,
		Name:              strings.TrimSpace("invoke_agent " + turn.AgentName),
		StartTimeUnixNano: unixNano(start), EndTimeUnixNano: unixNano(end),
		Attributes: append([]KeyValue{
			stringAttr("gen_ai.operation.name", "invoke_agent"),
			stringAttr("gen_ai.agent.id", turn.AgentID),
			stringAttr("gen_ai.agent.name", turn.AgentName),
			stringAttr("gen_ai.conversation.id", turn.AgentID),
		}, common...),
	}
	if turn.Truncated {
		root.Attributes = append(root.Attributes, boolAttr("omnara.trace.truncated", true))
	}
	rootContent := spanContent{}
	if opening != nil {
		rootContent.input = []message{{Role: "user", Parts: parseParts(opening.ContentBlocks, providerCallIDs)}}
	}
	if closing != nil {
		rootContent.output = []message{{Role: "assistant", Parts: parseParts(closing.ContentBlocks, providerCallIDs)}}
		if closing.StopReason == "error" {
			root.Status = &Status{Code: StatusCodeError}
		}
	}
	root.Attributes = append(root.Attributes, variant.attributes(spanRoleAgent, turn, rootContent, "")...)
	spans = append(spans, root)

	for _, call := range turn.ModelCalls {
		span := Span{
			TraceID: traceID, SpanID: spanID("model_call", call.ID), ParentSpanID: rootID, Kind: SpanKindClient,
			Name:              strings.TrimSpace("chat " + call.RequestModel),
			StartTimeUnixNano: unixNano(call.StartedAt), EndTimeUnixNano: unixNano(call.CompletedAt),
			Attributes: append(call.attributes(), common...),
		}
		if call.State != "succeeded" {
			span.Status = &Status{Code: StatusCodeError, Message: call.ErrorMessage}
		}
		content := spanContent{}
		if output, ok := outputs[call.ID]; ok {
			content.output = []message{{Role: "assistant", Parts: parseParts(output.ContentBlocks, providerCallIDs)}}
		}
		span.Attributes = append(span.Attributes, variant.attributes(spanRoleModel, turn, content, call.CostUSD)...)
		spans = append(spans, span)
	}

	for _, call := range turn.ToolCalls {
		// The result event is absent only when the turn's events were truncated.
		result := results[call.ID]
		span := Span{
			TraceID: traceID, SpanID: spanID("tool_call", call.ID), ParentSpanID: rootID, Kind: SpanKindInternal,
			Name:              strings.TrimSpace("execute_tool " + call.Name),
			StartTimeUnixNano: unixNano(call.StartedAt), EndTimeUnixNano: unixNano(call.CompletedAt),
			Attributes: append([]KeyValue{
				stringAttr("gen_ai.operation.name", "execute_tool"),
				stringAttr("gen_ai.tool.name", call.Name),
				stringAttr("gen_ai.tool.type", "function"),
				stringAttr("gen_ai.tool.call.id", call.ProviderCallID),
			}, common...),
		}
		content := spanContent{
			toolArguments: string(call.Input),
			toolResult:    partsText(parseParts(result.ContentBlocks, providerCallIDs)),
		}
		if content.toolArguments != "" {
			span.Attributes = append(span.Attributes, stringAttr("gen_ai.tool.call.arguments", content.toolArguments))
		}
		if call.Outcome == "succeeded" {
			span.Attributes = append(span.Attributes, stringAttr("gen_ai.tool.call.result", content.toolResult))
		} else {
			span.Attributes = append(span.Attributes, stringAttr("error.type", call.Outcome))
			span.Status = &Status{Code: StatusCodeError, Message: content.toolResult}
		}
		span.Attributes = append(span.Attributes, variant.attributes(spanRoleTool, turn, content, "")...)
		spans = append(spans, span)
	}
	return spans
}

func (turn Turn) commonAttributes() []KeyValue {
	attributes := []KeyValue{
		stringAttr("omnara.org.id", turn.OrgID),
		stringAttr("omnara.project.id", turn.ProjectID),
		stringAttr("omnara.agent.id", turn.AgentID),
		stringAttr("omnara.root_agent.id", turn.RootAgentID),
		stringAttr("omnara.turn.id", turn.PublicTurnID),
		intAttr("omnara.turn.sequence", turn.TurnSequence),
	}
	if turn.ParentAgentID != "" {
		attributes = append(attributes, stringAttr("omnara.parent_agent.id", turn.ParentAgentID))
	}
	return attributes
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

func (call ModelCall) attributes() []KeyValue {
	attributes := []KeyValue{
		stringAttr("gen_ai.operation.name", "chat"),
		stringAttr("gen_ai.provider.name", providerName(call.APIFormat)),
		stringAttr("gen_ai.request.model", call.RequestModel),
		stringAttr("omnara.model_call.operation", call.Operation),
		intAttr("omnara.model_call.attempt", int64(call.Attempt)),
	}
	if call.ResponseModel != "" {
		attributes = append(attributes, stringAttr("gen_ai.response.model", call.ResponseModel))
	}
	if call.ResponseID != "" {
		attributes = append(attributes, stringAttr("gen_ai.response.id", call.ResponseID))
	}
	if call.StopReason != "" {
		attributes = append(attributes, stringsAttr("gen_ai.response.finish_reasons", call.StopReason))
	}
	if call.State == "succeeded" {
		attributes = append(attributes,
			intAttr("gen_ai.usage.input_tokens", call.InputTokens),
			intAttr("gen_ai.usage.output_tokens", call.OutputTokens),
			intAttr("gen_ai.usage.cache_read.input_tokens", call.CacheReadTokens),
			intAttr("gen_ai.usage.cache_write.input_tokens", call.CacheWriteTokens),
			intAttr("gen_ai.usage.reasoning.output_tokens", call.ReasoningTokens),
		)
	} else if call.ErrorKind != "" {
		attributes = append(attributes, stringAttr("error.type", call.ErrorKind))
	}
	if call.CostUSD != "" {
		attributes = append(attributes, stringAttr("omnara.model_call.cost_usd", call.CostUSD))
	}
	return attributes
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

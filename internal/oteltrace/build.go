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
	// Parent is the trace context of the request that opened the turn. When
	// set, the turn's spans join that trace under the caller's span.
	Parent RemoteParent
	// Truncated is set when the turn has more events or calls than were
	// loaded, so the trace holds only the most recent ones.
	Truncated bool
	// ContentTruncated is set when tool arguments or event content blocks were
	// clipped or omitted during bounded loading before trace rendering.
	ContentTruncated bool
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

	InputTokens, OutputTokens int64
	// Nil when the provider did not report the count.
	CacheReadTokens, CacheWriteTokens, ReasoningTokens *int64
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
	// TurnID is the linked turn, whose ID is also its trace ID unless TraceID is set.
	TurnID uuid.UUID
	// TraceID is the caller's trace the linked turn joined, when it joined one.
	TraceID pcommon.TraceID
	// PublicTurnID is the public trn_... ID of the linked turn.
	PublicTurnID string
	// ToolCallID is set when the link points at a tool call's span; otherwise
	// it points at the turn's root span.
	ToolCallID uuid.UUID
}

// putLinks adds the links both as OTLP span links and as one JSON attribute,
// because some backends, Langfuse among them, drop span links on ingestion.
func (link Link) traceID() pcommon.TraceID {
	if !link.TraceID.IsEmpty() {
		return link.TraceID
	}
	return TraceID(link.TurnID)
}

func putLinks(span ptrace.Span, links []Link) {
	if len(links) == 0 {
		return
	}
	type linkAttribute struct {
		Kind    string `json:"kind"`
		AgentID string `json:"agent_id"`
		TurnID  string `json:"turn_id,omitempty"`
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
		spanLink.SetTraceID(link.traceID())
		spanLink.SetSpanID(target)
		spanLink.Attributes().PutStr("omnara.link.kind", link.Kind)
		spanLink.Attributes().PutStr("omnara.link.agent.id", link.AgentID)
		if link.PublicTurnID != "" {
			spanLink.Attributes().PutStr("omnara.link.turn.id", link.PublicTurnID)
		}
		summary = append(summary, linkAttribute{
			Kind: link.Kind, AgentID: link.AgentID, TurnID: link.PublicTurnID,
			TraceID: link.traceID().String(), SpanID: target.String(),
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

// spanFlags carries the caller's W3C trace flags in the low byte, and above
// it whether the span's parent is in another process: only the root's is.
func spanFlags(parent RemoteParent, root bool) uint32 {
	const hasIsRemote, isRemote = 0x100, 0x200
	flags := uint32(parent.Flags) | hasIsRemote
	if root {
		flags |= isRemote
	}
	return flags
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
	if !turn.Parent.IsZero() {
		traceID = turn.Parent.TraceID
	}
	rootID := spanID("turn", turn.TurnID)
	outputs := make(map[uuid.UUID]Event)
	results := make(map[uuid.UUID]Event)
	content := contentReader{
		providerCallIDs: make(map[string]string, len(turn.ToolCalls)), budget: maxTraceContentBytes,
	}
	for _, call := range turn.ToolCalls {
		content.providerCallIDs[call.ID.String()] = call.ProviderCallID
	}
	var openings []*Event
	var lastOutput *Event
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
			if event.IsOpening {
				openings = append(openings, event)
			}
		case events.KindModelOutput:
			outputs[event.ModelCallID] = *event
			lastOutput = event
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
		switch {
		case id != rootID:
			span.SetParentSpanID(rootID)
		case !turn.Parent.IsZero():
			span.SetParentSpanID(turn.Parent.SpanID)
		}
		if !turn.Parent.IsZero() {
			span.TraceState().FromRaw(turn.Parent.State)
			span.SetFlags(spanFlags(turn.Parent, id == rootID))
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
	rootContent := spanContent{}
	if len(openings) > 0 {
		rootContent.input = make([]message, 0, len(openings))
		for _, opening := range openings {
			rootContent.input = append(rootContent.input, message{Role: "user", Parts: content.parts(opening.ContentBlocks)})
		}
	}
	// The turn's answer is its last output, unless the turn goes on from it:
	// an output that calls tools or was cut off at the output limit is
	// followed by another model call, as the execution commands decide.
	if lastOutput != nil && lastOutput.StopReason != modelenvelope.StopReasonMaxTokens {
		if parts := content.parts(lastOutput.ContentBlocks); !hasToolCall(parts) {
			rootContent.output = []message{{Role: "assistant", Parts: parts}}
			if lastOutput.StopReason == modelenvelope.StopReasonError {
				root.Status().SetCode(ptrace.StatusCodeError)
			}
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
		spanContent := spanContent{}
		if output, ok := outputs[call.ID]; ok {
			spanContent.output = []message{{Role: "assistant", Parts: content.parts(output.ContentBlocks)}}
		}
		variant.putAttributes(attributes, spanRoleModel, turn, spanContent, call.CostUSD)
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
		spanContent := spanContent{
			toolArguments: content.clip(string(call.Input)),
			toolResult:    partsText(content.parts(result.ContentBlocks)),
		}
		if spanContent.toolArguments != "" {
			attributes.PutStr("gen_ai.tool.call.arguments", spanContent.toolArguments)
		}
		if call.Outcome == "succeeded" {
			attributes.PutStr("gen_ai.tool.call.result", spanContent.toolResult)
		} else {
			attributes.PutStr("error.type", call.Outcome)
			span.Status().SetCode(ptrace.StatusCodeError)
			span.Status().SetMessage(spanContent.toolResult)
		}
		variant.putAttributes(attributes, spanRoleTool, turn, spanContent, "")
		putLinks(span, call.Links)
	}
	if turn.Truncated {
		root.Attributes().PutBool("omnara.trace.truncated", true)
	}
	if turn.ContentTruncated || content.clipped {
		root.Attributes().PutBool("omnara.trace.content_truncated", true)
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
		for _, usage := range []struct {
			key    string
			tokens *int64
		}{
			{"gen_ai.usage.cache_read.input_tokens", call.CacheReadTokens},
			{"gen_ai.usage.cache_write.input_tokens", call.CacheWriteTokens},
			{"gen_ai.usage.reasoning.output_tokens", call.ReasoningTokens},
		} {
			if usage.tokens != nil {
				attributes.PutInt(usage.key, *usage.tokens)
			}
		}
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

const (
	// maxContentValueBytes bounds one message part, tool argument or result.
	maxContentValueBytes = 256 * 1024
	// maxTraceContentBytes bounds the content of one trace, so a turn of any
	// size encodes to a request an endpoint can accept.
	maxTraceContentBytes = 8 * 1024 * 1024
)

// contentReader turns stored content blocks into message parts within the
// trace's content budget. clipped records that some content was cut, which the
// root span reports rather than leaving the loss silent.
type contentReader struct {
	providerCallIDs map[string]string
	budget          int
	clipped         bool
}

func (reader *contentReader) clip(text string) string {
	limit := max(min(maxContentValueBytes, reader.budget), 0)
	if len(text) > limit {
		reader.clipped = true
		text = strings.ToValidUTF8(text[:limit], "")
	}
	reader.budget -= len(text)
	return text
}

func (reader *contentReader) parts(raw json.RawMessage) []part {
	var blocks []struct {
		Type       string          `json:"type"`
		Text       string          `json:"text"`
		ToolCallID string          `json:"tool_call_id"`
		Name       string          `json:"name"`
		Input      json.RawMessage `json:"input"`
		Value      json.RawMessage `json:"value"`
		ArtifactID string          `json:"artifact_id"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return []part{}
	}
	parts := make([]part, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "tool_call":
			id := reader.providerCallIDs[block.ToolCallID]
			if id == "" {
				id = block.ToolCallID
			}
			arguments := block.Input
			if clipped := reader.clip(string(arguments)); len(clipped) != len(arguments) {
				// Cut JSON is no longer JSON, so it is carried as a string.
				arguments = json.RawMessage(jsonString(clipped))
			}
			parts = append(parts, part{Type: "tool_call", ID: id, Name: block.Name, Arguments: arguments})
		case "structured_data":
			parts = append(parts, part{Type: block.Type, Content: reader.clip(string(block.Value))})
		case "media_ref":
			parts = append(parts, part{Type: block.Type, ID: block.ArtifactID})
		default:
			parts = append(parts, part{Type: block.Type, Content: reader.clip(block.Text)})
		}
	}
	return parts
}

func hasToolCall(parts []part) bool {
	for _, part := range parts {
		if part.Type == "tool_call" {
			return true
		}
	}
	return false
}

func partsText(parts []part) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch {
		case part.Type == "media_ref":
			texts = append(texts, "[media "+part.ID+"]")
		case part.Content != "" && part.Type != "reasoning":
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

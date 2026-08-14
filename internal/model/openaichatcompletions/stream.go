package openaichatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

var errChatStreamTerminal = errors.New("chat completions stream reached a terminal event")

func (p protocol) StreamAccept() string {
	return route.ServerSentEventsMediaType
}

func (p protocol) IsStreamingResponse(contentType string) bool {
	return route.MatchesMediaType(contentType, route.ServerSentEventsMediaType)
}

func (p protocol) BuildStreamRequest(body json.RawMessage) (json.RawMessage, error) {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode openai chat completions request for streaming: %w", err)
	}
	decoded["stream"] = json.RawMessage(`true`)
	if p.client.ModelAPIVariant() != modelprotocol.APIVariantOpenRouter {
		decoded["stream_options"] = streamOptionsWithUsage(decoded["stream_options"])
	}
	return json.Marshal(decoded)
}

func streamOptionsWithUsage(raw json.RawMessage) json.RawMessage {
	var options map[string]json.RawMessage
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		_ = json.Unmarshal(trimmed, &options)
	}
	if options == nil {
		options = map[string]json.RawMessage{}
	}
	options["include_usage"] = json.RawMessage(`true`)
	body, err := json.Marshal(options)
	if err != nil {
		return json.RawMessage(`{"include_usage":true}`)
	}
	return body
}

func (p protocol) ConsumeStream(
	ctx context.Context,
	body io.Reader,
	statusCode int,
	header http.Header,
	sink model.StreamSink,
) (model.Response, error) {
	emit := model.NewStreamEmitter(sink)
	acc := &chatStreamAccumulator{
		protocol:   p,
		statusCode: statusCode,
		header:     header,
		emit:       emit,
		choices:    map[int]*chatStreamChoiceState{},
	}
	readErr := route.ReadSSEEvents(ctx, body, func(ev route.SSEEvent) error {
		if err := acc.handle(ctx, ev); err != nil {
			return err
		}
		if acc.completed || acc.streamErr != nil {
			return errChatStreamTerminal
		}
		return nil
	})
	acc.closeOpenBlocks(ctx)
	if readErr != nil && !errors.Is(readErr, errChatStreamTerminal) {
		emit.Error(ctx, readErr.Error())
		return acc.partialResponse(ctx), model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     p.errorSource(),
			StatusCode: statusCode,
			Message:    fmt.Sprintf("read openai chat completions stream: %v", readErr),
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
			Cause:      readErr,
		})
	}
	if acc.streamErr != nil {
		emit.Error(ctx, acc.streamErr.Error())
		return acc.partialResponse(ctx), acc.streamErr
	}
	if !acc.hasCompleteTerminalOutcome() ||
		(!acc.completed && (p.ModelAPIVariant() != modelprotocol.APIVariantBedrock || !acc.usageReceived)) {
		err := model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     p.errorSource(),
			StatusCode: statusCode,
			Message:    "openai chat completions stream ended without a terminal chunk",
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
		})
		emit.Error(ctx, err.Error())
		return acc.partialResponse(ctx), err
	}
	responseBody, err := acc.responseBody()
	if err != nil {
		emit.Error(ctx, err.Error())
		return acc.partialResponse(ctx), model.ProviderError{
			Kind:       model.ErrorKindUnknown,
			Source:     p.errorSource(),
			StatusCode: statusCode,
			Message:    err.Error(),
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
			Cause:      err,
		}
	}
	out, err := p.ParseResponse(ctx, route.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       responseBody,
	})
	if err != nil {
		emit.Error(ctx, err.Error())
		if _, ok := model.ClassifyError(err); !ok {
			err = model.ProviderError{
				Kind:       model.ErrorKindUnknown,
				Source:     p.errorSource(),
				StatusCode: statusCode,
				Message:    err.Error(),
				RequestID:  model.RequestIDFromHeader(header),
				RetryAfter: model.RetryAfterFromHeader(header),
				Cause:      err,
			}
		}
		return out, err
	}
	emit.MessageStop(ctx, out.StopReason, out.Usage)
	return out, nil
}

func (a *chatStreamAccumulator) hasCompleteTerminalOutcome() bool {
	if len(a.choices) == 0 {
		return false
	}
	for _, choice := range a.choices {
		if strings.TrimSpace(choice.finishReason) == "" {
			return false
		}
	}
	return true
}

type chatStreamAccumulator struct {
	protocol       protocol
	statusCode     int
	header         http.Header
	emit           model.StreamEmitter
	nextBlockIndex int
	choices        map[int]*chatStreamChoiceState
	id             string
	servedModel    string
	usage          chatUsage
	usageReceived  bool
	completed      bool
	streamErr      error
}

func (a *chatStreamAccumulator) partialResponse(ctx context.Context) model.Response {
	return a.protocol.chatResponseEvidence(ctx, chatCompletionsResponse{
		ID:    a.id,
		Model: a.servedModel,
		Usage: a.usage,
	})
}

type chatStreamChoiceState struct {
	role                 chatRole
	text                 strings.Builder
	refusal              strings.Builder
	reasoning            strings.Builder
	reasoningContent     strings.Builder
	reasoningDetails     map[string]*chatStreamReasoningDetailState
	reasoningDetailOrder []string
	finishReason         string
	textBlockIndex       int
	textBlockOpen        bool
	reasoningBlockIndex  int
	reasoningBlockOpen   bool
	toolCalls            map[int]*chatStreamToolCallState
	toolOrder            []int
}

type chatStreamToolCallState struct {
	id         string
	callType   string
	name       string
	arguments  strings.Builder
	pending    strings.Builder
	blockIndex int
	blockOpen  bool
}

type chatStreamReasoningDetailState struct {
	fields    map[string]json.RawMessage
	text      strings.Builder
	summary   strings.Builder
	data      strings.Builder
	signature strings.Builder
}

type chatStreamChunk struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Choices []chatStreamChoice `json:"choices"`
	Usage   *chatUsage         `json:"usage"`
	Error   chatProviderError  `json:"error"`
}

type chatStreamChoice struct {
	Index        int               `json:"index"`
	Delta        chatStreamDelta   `json:"delta"`
	FinishReason string            `json:"finish_reason"`
	Error        chatProviderError `json:"error"`
}

type chatStreamDelta struct {
	Role             chatRole                  `json:"role"`
	Content          json.RawMessage           `json:"content"`
	Refusal          json.RawMessage           `json:"refusal"`
	Reasoning        json.RawMessage           `json:"reasoning"`
	ReasoningContent json.RawMessage           `json:"reasoning_content"`
	ReasoningDetails []json.RawMessage         `json:"reasoning_details"`
	ToolCalls        []chatStreamToolCallDelta `json:"tool_calls"`
}

type chatStreamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (a *chatStreamAccumulator) allocBlock() int {
	idx := a.nextBlockIndex
	a.nextBlockIndex++
	return idx
}

func (a *chatStreamAccumulator) handle(ctx context.Context, ev route.SSEEvent) error {
	data := strings.TrimSpace(ev.Data)
	if data == "" {
		return nil
	}
	if data == "[DONE]" {
		a.completed = true
		a.closeOpenBlocks(ctx)
		return nil
	}
	var chunk chatStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return fmt.Errorf("decode chat completion chunk: %w", err)
	}
	if chunk.ID != "" && a.id == "" {
		a.id = chunk.ID
	}
	if chunk.Model != "" {
		a.servedModel = chunk.Model
	}
	if chunk.Usage != nil {
		a.usage = *chunk.Usage
		a.usageReceived = true
	}
	if chunk.Error.present() {
		a.streamErr = classifyProviderError(
			a.protocol.errorSource(),
			a.statusCode,
			a.header,
			chunk.Error,
			"",
		)
		a.closeOpenBlocks(ctx)
		return nil
	}
	if err := model.ValidateProviderJSON([]byte(data)); err != nil {
		return fmt.Errorf("validate openai chat completions stream event: %w", err)
	}
	if len(chunk.Choices) > 1 {
		return fmt.Errorf(
			"openai chat completions stream chunk must contain at most one choice (got %d)",
			len(chunk.Choices),
		)
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return fmt.Errorf(
				"openai chat completions stream choice index must be 0 (got %d)",
				choice.Index,
			)
		}
		if err := a.handleChoice(ctx, choice); err != nil {
			return err
		}
	}
	return nil
}

func (a *chatStreamAccumulator) handleChoice(ctx context.Context, choice chatStreamChoice) error {
	state := a.choice(choice.Index)
	text, err := optionalDeltaString(choice.Delta.Content, "content")
	if err != nil {
		return err
	}
	refusal, err := optionalDeltaString(choice.Delta.Refusal, "refusal")
	if err != nil {
		return err
	}
	reasoning, err := optionalDeltaString(choice.Delta.Reasoning, "reasoning")
	if err != nil {
		return err
	}
	reasoningContent, err := optionalDeltaString(choice.Delta.ReasoningContent, "reasoning_content")
	if err != nil {
		return err
	}
	if choice.Delta.Role != "" {
		state.role = choice.Delta.Role
	}
	if text != "" {
		state.appendText(ctx, a, text)
	}
	if refusal != "" {
		state.refusal.WriteString(refusal)
		state.appendText(ctx, a, refusal)
	}
	if reasoning != "" {
		state.reasoning.WriteString(reasoning)
	}
	if reasoningContent != "" {
		state.reasoningContent.WriteString(reasoningContent)
	}
	if len(choice.Delta.ReasoningDetails) > 0 {
		if text := state.appendReasoningDetails(choice.Delta.ReasoningDetails); text != "" {
			state.appendReasoning(ctx, a, text)
		}
	} else if reasoning != "" {
		state.appendReasoning(ctx, a, reasoning)
	} else if reasoningContent != "" {
		state.appendReasoning(ctx, a, reasoningContent)
	}
	for _, toolDelta := range choice.Delta.ToolCalls {
		state.applyToolDelta(ctx, a, toolDelta)
	}
	if choice.Error.present() || strings.EqualFold(choice.FinishReason, "error") {
		a.streamErr = classifyChoiceError(a.protocol.errorSource(), a.statusCode, a.header, chatChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
			Error:        choice.Error,
		})
		a.closeOpenBlocks(ctx)
		return nil
	}
	if choice.FinishReason != "" {
		state.finishReason = choice.FinishReason
		state.closeOpenBlocks(ctx, a.emit)
	}
	return nil
}

func (a *chatStreamAccumulator) choice(index int) *chatStreamChoiceState {
	state, ok := a.choices[index]
	if ok {
		return state
	}
	state = &chatStreamChoiceState{
		reasoningDetails: map[string]*chatStreamReasoningDetailState{},
		toolCalls:        map[int]*chatStreamToolCallState{},
	}
	a.choices[index] = state
	return state
}

func (a *chatStreamAccumulator) closeOpenBlocks(ctx context.Context) {
	for _, index := range sortedChoiceIndexes(a.choices) {
		a.choices[index].closeOpenBlocks(ctx, a.emit)
	}
}

func (s *chatStreamChoiceState) appendText(ctx context.Context, acc *chatStreamAccumulator, text string) {
	if !s.textBlockOpen {
		s.textBlockIndex = acc.allocBlock()
		s.textBlockOpen = true
		acc.emit.BlockStart(ctx, s.textBlockIndex, model.StreamBlock{Kind: model.StreamBlockText})
	}
	s.text.WriteString(text)
	acc.emit.TextDelta(ctx, s.textBlockIndex, text)
}

func (s *chatStreamChoiceState) appendReasoning(ctx context.Context, acc *chatStreamAccumulator, text string) {
	if !s.reasoningBlockOpen {
		s.reasoningBlockIndex = acc.allocBlock()
		s.reasoningBlockOpen = true
		acc.emit.BlockStart(ctx, s.reasoningBlockIndex, model.StreamBlock{Kind: model.StreamBlockThinking})
	}
	acc.emit.ThinkingDelta(ctx, s.reasoningBlockIndex, text)
}

func (s *chatStreamChoiceState) appendReasoningDetails(details []json.RawMessage) string {
	var visible strings.Builder
	for _, raw := range details {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}
		key := reasoningDetailKey(fields, len(s.reasoningDetailOrder))
		detail := s.reasoningDetail(key)
		detail.apply(fields)
		if text := rawString(fields["summary"]); text != "" {
			visible.WriteString(text)
			continue
		}
		visible.WriteString(rawString(fields["text"]))
	}
	return visible.String()
}

func (s *chatStreamChoiceState) reasoningDetail(key string) *chatStreamReasoningDetailState {
	detail, ok := s.reasoningDetails[key]
	if ok {
		return detail
	}
	detail = &chatStreamReasoningDetailState{fields: map[string]json.RawMessage{}}
	s.reasoningDetails[key] = detail
	s.reasoningDetailOrder = append(s.reasoningDetailOrder, key)
	return detail
}

func (d *chatStreamReasoningDetailState) apply(fields map[string]json.RawMessage) {
	for key, value := range fields {
		if isReasoningDetailAccumulatorField(key) {
			continue
		}
		d.fields[key] = value
	}
	for _, field := range []struct {
		name string
		dst  *strings.Builder
	}{
		{name: "text", dst: &d.text},
		{name: "summary", dst: &d.summary},
		{name: "data", dst: &d.data},
		{name: "signature", dst: &d.signature},
	} {
		if value := rawString(fields[field.name]); value != "" {
			field.dst.WriteString(value)
		}
		if field.dst.Len() > 0 {
			d.fields[field.name] = mustJSONString(field.dst.String())
		} else if value, ok := fields[field.name]; ok {
			d.fields[field.name] = value
		}
	}
}

func isReasoningDetailAccumulatorField(key string) bool {
	switch key {
	case "text", "summary", "data", "signature":
		return true
	default:
		return false
	}
}

func (s *chatStreamChoiceState) applyToolDelta(
	ctx context.Context,
	acc *chatStreamAccumulator,
	delta chatStreamToolCallDelta,
) {
	tool := s.tool(delta.Index)
	if delta.ID != "" {
		tool.id = delta.ID
	}
	if delta.Type != "" {
		tool.callType = delta.Type
	}
	if delta.Function.Name != "" {
		tool.name = delta.Function.Name
	}
	arguments := delta.Function.Arguments
	if !tool.blockOpen {
		if tool.id == "" || tool.name == "" {
			tool.pending.WriteString(arguments)
			tool.arguments.WriteString(arguments)
			return
		}
		tool.blockIndex = acc.allocBlock()
		tool.blockOpen = true
		acc.emit.BlockStart(ctx, tool.blockIndex, model.StreamBlock{
			Kind:       model.StreamBlockToolUse,
			ToolCallID: tool.id,
			ToolName:   tool.name,
		})
		if pending := tool.pending.String(); pending != "" {
			acc.emit.ToolArgsDelta(ctx, tool.blockIndex, pending)
		}
	}
	tool.arguments.WriteString(arguments)
	acc.emit.ToolArgsDelta(ctx, tool.blockIndex, arguments)
}

func (s *chatStreamChoiceState) tool(index int) *chatStreamToolCallState {
	tool, ok := s.toolCalls[index]
	if ok {
		return tool
	}
	tool = &chatStreamToolCallState{}
	s.toolCalls[index] = tool
	s.toolOrder = append(s.toolOrder, index)
	return tool
}

func (s *chatStreamChoiceState) closeOpenBlocks(ctx context.Context, emit model.StreamEmitter) {
	type openBlock struct {
		index     int
		kind      string
		toolIndex int
	}
	blocks := []openBlock{}
	if s.textBlockOpen {
		blocks = append(blocks, openBlock{index: s.textBlockIndex, kind: "text"})
	}
	if s.reasoningBlockOpen {
		blocks = append(blocks, openBlock{index: s.reasoningBlockIndex, kind: "reasoning"})
	}
	sort.Ints(s.toolOrder)
	for _, index := range s.toolOrder {
		tool := s.toolCalls[index]
		if tool.blockOpen {
			blocks = append(blocks, openBlock{index: tool.blockIndex, kind: "tool", toolIndex: index})
		}
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].index < blocks[j].index })
	for _, block := range blocks {
		emit.BlockStop(ctx, block.index)
		switch block.kind {
		case "text":
			s.textBlockOpen = false
		case "reasoning":
			s.reasoningBlockOpen = false
		case "tool":
			s.toolCalls[block.toolIndex].blockOpen = false
		}
	}
}

func (a *chatStreamAccumulator) responseBody() (json.RawMessage, error) {
	choices := make([]chatChoice, 0, len(a.choices))
	for _, index := range sortedChoiceIndexes(a.choices) {
		state := a.choices[index]
		message, err := state.responseMessage()
		if err != nil {
			return nil, err
		}
		choices = append(choices, chatChoice{
			Index:        index,
			Message:      message,
			FinishReason: state.finishReason,
		})
	}
	return json.Marshal(chatCompletionsResponse{
		ID:      a.id,
		Model:   a.servedModel,
		Choices: choices,
		Usage:   a.usage,
	})
}

func (s *chatStreamChoiceState) responseMessage() (chatResponseMessage, error) {
	role := s.role
	if role == "" {
		role = chatRoleAssistant
	}
	content := json.RawMessage(`null`)
	if text := s.text.String(); text != "" {
		raw, err := json.Marshal(text)
		if err != nil {
			return chatResponseMessage{}, err
		}
		content = raw
	}
	var toolCalls []json.RawMessage
	if s.finishReason != "length" {
		var err error
		toolCalls, err = s.toolCallMessages()
		if err != nil {
			return chatResponseMessage{}, err
		}
	}
	reasoningDetails, err := s.reasoningDetailMessages()
	if err != nil {
		return chatResponseMessage{}, err
	}
	return chatResponseMessage{
		Role:             role,
		Content:          content,
		Refusal:          s.refusal.String(),
		Reasoning:        s.reasoning.String(),
		ReasoningContent: s.reasoningContent.String(),
		ReasoningDetails: reasoningDetails,
		ToolCalls:        toolCalls,
	}, nil
}

func (s *chatStreamChoiceState) reasoningDetailMessages() ([]json.RawMessage, error) {
	if len(s.reasoningDetailOrder) == 0 {
		return nil, nil
	}
	out := make([]json.RawMessage, 0, len(s.reasoningDetailOrder))
	for _, key := range s.reasoningDetailOrder {
		raw, err := json.Marshal(s.reasoningDetails[key].fields)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func (s *chatStreamChoiceState) toolCallMessages() ([]json.RawMessage, error) {
	if len(s.toolCalls) == 0 {
		return nil, nil
	}
	sort.Ints(s.toolOrder)
	out := make([]json.RawMessage, 0, len(s.toolOrder))
	for _, index := range s.toolOrder {
		tool := s.toolCalls[index]
		callType := tool.callType
		if callType == "" {
			callType = "function"
		}
		arguments := tool.arguments.String()
		raw, err := json.Marshal(chatToolCall{
			ID:   tool.id,
			Type: callType,
			Function: chatFunction{
				Name:      tool.name,
				Arguments: arguments,
			},
		})
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func sortedChoiceIndexes(choices map[int]*chatStreamChoiceState) []int {
	indexes := make([]int, 0, len(choices))
	for index := range choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func optionalDeltaString(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("openai chat completions stream delta %s must be a string or null", field)
	}
	return value, nil
}

func rawInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func reasoningDetailKey(fields map[string]json.RawMessage, fallback int) string {
	if index, ok := rawInt(fields["index"]); ok {
		return fmt.Sprintf("index:%d", index)
	}
	if id := rawString(fields["id"]); id != "" {
		return "id:" + id
	}
	return fmt.Sprintf("fragment:%d", fallback)
}

func mustJSONString(value string) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return raw
}

package anthropicmessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
)

var errAnthropicStreamTerminal = errors.New("anthropic stream reached a terminal event")

func (p protocol) StreamAccept() string {
	return route.ServerSentEventsMediaType
}

func (p protocol) IsStreamingResponse(contentType string) bool {
	return route.MatchesMediaType(contentType, route.ServerSentEventsMediaType)
}

func (p protocol) BuildStreamRequest(body json.RawMessage) (json.RawMessage, error) {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode anthropic request for streaming: %w", err)
	}
	decoded["stream"] = json.RawMessage(`true`)
	return json.Marshal(decoded)
}

func (p protocol) ConsumeStream(
	ctx context.Context,
	body io.Reader,
	statusCode int,
	header http.Header,
	sink model.StreamSink,
) (model.Response, error) {
	emit := model.NewStreamEmitter(sink)
	acc := &anthropicStreamAccumulator{
		protocol:   p,
		statusCode: statusCode,
		header:     header,
		emit:       emit,
	}
	readErr := route.ReadSSEEvents(ctx, body, func(ev route.SSEEvent) error {
		if err := acc.handle(ctx, ev); err != nil {
			return err
		}
		if acc.sawMessageStop || acc.streamErr != nil {
			return errAnthropicStreamTerminal
		}
		return nil
	})
	if readErr != nil && !errors.Is(readErr, errAnthropicStreamTerminal) {
		acc.abortOpenBlocks(ctx)
		emit.Error(ctx, readErr.Error())
		return acc.partialResponse(), model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     p.errorSource(),
			StatusCode: statusCode,
			Message:    fmt.Sprintf("read anthropic stream: %v", readErr),
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
			Cause:      readErr,
		})
	}
	if acc.streamErr != nil {
		acc.abortOpenBlocks(ctx)
		emit.Error(ctx, acc.streamErr.Error())
		return acc.partialResponse(), acc.streamErr
	}
	if !acc.hasCompleteTerminalOutcome() {
		err := model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     p.errorSource(),
			StatusCode: statusCode,
			Message:    "anthropic stream ended without a complete terminal outcome",
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
		})
		acc.abortOpenBlocks(ctx)
		emit.Error(ctx, err.Error())
		return acc.partialResponse(), err
	}
	responseBody, err := acc.responseBody()
	if err != nil {
		emit.Error(ctx, err.Error())
		return acc.partialResponse(), model.ProviderError{
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
	acc.restoreMalformedToolInputs(&out)
	emit.MessageStop(ctx, out.StopReason, out.Usage)
	return out, nil
}

func (a *anthropicStreamAccumulator) hasCompleteTerminalOutcome() bool {
	return a.sawMessageStop && strings.TrimSpace(a.stopReasonRaw) != "" && a.activeBlock == nil
}

type anthropicStreamAccumulator struct {
	protocol   protocol
	statusCode int
	header     http.Header
	emit       model.StreamEmitter

	id             string
	servedModel    string
	stopReasonRaw  string
	usageRaw       usage
	content        []json.RawMessage
	toolInputs     []string
	activeBlock    *anthropicStreamBlock
	activeBlockIdx int
	nextBlockIdx   int
	sawMessageStop bool
	streamErr      error
}

func (a *anthropicStreamAccumulator) partialResponse() model.Response {
	return model.Response{
		ID:                      a.id,
		ServedProviderModelSlug: a.servedModel,
		Usage:                   usageFromResponse(a.usageRaw),
	}
}

type anthropicStreamBlock struct {
	kind          model.StreamBlockKind
	raw           json.RawMessage
	text          strings.Builder
	thinking      strings.Builder
	signature     strings.Builder
	toolInputJSON strings.Builder
	toolID        string
	toolName      string
}

func (a *anthropicStreamAccumulator) handle(ctx context.Context, ev route.SSEEvent) error {
	if ev.Data != "" && ev.Event != "error" {
		if err := model.ValidateProviderJSON([]byte(ev.Data)); err != nil {
			return fmt.Errorf("validate anthropic stream event %q: %w", ev.Event, err)
		}
	}
	switch ev.Event {
	case "message_start":
		var msg struct {
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage usage  `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &msg); err != nil {
			return fmt.Errorf("decode message_start: %w", err)
		}
		a.id = msg.Message.ID
		a.servedModel = msg.Message.Model
		a.usageRaw = mergeAnthropicUsage(a.usageRaw, msg.Message.Usage)
		return nil
	case "content_block_start":
		var frame struct {
			Index *int            `json:"index"`
			Block json.RawMessage `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &frame); err != nil {
			return fmt.Errorf("decode content_block_start: %w", err)
		}
		index, err := requiredAnthropicBlockIndex("content_block_start", frame.Index)
		if err != nil {
			return err
		}
		return a.openBlock(ctx, index, frame.Block)
	case "content_block_delta":
		var frame struct {
			Index *int            `json:"index"`
			Delta json.RawMessage `json:"delta"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &frame); err != nil {
			return fmt.Errorf("decode content_block_delta: %w", err)
		}
		index, err := requiredAnthropicBlockIndex("content_block_delta", frame.Index)
		if err != nil {
			return err
		}
		return a.applyDelta(ctx, index, frame.Delta)
	case "content_block_stop":
		var frame struct {
			Index *int `json:"index"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &frame); err != nil {
			return fmt.Errorf("decode content_block_stop: %w", err)
		}
		index, err := requiredAnthropicBlockIndex("content_block_stop", frame.Index)
		if err != nil {
			return err
		}
		return a.closeBlock(ctx, index)
	case "message_delta":
		var frame struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &frame); err != nil {
			return fmt.Errorf("decode message_delta: %w", err)
		}
		if frame.Delta.StopReason != "" {
			a.stopReasonRaw = frame.Delta.StopReason
		}
		a.usageRaw = mergeAnthropicUsage(a.usageRaw, frame.Usage)
		return nil
	case "message_stop":
		a.sawMessageStop = true
		return nil
	case "ping", "":
		return nil
	case "error":
		var frame anthropicErrorEnvelope
		if err := json.Unmarshal([]byte(ev.Data), &frame); err != nil {
			metadata, metadataErr := json.Marshal(map[string]any{
				"event":           ev.Event,
				"raw_event_bytes": len(ev.Data),
			})
			if metadataErr != nil {
				return fmt.Errorf("encode malformed anthropic error evidence: %w", metadataErr)
			}
			a.streamErr = model.ProviderError{
				Kind:       model.ErrorKindUnknown,
				Source:     a.protocol.errorSource(),
				StatusCode: a.statusCode,
				Code:       "malformed_error_event",
				Message:    "anthropic stream returned an undecodable error event",
				RequestID:  model.RequestIDFromHeader(a.header),
				RetryAfter: model.RetryAfterFromHeader(a.header),
				Metadata:   metadata,
				Cause:      err,
			}
			return nil
		}
		a.streamErr = anthropicProviderError(
			a.protocol.errorSource(),
			a.statusCode,
			a.header,
			frame,
			"anthropic stream failed",
		)
		return nil
	default:
		return nil
	}
}

func requiredAnthropicBlockIndex(event string, index *int) (int, error) {
	if index == nil {
		return 0, fmt.Errorf("%s is missing its block index", event)
	}
	return *index, nil
}

func (a *anthropicStreamAccumulator) openBlock(ctx context.Context, index int, raw json.RawMessage) error {
	if a.activeBlock != nil {
		return fmt.Errorf(
			"content block index %d started before index %d stopped",
			index,
			a.activeBlockIdx,
		)
	}
	if index != a.nextBlockIdx {
		return fmt.Errorf("content block started at index %d, want %d", index, a.nextBlockIdx)
	}
	var typed struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Text  string          `json:"text"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		return fmt.Errorf("decode content_block_start payload: %w", err)
	}
	block := &anthropicStreamBlock{}
	a.activeBlock = block
	a.activeBlockIdx = index
	switch typed.Type {
	case "text":
		block.kind = model.StreamBlockText
		block.text.WriteString(typed.Text)
		a.emit.BlockStart(ctx, index, model.StreamBlock{Kind: model.StreamBlockText})
		a.emit.TextDelta(ctx, index, typed.Text)
	case "thinking":
		block.kind = model.StreamBlockThinking
		a.emit.BlockStart(ctx, index, model.StreamBlock{Kind: model.StreamBlockThinking})
	case "tool_use":
		block.kind = model.StreamBlockToolUse
		block.toolID = typed.ID
		block.toolName = typed.Name
		if len(typed.Input) > 0 && string(typed.Input) != "null" && string(typed.Input) != "{}" {
			block.toolInputJSON.Write(typed.Input)
		}
		a.emit.BlockStart(ctx, index, model.StreamBlock{
			Kind:       model.StreamBlockToolUse,
			ToolCallID: typed.ID,
			ToolName:   typed.Name,
		})
	default:
		block.raw = raw
	}
	return nil
}

func (a *anthropicStreamAccumulator) applyDelta(ctx context.Context, index int, raw json.RawMessage) error {
	block := a.activeBlock
	if block == nil {
		return fmt.Errorf("content block delta references unopened index %d", index)
	}
	if index != a.activeBlockIdx {
		return fmt.Errorf("content block delta references index %d while index %d is open", index, a.activeBlockIdx)
	}
	var typed struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		return fmt.Errorf("decode content_block_delta payload: %w", err)
	}
	if block.kind == "" {
		return nil
	}
	switch typed.Type {
	case "text_delta":
		block.text.WriteString(typed.Text)
		a.emit.TextDelta(ctx, index, typed.Text)
	case "thinking_delta":
		block.thinking.WriteString(typed.Thinking)
		a.emit.ThinkingDelta(ctx, index, typed.Thinking)
	case "input_json_delta":
		block.toolInputJSON.WriteString(typed.PartialJSON)
		a.emit.ToolArgsDelta(ctx, index, typed.PartialJSON)
	case "signature_delta":
		block.signature.WriteString(typed.Signature)
	default:
	}
	return nil
}

func (a *anthropicStreamAccumulator) closeBlock(ctx context.Context, index int) error {
	block := a.activeBlock
	if block == nil {
		return fmt.Errorf("content block stop references unopened index %d", index)
	}
	if index != a.activeBlockIdx {
		return fmt.Errorf("content block stop references index %d while index %d is open", index, a.activeBlockIdx)
	}
	rawBlock, err := block.assemble()
	if err != nil {
		return err
	}
	if len(rawBlock) > 0 {
		a.content = append(a.content, rawBlock)
	}
	if block.kind == model.StreamBlockToolUse {
		a.toolInputs = append(a.toolInputs, strings.TrimSpace(block.toolInputJSON.String()))
	}
	if block.kind != "" {
		a.emit.BlockStop(ctx, index)
	}
	a.activeBlock = nil
	a.nextBlockIdx++
	return nil
}

func (a *anthropicStreamAccumulator) abortOpenBlocks(ctx context.Context) {
	if a.activeBlock != nil && a.activeBlock.kind != "" {
		a.emit.BlockStop(ctx, a.activeBlockIdx)
	}
}

func (a *anthropicStreamAccumulator) restoreMalformedToolInputs(response *model.Response) {
	toolIndex := 0
	for index := range response.Content {
		part := &response.Content[index]
		if part.Type != model.ResponsePartTypeToolCall {
			continue
		}
		if toolIndex >= len(a.toolInputs) {
			return
		}
		raw := a.toolInputs[toolIndex]
		toolIndex++
		if raw != "" && !json.Valid([]byte(raw)) {
			part.ToolInput = json.RawMessage(raw)
		}
	}
}

func (b *anthropicStreamBlock) assemble() (json.RawMessage, error) {
	switch b.kind {
	case model.StreamBlockText:
		return json.Marshal(map[string]any{"type": "text", "text": b.text.String()})
	case model.StreamBlockThinking:
		return json.Marshal(map[string]any{
			"type":      "thinking",
			"thinking":  b.thinking.String(),
			"signature": b.signature.String(),
		})
	case model.StreamBlockToolUse:
		input := json.RawMessage(`{}`)
		if buf := strings.TrimSpace(b.toolInputJSON.String()); buf != "" && json.Valid([]byte(buf)) {
			input = json.RawMessage(buf)
		}
		return json.Marshal(map[string]any{
			"type":  "tool_use",
			"id":    b.toolID,
			"name":  b.toolName,
			"input": input,
		})
	default:
		return b.raw, nil
	}
}

func (a *anthropicStreamAccumulator) responseBody() (json.RawMessage, error) {
	content := a.content
	if content == nil {
		content = []json.RawMessage{}
	}
	body, err := json.Marshal(map[string]any{
		"id":          a.id,
		"type":        "message",
		"role":        anthropicRoleAssistant,
		"model":       a.servedModel,
		"content":     content,
		"stop_reason": a.stopReasonRaw,
		"usage":       a.usageRaw,
	})
	if err != nil {
		return nil, fmt.Errorf("assemble anthropic streamed response body: %w", err)
	}
	return body, nil
}

func mergeAnthropicUsage(into, delta usage) usage {
	if delta.InputTokens > into.InputTokens {
		into.InputTokens = delta.InputTokens
	}
	if delta.OutputTokens > into.OutputTokens {
		into.OutputTokens = delta.OutputTokens
	}
	if delta.CacheCreationInputTokens > into.CacheCreationInputTokens {
		into.CacheCreationInputTokens = delta.CacheCreationInputTokens
	}
	if delta.CacheReadInputTokens > into.CacheReadInputTokens {
		into.CacheReadInputTokens = delta.CacheReadInputTokens
	}
	return into
}

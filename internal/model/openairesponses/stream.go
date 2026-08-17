package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/providererrors"
	"github.com/omnara-ai/omnara/internal/model/route"
)

var errOpenAIStreamTerminal = errors.New("openai stream reached a terminal event")

func (p protocol) StreamAccept() string {
	return route.ServerSentEventsMediaType
}

func (p protocol) IsStreamingResponse(contentType string) bool {
	return route.MatchesMediaType(contentType, route.ServerSentEventsMediaType)
}

func (p protocol) BuildStreamRequest(body json.RawMessage) (json.RawMessage, error) {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode openai request for streaming: %w", err)
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
	acc := &openAIStreamAccumulator{
		protocol:              p,
		header:                header,
		statusCode:            statusCode,
		emit:                  emit,
		itemBlockIndex:        map[string]int{},
		itemContentBlockIndex: map[string]int{},
		seenItemIDs:           map[string]bool{},
		seenContentParts:      map[string]bool{},
	}
	readErr := route.ReadSSEEvents(ctx, body, func(ev route.SSEEvent) error {
		if err := acc.handle(ctx, ev); err != nil {
			return err
		}
		if acc.completed {
			return errOpenAIStreamTerminal
		}
		return nil
	})
	acc.closeOpenBlocks(ctx)
	if readErr != nil && !errors.Is(readErr, errOpenAIStreamTerminal) {
		emit.Error(ctx, readErr.Error())
		return acc.out, model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     p.errorSource(),
			StatusCode: statusCode,
			Message:    fmt.Sprintf("read openai stream: %v", readErr),
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
			Cause:      readErr,
		})
	}
	if !acc.completed {
		err := model.AmbiguousProviderOutcome(model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     p.errorSource(),
			StatusCode: statusCode,
			Message:    "openai stream ended without a terminal response event",
			RequestID:  model.RequestIDFromHeader(header),
			RetryAfter: model.RetryAfterFromHeader(header),
		})
		emit.Error(ctx, err.Error())
		return acc.out, err
	}
	if acc.streamErr != nil {
		emit.Error(ctx, acc.streamErr.Error())
		return acc.out, acc.streamErr
	}
	emit.MessageStop(ctx, acc.out.StopReason, acc.out.Usage)
	return acc.out, nil
}

type openAIStreamAccumulator struct {
	protocol              protocol
	header                http.Header
	statusCode            int
	emit                  model.StreamEmitter
	nextBlockIndex        int
	itemBlockIndex        map[string]int
	itemContentBlockIndex map[string]int
	seenItemIDs           map[string]bool
	seenContentParts      map[string]bool
	completed             bool
	streamErr             error
	out                   model.Response
}

func messageContentKey(itemID string, contentIndex int) string {
	return fmt.Sprintf("%s/%d", itemID, contentIndex)
}

func (a *openAIStreamAccumulator) allocBlock() int {
	idx := a.nextBlockIndex
	a.nextBlockIndex++
	return idx
}

func (a *openAIStreamAccumulator) closeOpenBlocks(ctx context.Context) {
	open := make(map[int]struct{}, len(a.itemBlockIndex)+len(a.itemContentBlockIndex))
	for _, idx := range a.itemBlockIndex {
		open[idx] = struct{}{}
	}
	for _, idx := range a.itemContentBlockIndex {
		open[idx] = struct{}{}
	}
	for idx := range a.nextBlockIndex {
		if _, ok := open[idx]; ok {
			a.emit.BlockStop(ctx, idx)
		}
	}
	clear(a.itemBlockIndex)
	clear(a.itemContentBlockIndex)
}

func (a *openAIStreamAccumulator) handle(ctx context.Context, ev route.SSEEvent) error {
	if ev.Data == "" {
		return nil
	}
	event, err := openAIStreamEventType(ev)
	if err != nil {
		return err
	}
	if event != "response.error" && event != "error" {
		if err := model.ValidateProviderJSON([]byte(ev.Data)); err != nil {
			return fmt.Errorf("validate openai-responses stream event %q: %w", event, err)
		}
	}
	switch event {
	case "response.created", "response.queued", "response.in_progress":
		return a.handleResponseEvidence(event, ev.Data)
	case "response.output_item.added":
		return a.handleOutputItemAdded(ctx, ev.Data)
	case "response.content_part.added":
		return a.handleContentPartAdded(ctx, ev.Data)
	case "response.output_text.delta":
		return a.handleOutputTextDelta(ctx, ev.Data)
	case "response.function_call_arguments.delta":
		return a.handleFunctionCallArgsDelta(ctx, ev.Data)
	case "response.reasoning_summary_text.delta",
		"response.reasoning_text.delta":
		return a.handleReasoningDelta(ctx, ev.Data)
	case "response.content_part.done":
		return a.handleContentPartDone(ctx, ev.Data)
	case "response.output_item.done":
		return a.handleOutputItemDone(ctx, ev.Data)
	case "response.completed", "response.failed", "response.incomplete":
		return a.handleTerminal(ctx, event, ev.Data)
	case "response.error", "error":
		return a.handleErrorEvent(event, ev.Data)
	default:
		return nil
	}
}

func openAIStreamEventType(ev route.SSEEvent) (string, error) {
	payloadTypeJSON, validJSON := openAIStreamPayloadType(ev.Data)
	if ev.Event == "" {
		if !validJSON {
			return "", errors.New("decode unlabeled openai-responses stream event: invalid JSON")
		}
		if len(payloadTypeJSON) == 0 {
			return "", errors.New("unlabeled openai-responses stream event is missing type")
		}
	}
	if !validJSON {
		return ev.Event, nil
	}
	if len(payloadTypeJSON) == 0 {
		return ev.Event, nil
	}
	var payloadType string
	if err := json.Unmarshal(payloadTypeJSON, &payloadType); err != nil || payloadType == "" {
		return "", errors.New("openai-responses stream event type must be a non-empty string")
	}
	if ev.Event == "" {
		return payloadType, nil
	}
	if payloadType != ev.Event {
		return "", fmt.Errorf(
			"openai-responses stream event label %q disagrees with payload type %q",
			ev.Event,
			payloadType,
		)
	}
	return ev.Event, nil
}

func openAIStreamPayloadType(data string) (json.RawMessage, bool) {
	var envelope struct {
		Type json.RawMessage `json:"type"`
	}
	if json.Unmarshal([]byte(data), &envelope) != nil {
		return nil, false
	}
	return envelope.Type, true
}

func (a *openAIStreamAccumulator) handleResponseEvidence(event, data string) error {
	var frame struct {
		Response responsesResponse `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode %s: %w", event, err)
	}
	evidence := responseEvidence(frame.Response)
	if evidence.ID != "" {
		a.out.ID = evidence.ID
	}
	if evidence.ServedProviderModelSlug != "" {
		a.out.ServedProviderModelSlug = evidence.ServedProviderModelSlug
	}
	if evidence.Usage != (model.Usage{}) {
		a.out.Usage = evidence.Usage
	}
	return nil
}

func (a *openAIStreamAccumulator) handleErrorEvent(event, data string) error {
	var frame struct {
		ErrorType string         `json:"error_type"`
		Error     responsesError `json:"error"`
		Code      any            `json:"code"`
		Message   string         `json:"message"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return a.recordMalformedErrorEvent(event, data, err)
	}
	if frame.Error.codeText() == "" {
		frame.Error.Code = frame.Code
	}
	if frame.Error.Message == "" {
		frame.Error.Message = frame.Message
	}
	if !frame.Error.present() && frame.ErrorType == "" &&
		providererrors.CodeText(frame.Code) == "" && frame.Message == "" {
		return a.recordMalformedErrorEvent(event, data, errors.New("payload missing error evidence"))
	}
	message := frame.Error.Message
	if message == "" {
		message = "openai-responses stream failed"
	}
	a.streamErr = model.ProviderError{
		Kind: classifyResponsesError(
			a.statusCode,
			0,
			message,
			frame.ErrorType,
			frame.Error.codeText(),
			frame.Error.Type,
		),
		Source:     a.protocol.errorSource(),
		StatusCode: a.statusCode,
		Code: firstNonEmpty(
			frame.ErrorType,
			frame.Error.codeText(),
			frame.Error.Type,
			providererrors.CodeText(frame.Code),
		),
		Message:    message,
		RequestID:  model.RequestIDFromHeader(a.header),
		RetryAfter: model.RetryAfterFromHeader(a.header),
	}
	a.completed = true
	return nil
}

func (a *openAIStreamAccumulator) recordMalformedErrorEvent(event, data string, cause error) error {
	metadata, err := json.Marshal(map[string]any{
		"event":           event,
		"raw_event_bytes": len(data),
	})
	if err != nil {
		return fmt.Errorf("encode malformed openai-responses error evidence: %w", err)
	}
	a.streamErr = model.ProviderError{
		Kind:       model.ErrorKindUnknown,
		Source:     a.protocol.errorSource(),
		StatusCode: a.statusCode,
		Code:       "malformed_error_event",
		Message:    "openai-responses stream returned an undecodable error event",
		RequestID:  model.RequestIDFromHeader(a.header),
		RetryAfter: model.RetryAfterFromHeader(a.header),
		Metadata:   metadata,
		Cause:      cause,
	}
	a.completed = true
	return nil
}

func (a *openAIStreamAccumulator) handleOutputItemAdded(ctx context.Context, data string) error {
	var frame struct {
		Item struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode response.output_item.added: %w", err)
	}
	if strings.TrimSpace(frame.Item.ID) == "" {
		return errors.New("response.output_item.added is missing item id")
	}
	if a.seenItemIDs[frame.Item.ID] {
		return fmt.Errorf("response.output_item.added reused item id %q", frame.Item.ID)
	}
	a.seenItemIDs[frame.Item.ID] = true
	switch frame.Item.Type {
	case "function_call":
		if strings.TrimSpace(frame.Item.CallID) == "" || strings.TrimSpace(frame.Item.Name) == "" {
			return errors.New("response.output_item.added function call is missing call_id or name")
		}
		idx := a.allocBlock()
		a.itemBlockIndex[frame.Item.ID] = idx
		a.emit.BlockStart(ctx, idx, model.StreamBlock{
			Kind:       model.StreamBlockToolUse,
			ToolCallID: frame.Item.CallID,
			ToolName:   frame.Item.Name,
		})
	case "reasoning":
		idx := a.allocBlock()
		a.itemBlockIndex[frame.Item.ID] = idx
		a.emit.BlockStart(ctx, idx, model.StreamBlock{Kind: model.StreamBlockThinking})
	default:
	}
	return nil
}

func (a *openAIStreamAccumulator) handleContentPartAdded(ctx context.Context, data string) error {
	var frame struct {
		ItemID       string `json:"item_id"`
		ContentIndex int    `json:"content_index"`
		Part         struct {
			Type string `json:"type"`
		} `json:"part"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode response.content_part.added: %w", err)
	}
	if frame.Part.Type != "output_text" {
		return nil
	}
	key := messageContentKey(frame.ItemID, frame.ContentIndex)
	if a.seenContentParts[key] {
		return fmt.Errorf("response.content_part.added reused content part %q", key)
	}
	a.seenContentParts[key] = true
	idx := a.allocBlock()
	a.itemContentBlockIndex[key] = idx
	a.emit.BlockStart(ctx, idx, model.StreamBlock{Kind: model.StreamBlockText})
	return nil
}

func (a *openAIStreamAccumulator) handleOutputTextDelta(ctx context.Context, data string) error {
	var frame struct {
		ItemID       string `json:"item_id"`
		ContentIndex int    `json:"content_index"`
		Delta        string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode response.output_text.delta: %w", err)
	}
	idx, ok := a.itemContentBlockIndex[messageContentKey(frame.ItemID, frame.ContentIndex)]
	if !ok {
		return nil
	}
	a.emit.TextDelta(ctx, idx, frame.Delta)
	return nil
}

func (a *openAIStreamAccumulator) handleFunctionCallArgsDelta(ctx context.Context, data string) error {
	var frame struct {
		ItemID string `json:"item_id"`
		Delta  string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode response.function_call_arguments.delta: %w", err)
	}
	idx, ok := a.itemBlockIndex[frame.ItemID]
	if !ok {
		return nil
	}
	a.emit.ToolArgsDelta(ctx, idx, frame.Delta)
	return nil
}

func (a *openAIStreamAccumulator) handleReasoningDelta(ctx context.Context, data string) error {
	var frame struct {
		ItemID string `json:"item_id"`
		Delta  string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode reasoning delta: %w", err)
	}
	idx, ok := a.itemBlockIndex[frame.ItemID]
	if !ok {
		return nil
	}
	a.emit.ThinkingDelta(ctx, idx, frame.Delta)
	return nil
}

func (a *openAIStreamAccumulator) handleContentPartDone(ctx context.Context, data string) error {
	var frame struct {
		ItemID       string `json:"item_id"`
		ContentIndex int    `json:"content_index"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode response.content_part.done: %w", err)
	}
	key := messageContentKey(frame.ItemID, frame.ContentIndex)
	idx, ok := a.itemContentBlockIndex[key]
	if !ok {
		return nil
	}
	delete(a.itemContentBlockIndex, key)
	a.emit.BlockStop(ctx, idx)
	return nil
}

func (a *openAIStreamAccumulator) handleOutputItemDone(ctx context.Context, data string) error {
	var frame struct {
		Item struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode response.output_item.done: %w", err)
	}
	idx, ok := a.itemBlockIndex[frame.Item.ID]
	if !ok {
		return nil
	}
	delete(a.itemBlockIndex, frame.Item.ID)
	a.emit.BlockStop(ctx, idx)
	return nil
}

func (a *openAIStreamAccumulator) handleTerminal(ctx context.Context, event, data string) error {
	var frame struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		return fmt.Errorf("decode %s: %w", event, err)
	}
	if len(frame.Response) == 0 {
		return fmt.Errorf("%s payload missing response", event)
	}
	var statusFrame struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(frame.Response, &statusFrame); err != nil {
		return fmt.Errorf("decode %s response status: %w", event, err)
	}
	expectedStatus := ""
	switch event {
	case "response.completed":
		expectedStatus = "completed"
	case "response.failed":
		expectedStatus = "failed"
	case "response.incomplete":
		expectedStatus = "incomplete"
	}
	if statusFrame.Status != expectedStatus {
		return fmt.Errorf(
			"%s payload has response status %q, want %q",
			event,
			statusFrame.Status,
			expectedStatus,
		)
	}
	resp, err := a.protocol.ParseResponse(ctx, route.Response{
		StatusCode: a.statusCode,
		Header:     a.header,
		Body:       frame.Response,
	})
	a.completed = true
	a.out = resp
	if err != nil {
		if _, ok := model.ClassifyError(err); !ok {
			err = model.ProviderError{
				Kind:       model.ErrorKindUnknown,
				Source:     a.protocol.errorSource(),
				StatusCode: a.statusCode,
				Message:    err.Error(),
				RequestID:  model.RequestIDFromHeader(a.header),
				RetryAfter: model.RetryAfterFromHeader(a.header),
				Cause:      err,
			}
		}
		a.streamErr = err
		return nil
	}
	return nil
}

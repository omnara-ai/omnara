package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type StreamEvent struct {
	Kind       StreamEventKind `json:"kind"`
	BlockIndex int             `json:"block_index,omitempty"`
	Block      *StreamBlock    `json:"block,omitempty"`
	Delta      string          `json:"delta,omitempty"`
	Stop       *StreamStop     `json:"stop,omitempty"`
	Error      *StreamError    `json:"error,omitempty"`
}

// MarshalJSON emits the public ModelOutputStreamDelta wire shape: exactly the
// fields the OpenAPI schema declares for the event's kind, with required
// fields always present.
func (e StreamEvent) MarshalJSON() ([]byte, error) {
	if e.BlockIndex < 0 {
		return nil, fmt.Errorf("stream event block index %d is negative", e.BlockIndex)
	}
	switch e.Kind {
	case StreamEventBlockStart:
		if e.Block == nil {
			return nil, errors.New("block_start stream event requires a block")
		}
		return json.Marshal(struct {
			Kind       StreamEventKind `json:"kind"`
			BlockIndex int             `json:"block_index"`
			Block      StreamBlock     `json:"block"`
		}{e.Kind, e.BlockIndex, *e.Block})
	case StreamEventTextDelta, StreamEventThinkingDelta, StreamEventToolArgsDelta:
		if e.Delta == "" {
			return nil, fmt.Errorf("%s stream event requires a delta", e.Kind)
		}
		return json.Marshal(struct {
			Kind       StreamEventKind `json:"kind"`
			BlockIndex int             `json:"block_index"`
			Delta      string          `json:"delta"`
		}{e.Kind, e.BlockIndex, e.Delta})
	case StreamEventBlockStop:
		return json.Marshal(struct {
			Kind       StreamEventKind `json:"kind"`
			BlockIndex int             `json:"block_index"`
		}{e.Kind, e.BlockIndex})
	case StreamEventMessageStop:
		if e.Stop == nil || e.Stop.Usage == nil {
			return nil, errors.New("message_stop stream event requires stop reason and usage")
		}
		if !streamMessageStopReasonValid(e.Stop.Reason) {
			return nil, fmt.Errorf(
				"message_stop stream event has unsupported stop reason %q",
				e.Stop.Reason,
			)
		}
		return json.Marshal(struct {
			Kind StreamEventKind       `json:"kind"`
			Stop streamMessageStopBody `json:"stop"`
		}{e.Kind, streamMessageStopBody{Reason: e.Stop.Reason, Usage: *e.Stop.Usage}})
	case StreamEventError:
		if e.Error == nil {
			return nil, errors.New("error stream event requires an error")
		}
		return json.Marshal(struct {
			Kind  StreamEventKind `json:"kind"`
			Error StreamError     `json:"error"`
		}{e.Kind, *e.Error})
	default:
		return nil, fmt.Errorf("unsupported stream event kind %q", e.Kind)
	}
}

type streamMessageStopBody struct {
	Reason StopReason `json:"reason"`
	Usage  Usage      `json:"usage"`
}

// streamMessageStopReasonValid owns the ModelStopReason value domain for
// message_stop stream frames; it excludes the durable-only "error" reason.
func streamMessageStopReasonValid(reason StopReason) bool {
	switch reason {
	case StopReasonEndTurn,
		StopReasonToolUse,
		StopReasonMaxTokens,
		StopReasonRefusal,
		StopReasonContentFilter,
		StopReasonPause,
		StopReasonContextWindow,
		StopReasonUnknown:
		return true
	default:
		return false
	}
}

type StreamEventKind string

const (
	StreamEventBlockStart    StreamEventKind = "block_start"
	StreamEventTextDelta     StreamEventKind = "text_delta"
	StreamEventThinkingDelta StreamEventKind = "thinking_delta"
	StreamEventToolArgsDelta StreamEventKind = "tool_arguments_delta"
	StreamEventBlockStop     StreamEventKind = "block_stop"
	StreamEventMessageStop   StreamEventKind = "message_stop"
	StreamEventError         StreamEventKind = "error"
)

type StreamBlockKind string

const (
	StreamBlockText     StreamBlockKind = "text"
	StreamBlockThinking StreamBlockKind = "thinking"
	StreamBlockToolUse  StreamBlockKind = "tool_use"
)

type StreamBlock struct {
	Kind       StreamBlockKind `json:"kind"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
}

func (b StreamBlock) MarshalJSON() ([]byte, error) {
	switch b.Kind {
	case StreamBlockText, StreamBlockThinking:
		return json.Marshal(struct {
			Kind StreamBlockKind `json:"kind"`
		}{b.Kind})
	case StreamBlockToolUse:
		if b.ToolCallID == "" || b.ToolName == "" {
			return nil, errors.New("tool_use stream block requires tool_call_id and tool_name")
		}
		return json.Marshal(struct {
			Kind       StreamBlockKind `json:"kind"`
			ToolCallID string          `json:"tool_call_id"`
			ToolName   string          `json:"tool_name"`
		}{b.Kind, b.ToolCallID, b.ToolName})
	default:
		return nil, fmt.Errorf("unsupported stream block kind %q", b.Kind)
	}
}

type StreamStop struct {
	Reason StopReason `json:"reason,omitempty"`
	Usage  *Usage     `json:"usage,omitempty"`
}

type StreamError struct {
	Message string `json:"message"`
}

type StreamSink interface {
	Emit(ctx context.Context, event StreamEvent)
}

type NoopSink struct{}

func (NoopSink) Emit(context.Context, StreamEvent) {}

type StreamEmitter struct {
	sink StreamSink
}

func NewStreamEmitter(sink StreamSink) StreamEmitter {
	if sink == nil {
		sink = NoopSink{}
	}
	return StreamEmitter{sink: sink}
}

func (e StreamEmitter) BlockStart(ctx context.Context, index int, block StreamBlock) {
	e.sink.Emit(ctx, StreamEvent{Kind: StreamEventBlockStart, BlockIndex: index, Block: &block})
}

func (e StreamEmitter) TextDelta(ctx context.Context, index int, delta string) {
	e.delta(ctx, StreamEventTextDelta, index, delta)
}

func (e StreamEmitter) ThinkingDelta(ctx context.Context, index int, delta string) {
	e.delta(ctx, StreamEventThinkingDelta, index, delta)
}

func (e StreamEmitter) ToolArgsDelta(ctx context.Context, index int, delta string) {
	e.delta(ctx, StreamEventToolArgsDelta, index, delta)
}

func (e StreamEmitter) delta(ctx context.Context, kind StreamEventKind, index int, delta string) {
	if delta == "" {
		return
	}
	e.sink.Emit(ctx, StreamEvent{Kind: kind, BlockIndex: index, Delta: delta})
}

func (e StreamEmitter) BlockStop(ctx context.Context, index int) {
	e.sink.Emit(ctx, StreamEvent{Kind: StreamEventBlockStop, BlockIndex: index})
}

func (e StreamEmitter) MessageStop(ctx context.Context, reason StopReason, usage Usage) {
	e.sink.Emit(ctx, StreamEvent{Kind: StreamEventMessageStop, Stop: &StreamStop{Reason: reason, Usage: &usage}})
}

func (e StreamEmitter) Error(ctx context.Context, message string) {
	e.sink.Emit(ctx, StreamEvent{Kind: StreamEventError, Error: &StreamError{Message: message}})
}

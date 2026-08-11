package model

import (
	"context"
)

type StreamEvent struct {
	Kind       StreamEventKind `json:"kind"`
	BlockIndex int             `json:"block_index,omitempty"`
	Block      *StreamBlock    `json:"block,omitempty"`
	Delta      string          `json:"delta,omitempty"`
	Stop       *StreamStop     `json:"stop,omitempty"`
	Error      *StreamError    `json:"error,omitempty"`
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

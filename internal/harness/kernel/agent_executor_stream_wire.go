package kernel

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/model"
)

type streamDeltaEnvelope struct {
	TurnID             string          `json:"turn_id"`
	ModelCallContextID string          `json:"model_call_context_id"`
	Seq                uint64          `json:"seq"`
	SourceSeqStart     uint64          `json:"source_seq_start"`
	SourceSeqEnd       uint64          `json:"source_seq_end"`
	CoalescedCount     uint64          `json:"coalesced_count"`
	Event              json.RawMessage `json:"event"`
}

func publicStreamDelta(e model.StreamEvent) (json.RawMessage, error) {
	if e.BlockIndex < 0 {
		return nil, fmt.Errorf("stream event block index %d is negative", e.BlockIndex)
	}
	switch e.Kind {
	case model.StreamEventBlockStart:
		if e.Block == nil {
			return nil, errors.New("block_start stream event requires a block")
		}
		block, err := publicStreamBlock(*e.Block)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Kind       model.StreamEventKind `json:"kind"`
			BlockIndex int                   `json:"block_index"`
			Block      streamBlockWire       `json:"block"`
		}{e.Kind, e.BlockIndex, block})
	case model.StreamEventTextDelta, model.StreamEventThinkingDelta, model.StreamEventToolArgsDelta:
		if e.Delta == "" {
			return nil, fmt.Errorf("%s stream event requires a delta", e.Kind)
		}
		return json.Marshal(struct {
			Kind       model.StreamEventKind `json:"kind"`
			BlockIndex int                   `json:"block_index"`
			Delta      string                `json:"delta"`
		}{e.Kind, e.BlockIndex, e.Delta})
	case model.StreamEventBlockStop:
		return json.Marshal(struct {
			Kind       model.StreamEventKind `json:"kind"`
			BlockIndex int                   `json:"block_index"`
		}{e.Kind, e.BlockIndex})
	case model.StreamEventMessageStop:
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
			Kind model.StreamEventKind `json:"kind"`
			Stop streamMessageStopBody `json:"stop"`
		}{e.Kind, streamMessageStopBody{Reason: e.Stop.Reason, Usage: *e.Stop.Usage}})
	case model.StreamEventError:
		if e.Error == nil {
			return nil, errors.New("error stream event requires an error")
		}
		return json.Marshal(struct {
			Kind  model.StreamEventKind `json:"kind"`
			Error model.StreamError     `json:"error"`
		}{e.Kind, *e.Error})
	default:
		return nil, fmt.Errorf("unsupported stream event kind %q", e.Kind)
	}
}

type streamBlockWire struct {
	Kind       model.StreamBlockKind `json:"kind"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	ToolName   string                `json:"tool_name,omitempty"`
}

func publicStreamBlock(b model.StreamBlock) (streamBlockWire, error) {
	switch b.Kind {
	case model.StreamBlockText, model.StreamBlockThinking:
		return streamBlockWire{Kind: b.Kind}, nil
	case model.StreamBlockToolUse:
		if b.ToolCallID == "" || b.ToolName == "" {
			return streamBlockWire{}, errors.New(
				"tool_use stream block requires tool_call_id and tool_name",
			)
		}
		return streamBlockWire{Kind: b.Kind, ToolCallID: b.ToolCallID, ToolName: b.ToolName}, nil
	default:
		return streamBlockWire{}, fmt.Errorf("unsupported stream block kind %q", b.Kind)
	}
}

type streamMessageStopBody struct {
	Reason model.StopReason `json:"reason"`
	Usage  model.Usage      `json:"usage"`
}

func streamMessageStopReasonValid(reason model.StopReason) bool {
	switch reason {
	case model.StopReasonEndTurn,
		model.StopReasonToolUse,
		model.StopReasonMaxTokens,
		model.StopReasonRefusal,
		model.StopReasonContentFilter,
		model.StopReasonPause,
		model.StopReasonContextWindow,
		model.StopReasonUnknown:
		return true
	default:
		return false
	}
}

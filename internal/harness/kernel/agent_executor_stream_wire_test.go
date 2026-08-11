package kernel

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
)

func TestPublicStreamDeltaExactWireShapes(t *testing.T) {
	usage := model.Usage{InputTokens: 12, OutputTokens: 3}
	cases := []struct {
		name  string
		event model.StreamEvent
		want  string
	}{
		{
			name: "block_start text",
			event: model.StreamEvent{
				Kind:       model.StreamEventBlockStart,
				BlockIndex: 1,
				Block:      &model.StreamBlock{Kind: model.StreamBlockText},
			},
			want: `{"kind":"block_start","block_index":1,"block":{"kind":"text"}}`,
		},
		{
			name: "block_start thinking drops tool fields",
			event: model.StreamEvent{
				Kind:  model.StreamEventBlockStart,
				Block: &model.StreamBlock{Kind: model.StreamBlockThinking, ToolCallID: "x", ToolName: "y"},
			},
			want: `{"kind":"block_start","block_index":0,"block":{"kind":"thinking"}}`,
		},
		{
			name: "block_start tool_use",
			event: model.StreamEvent{
				Kind: model.StreamEventBlockStart,
				Block: &model.StreamBlock{
					Kind:       model.StreamBlockToolUse,
					ToolCallID: "tlc_abc",
					ToolName:   "shell",
				},
			},
			want: `{"kind":"block_start","block_index":0,` +
				`"block":{"kind":"tool_use","tool_call_id":"tlc_abc","tool_name":"shell"}}`,
		},
		{
			name:  "text_delta",
			event: model.StreamEvent{Kind: model.StreamEventTextDelta, BlockIndex: 2, Delta: "hi"},
			want:  `{"kind":"text_delta","block_index":2,"delta":"hi"}`,
		},
		{
			name:  "thinking_delta",
			event: model.StreamEvent{Kind: model.StreamEventThinkingDelta, Delta: "hmm"},
			want:  `{"kind":"thinking_delta","block_index":0,"delta":"hmm"}`,
		},
		{
			name:  "tool_arguments_delta",
			event: model.StreamEvent{Kind: model.StreamEventToolArgsDelta, Delta: `{"cmd":`},
			want:  `{"kind":"tool_arguments_delta","block_index":0,"delta":"{\"cmd\":"}`,
		},
		{
			name:  "block_stop",
			event: model.StreamEvent{Kind: model.StreamEventBlockStop, BlockIndex: 3},
			want:  `{"kind":"block_stop","block_index":3}`,
		},
		{
			name: "message_stop excludes block_index",
			event: model.StreamEvent{
				Kind: model.StreamEventMessageStop,
				Stop: &model.StreamStop{Reason: model.StopReasonEndTurn, Usage: &usage},
			},
			want: `{"kind":"message_stop",` +
				`"stop":{"reason":"end_turn","usage":{"input_tokens_total":12,"output_tokens_total":3}}}`,
		},
		{
			name: "message_stop with zero usage",
			event: model.StreamEvent{
				Kind: model.StreamEventMessageStop,
				Stop: &model.StreamStop{Reason: model.StopReasonUnknown, Usage: &model.Usage{}},
			},
			want: `{"kind":"message_stop","stop":{"reason":"unknown","usage":{}}}`,
		},
		{
			name:  "error",
			event: model.StreamEvent{Kind: model.StreamEventError, Error: &model.StreamError{Message: "boom"}},
			want:  `{"kind":"error","error":{"message":"boom"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := publicStreamDelta(tc.event)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPublicStreamDeltaRejectsInvalidShapes(t *testing.T) {
	usage := model.Usage{}
	cases := []struct {
		name  string
		event model.StreamEvent
	}{
		{name: "unknown kind", event: model.StreamEvent{Kind: "bogus"}},
		{name: "negative block index", event: model.StreamEvent{Kind: model.StreamEventBlockStop, BlockIndex: -1}},
		{name: "block_start without block", event: model.StreamEvent{Kind: model.StreamEventBlockStart}},
		{
			name: "block_start unknown block kind",
			event: model.StreamEvent{
				Kind:  model.StreamEventBlockStart,
				Block: &model.StreamBlock{Kind: "bogus"},
			},
		},
		{
			name: "tool_use block without ids",
			event: model.StreamEvent{
				Kind:  model.StreamEventBlockStart,
				Block: &model.StreamBlock{Kind: model.StreamBlockToolUse},
			},
		},
		{name: "empty delta", event: model.StreamEvent{Kind: model.StreamEventTextDelta}},
		{name: "message_stop without stop", event: model.StreamEvent{Kind: model.StreamEventMessageStop}},
		{
			name: "message_stop without usage",
			event: model.StreamEvent{
				Kind: model.StreamEventMessageStop,
				Stop: &model.StreamStop{Reason: model.StopReasonEndTurn},
			},
		},
		{
			name: "message_stop with durable-only error reason",
			event: model.StreamEvent{
				Kind: model.StreamEventMessageStop,
				Stop: &model.StreamStop{Reason: model.StopReasonError, Usage: &usage},
			},
		},
		{
			name: "message_stop with empty reason",
			event: model.StreamEvent{
				Kind: model.StreamEventMessageStop,
				Stop: &model.StreamStop{Usage: &usage},
			},
		},
		{name: "error without payload", event: model.StreamEvent{Kind: model.StreamEventError}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := publicStreamDelta(tc.event); err == nil {
				t.Fatalf("marshal succeeded with %s, want error", got)
			}
		})
	}
}

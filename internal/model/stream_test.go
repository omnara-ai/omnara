package model

import (
	"encoding/json"
	"testing"
)

func TestStreamEventMarshalExactWireShapes(t *testing.T) {
	usage := Usage{InputTokens: 12, OutputTokens: 3}
	cases := []struct {
		name  string
		event StreamEvent
		want  string
	}{
		{
			name: "block_start text",
			event: StreamEvent{
				Kind:       StreamEventBlockStart,
				BlockIndex: 1,
				Block:      &StreamBlock{Kind: StreamBlockText},
			},
			want: `{"kind":"block_start","block_index":1,"block":{"kind":"text"}}`,
		},
		{
			name: "block_start thinking drops tool fields",
			event: StreamEvent{
				Kind:  StreamEventBlockStart,
				Block: &StreamBlock{Kind: StreamBlockThinking, ToolCallID: "x", ToolName: "y"},
			},
			want: `{"kind":"block_start","block_index":0,"block":{"kind":"thinking"}}`,
		},
		{
			name: "block_start tool_use",
			event: StreamEvent{
				Kind: StreamEventBlockStart,
				Block: &StreamBlock{
					Kind:       StreamBlockToolUse,
					ToolCallID: "tlc_abc",
					ToolName:   "shell",
				},
			},
			want: `{"kind":"block_start","block_index":0,` +
				`"block":{"kind":"tool_use","tool_call_id":"tlc_abc","tool_name":"shell"}}`,
		},
		{
			name:  "text_delta",
			event: StreamEvent{Kind: StreamEventTextDelta, BlockIndex: 2, Delta: "hi"},
			want:  `{"kind":"text_delta","block_index":2,"delta":"hi"}`,
		},
		{
			name:  "thinking_delta",
			event: StreamEvent{Kind: StreamEventThinkingDelta, Delta: "hmm"},
			want:  `{"kind":"thinking_delta","block_index":0,"delta":"hmm"}`,
		},
		{
			name:  "tool_arguments_delta",
			event: StreamEvent{Kind: StreamEventToolArgsDelta, Delta: `{"cmd":`},
			want:  `{"kind":"tool_arguments_delta","block_index":0,"delta":"{\"cmd\":"}`,
		},
		{
			name:  "block_stop",
			event: StreamEvent{Kind: StreamEventBlockStop, BlockIndex: 3},
			want:  `{"kind":"block_stop","block_index":3}`,
		},
		{
			name: "message_stop excludes block_index",
			event: StreamEvent{
				Kind: StreamEventMessageStop,
				Stop: &StreamStop{Reason: StopReasonEndTurn, Usage: &usage},
			},
			want: `{"kind":"message_stop",` +
				`"stop":{"reason":"end_turn","usage":{"input_tokens_total":12,"output_tokens_total":3}}}`,
		},
		{
			name: "message_stop with zero usage",
			event: StreamEvent{
				Kind: StreamEventMessageStop,
				Stop: &StreamStop{Reason: StopReasonUnknown, Usage: &Usage{}},
			},
			want: `{"kind":"message_stop","stop":{"reason":"unknown","usage":{}}}`,
		},
		{
			name:  "error",
			event: StreamEvent{Kind: StreamEventError, Error: &StreamError{Message: "boom"}},
			want:  `{"kind":"error","error":{"message":"boom"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.event)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStreamEventMarshalRejectsInvalidShapes(t *testing.T) {
	usage := Usage{}
	cases := []struct {
		name  string
		event StreamEvent
	}{
		{name: "unknown kind", event: StreamEvent{Kind: "bogus"}},
		{name: "negative block index", event: StreamEvent{Kind: StreamEventBlockStop, BlockIndex: -1}},
		{name: "block_start without block", event: StreamEvent{Kind: StreamEventBlockStart}},
		{
			name: "block_start unknown block kind",
			event: StreamEvent{
				Kind:  StreamEventBlockStart,
				Block: &StreamBlock{Kind: "bogus"},
			},
		},
		{
			name: "tool_use block without ids",
			event: StreamEvent{
				Kind:  StreamEventBlockStart,
				Block: &StreamBlock{Kind: StreamBlockToolUse},
			},
		},
		{name: "empty delta", event: StreamEvent{Kind: StreamEventTextDelta}},
		{name: "message_stop without stop", event: StreamEvent{Kind: StreamEventMessageStop}},
		{
			name: "message_stop without usage",
			event: StreamEvent{
				Kind: StreamEventMessageStop,
				Stop: &StreamStop{Reason: StopReasonEndTurn},
			},
		},
		{
			name: "message_stop with durable-only error reason",
			event: StreamEvent{
				Kind: StreamEventMessageStop,
				Stop: &StreamStop{Reason: StopReasonError, Usage: &usage},
			},
		},
		{
			name: "message_stop with empty reason",
			event: StreamEvent{
				Kind: StreamEventMessageStop,
				Stop: &StreamStop{Usage: &usage},
			},
		},
		{name: "error without payload", event: StreamEvent{Kind: StreamEventError}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := json.Marshal(tc.event); err == nil {
				t.Fatalf("marshal succeeded with %s, want error", got)
			}
		})
	}
}

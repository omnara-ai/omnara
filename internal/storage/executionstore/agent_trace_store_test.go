package executionstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/oteltrace"
)

func TestTraceLinkSourceReadsIdempotencyKeys(t *testing.T) {
	id := uuid.MustParse("01a1eb1c-8218-73ff-a0fb-365f42fa2164")
	for _, test := range []struct {
		scope, key   string
		kind         string
		fromToolCall bool
		ok           bool
	}{
		{"content_input", "launch:spawn:" + id.String() + ":content-input", oteltrace.LinkSpawnedBy, true, true},
		{"subagent_message", "tool_call:" + id.String(), oteltrace.LinkMessageFrom, true, true},
		{"subagent_message", "model_output:" + id.String(), oteltrace.LinkResultFrom, false, true},
		// A caller-chosen key that merely looks like one of ours is not a source.
		{"content_input", "tool_call:" + id.String(), "", false, false},
		{"content_input", "launch:spawn:not-a-uuid:content-input", "", false, false},
		{"subagent_message", "interaction:" + id.String(), "", false, false},
		{"agent_interaction_response", id.String(), "", false, false},
	} {
		kind, got, fromToolCall, ok := traceLinkSource(test.scope, test.key)
		if ok != test.ok || (ok && (kind != test.kind || got != id || fromToolCall != test.fromToolCall)) {
			t.Errorf("traceLinkSource(%q, %q) = %q %s %v %v", test.scope, test.key, kind, got, fromToolCall, ok)
		}
	}
}

package tools

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestToolCallMatchesPreservesJSONNumberIdentity(t *testing.T) {
	t.Parallel()
	if !toolCallMatches(
		executionstore.ToolCallRecord{
			Name:  "test",
			Input: json.RawMessage(`{"value":9007199254740993}`),
		},
		model.ToolCall{Name: "test", Input: json.RawMessage(`{"value": 9007199254740993}`)},
	) {
		t.Fatal("equivalent large integers did not match")
	}
	if toolCallMatches(
		executionstore.ToolCallRecord{
			Name:  "test",
			Input: json.RawMessage(`{"value":9007199254740992}`),
		},
		model.ToolCall{Name: "test", Input: json.RawMessage(`{"value":9007199254740993}`)},
	) {
		t.Fatal("distinct large integers matched")
	}
}

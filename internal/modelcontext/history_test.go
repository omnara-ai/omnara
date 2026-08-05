package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestCanonicalHistoryUsesStoredContentOrder(t *testing.T) {
	history, err := CanonicalHistory(Bundle{
		Messages: []Message{{
			ID:                 "output",
			ModelCallContextID: "context",
			Role:               modelprotocol.RoleAssistant,
			Sequence:           20,
			Content: json.RawMessage(`[
				{"type":"text","text":"before"},
				{"type":"tool_call","tool_call_id":"tool-1"},
				{"type":"reasoning","text":"between"},
				{"type":"tool_call","tool_call_id":"tool-2"},
				{"type":"text","text":"after"}
			]`),
		}},
		ToolResults: []ToolResultRef{
			{ToolCallID: "tool-2", ModelCallContextID: "context", ProviderCallID: "call-2"},
			{ToolCallID: "tool-1", ModelCallContextID: "context", ProviderCallID: "call-1"},
		},
	})
	if err != nil {
		t.Fatalf("canonical history: %v", err)
	}
	if len(history) != 1 || len(history[0].AssistantContent) != 5 {
		t.Fatalf("history = %+v", history)
	}
	got := make([]string, 0, len(history[0].AssistantContent))
	for _, entry := range history[0].AssistantContent {
		switch entry := entry.(type) {
		case AssistantToolCallEntry:
			got = append(got, entry.ToolCall.ProviderCallID)
		case AssistantBlockEntry:
			got = append(got, string(entry.Content))
		default:
			t.Fatalf("unexpected assistant content entry %T", entry)
		}
	}
	if strings.Join(got, "|") !=
		`{"type":"text","text":"before"}|call-1|{"type":"reasoning","text":"between"}|call-2|{"type":"text","text":"after"}` {
		t.Fatalf("assistant content = %s", strings.Join(got, "|"))
	}
	if len(history[0].ToolResults) != 2 ||
		history[0].ToolResults[0].ProviderCallID != "call-1" ||
		history[0].ToolResults[1].ProviderCallID != "call-2" {
		t.Fatalf("tool result order = %+v", history[0].ToolResults)
	}
}

func TestCanonicalHistoryKeepsPriorTurnToolsBeforeLaterMessages(t *testing.T) {
	history, err := CanonicalHistory(Bundle{
		Messages: []Message{
			{
				ID:                 "prior-output",
				ModelCallContextID: "prior-context",
				Role:               modelprotocol.RoleAssistant,
				Sequence:           20,
				Content:            json.RawMessage(`[{"type":"tool_call","tool_call_id":"prior-tool"}]`),
			},
			{
				ID:       "next-turn",
				Role:     modelprotocol.RoleUser,
				Sequence: 50,
				Content:  json.RawMessage(`[]`),
			},
		},
		ToolResults: []ToolResultRef{{
			ToolCallID:          "prior-tool",
			ModelCallContextID:  "prior-context",
			ProviderCallID:      "provider-call",
			SourceEventSequence: 20,
			ResultEventSequence: 30,
		}},
	})
	if err != nil {
		t.Fatalf("canonical history: %v", err)
	}
	if len(history) != 2 ||
		history[0].Sequence != 20 ||
		len(history[0].ToolResults) != 1 ||
		history[1].Message.ID != "next-turn" {
		t.Fatalf("prior tool exchange moved after next turn: %+v", history)
	}
}

func TestCanonicalHistoryRejectsToolResultWithoutSourceOutput(t *testing.T) {
	_, err := CanonicalHistory(Bundle{ToolResults: []ToolResultRef{{
		ToolCallID:         "tool",
		ModelCallContextID: "context",
		ProviderCallID:     "call",
	}}})
	if err == nil {
		t.Fatal("tool result without its source output was accepted")
	}
}

func TestCanonicalHistoryRejectsToolCallWithoutResult(t *testing.T) {
	_, err := CanonicalHistory(Bundle{Messages: []Message{{
		ID:                 "output",
		ModelCallContextID: "context",
		Role:               modelprotocol.RoleAssistant,
		Sequence:           1,
		Content:            json.RawMessage(`[{"type":"tool_call","tool_call_id":"tool"}]`),
	}}})
	if err == nil {
		t.Fatal("tool call without a completed result was accepted")
	}
}

func TestCanonicalHistoryRejectsMissingSequence(t *testing.T) {
	_, err := CanonicalHistory(Bundle{Messages: []Message{{
		ID:      "message",
		Role:    modelprotocol.RoleUser,
		Content: json.RawMessage(`[]`),
	}}})
	if err == nil {
		t.Fatal("message without an event sequence was accepted")
	}
}

func TestCanonicalHistoryRejectsProviderWireRole(t *testing.T) {
	_, err := CanonicalHistory(Bundle{Messages: []Message{{
		ID:       "message",
		Role:     modelprotocol.MessageRole("system"),
		Sequence: 1,
		Content:  json.RawMessage(`[{"type":"text","text":"wire-only role"}]`),
	}}})
	if err == nil || !strings.Contains(err.Error(), "unsupported message role") {
		t.Fatalf("provider wire role error = %v", err)
	}
}

package tools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
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

func TestMachineUnavailableToolResultGuidesKnownResolutionFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cause     error
		errorCode string
		message   string
	}{
		{
			name:      "no executable machine",
			cause:     ErrNoActiveAgentMachineBinding,
			errorCode: ErrNoActiveAgentMachineBinding.Error(),
			message:   "no executable machine is available",
		},
		{
			name:      "selection required",
			cause:     ErrMachineSelectionRequired,
			errorCode: ErrMachineSelectionRequired.Error(),
			message:   "machine_ref is required when multiple machines are available",
		},
		{
			name:      "ref unavailable",
			cause:     ErrMachineRefUnavailable,
			errorCode: ErrMachineRefUnavailable.Error(),
			message:   "machine_ref is unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := machineUnavailableToolResult(test.cause)
			if err != nil {
				t.Fatalf("machine unavailable result: %v", err)
			}
			body := unitStructuredResultBody(t, result.Content)
			if body["error_code"] != test.errorCode || body["error"] != test.message ||
				body["next_action"] != toolcatalog.ToolNameListMachines {
				t.Fatalf("machine unavailable result = %+v", body)
			}
		})
	}
}

func TestMachineUnavailableToolResultDoesNotGuideUnexpectedFailures(t *testing.T) {
	t.Parallel()
	cause := errors.New("database unavailable")
	result, err := machineUnavailableToolResult(cause)
	if err != nil {
		t.Fatalf("machine unavailable result: %v", err)
	}
	body := unitStructuredResultBody(t, result.Content)
	if body["error_code"] != cause.Error() || body["error"] != cause.Error() {
		t.Fatalf("unexpected machine failure result = %+v", body)
	}
	if _, ok := body["next_action"]; ok {
		t.Fatalf("unexpected machine failure includes next_action: %+v", body)
	}
}

func unitStructuredResultBody(t *testing.T, content toolResultContent) map[string]any {
	t.Helper()
	if len(content.parts) != 1 {
		t.Fatalf("unexpected result parts: %+v", content.parts)
	}
	part, ok := content.parts[0].(toolResultStructuredPart)
	if !ok {
		t.Fatalf("unexpected result part: %T", content.parts[0])
	}
	var body map[string]any
	if err := json.Unmarshal(part.value, &body); err != nil {
		t.Fatalf("decode structured result: %v", err)
	}
	return body
}

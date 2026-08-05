package modelcontext

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func modelToolResultContentParts(
	outcome executionstore.ToolResultOutcome,
	domainParts json.RawMessage,
) (json.RawMessage, error) {
	if !outcome.IsTerminal() {
		return nil, fmt.Errorf("terminal tool outcome is required")
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(domainParts, &parts); err != nil {
		return nil, fmt.Errorf("decode domain content parts: %w", err)
	}
	outcomePart, err := marshalJSON(map[string]any{
		"type": "structured_data",
		"value": map[string]any{
			"outcome": outcome,
		},
	})
	if err != nil {
		return nil, err
	}
	return marshalJSON(append([]json.RawMessage{outcomePart}, parts...))
}

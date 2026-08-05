package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestModelToolResultContentPartsAlwaysLeadWithOutcome(t *testing.T) {
	for _, outcome := range []executionstore.ToolResultOutcome{
		executionstore.ToolResultOutcomeSucceeded,
		executionstore.ToolResultOutcomeFailed,
		executionstore.ToolResultOutcomeDenied,
		executionstore.ToolResultOutcomeCanceled,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			parts, err := modelToolResultContentParts(
				outcome,
				json.RawMessage(`[{"type":"text","text":"domain result"}]`),
			)
			if err != nil {
				t.Fatalf("project tool result: %v", err)
			}
			var projected []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Value struct {
					Outcome executionstore.ToolResultOutcome `json:"outcome"`
				} `json:"value"`
			}
			if err := json.Unmarshal(parts, &projected); err != nil {
				t.Fatalf("decode projected parts: %v", err)
			}
			if len(projected) != 2 ||
				projected[0].Type != "structured_data" ||
				projected[0].Value.Outcome != outcome ||
				projected[1].Type != "text" ||
				projected[1].Text != "domain result" {
				t.Fatalf("projected parts = %s", parts)
			}
		})
	}
}

func TestModelToolResultContentPartsRejectsMissingOutcome(t *testing.T) {
	if _, err := modelToolResultContentParts("", json.RawMessage(`[]`)); err == nil {
		t.Fatal("missing terminal outcome was accepted")
	}
}

func TestModelToolResultContentPartsNeedsNoDomainContent(t *testing.T) {
	parts, err := modelToolResultContentParts(
		executionstore.ToolResultOutcomeDenied,
		json.RawMessage(`[]`),
	)
	if err != nil {
		t.Fatalf("project outcome-only tool result: %v", err)
	}
	if string(parts) != `[{"type":"structured_data","value":{"outcome":"denied"}}]` {
		t.Fatalf("outcome-only parts = %s", parts)
	}
}

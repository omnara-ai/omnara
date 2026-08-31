package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestProjectionNormalizerRejectsInvalidIntegrationTargets(t *testing.T) {
	base := Bundle{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		TurnID:             testTurnID,
		OpeningInputIDs:    []storage.ID{testInputID},
		InputEventSequence: 1,
	}
	tests := []struct {
		name    string
		targets []IntegrationTargetRef
	}{
		{
			name: "missing target ref",
			targets: []IntegrationTargetRef{{
				DurableID:       "durable",
				Provider:        "slack",
				ProviderRefKind: "thread",
				Label:           "slack thread",
			}},
		},
		{
			name: "missing durable id",
			targets: []IntegrationTargetRef{{
				TargetRef:       "slack-abcd",
				Provider:        "slack",
				ProviderRefKind: "thread",
				Label:           "slack thread",
			}},
		},
		{
			name: "missing label",
			targets: []IntegrationTargetRef{{
				TargetRef:       "slack-abcd",
				DurableID:       "durable",
				Provider:        "slack",
				ProviderRefKind: "thread",
			}},
		},
		{
			name: "duplicate target ref",
			targets: []IntegrationTargetRef{
				{TargetRef: "slack-abcd", DurableID: "durable-1", Provider: "slack", ProviderRefKind: "thread", Label: "one"},
				{TargetRef: "slack-abcd", DurableID: "durable-2", Provider: "slack", ProviderRefKind: "thread", Label: "two"},
			},
		},
		{
			name: "multiple current",
			targets: []IntegrationTargetRef{
				{
					TargetRef:       "slack-abcd",
					DurableID:       "durable-1",
					Provider:        "slack",
					ProviderRefKind: "thread",
					Label:           "one",
					IsCurrent:       true,
				},
				{
					TargetRef:       "slack-defg",
					DurableID:       "durable-2",
					Provider:        "slack",
					ProviderRefKind: "thread",
					Label:           "two",
					IsCurrent:       true,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle := base
			bundle.IntegrationTargets = tt.targets
			if err := (ProjectionNormalizer{}).Normalize(bundle); err == nil {
				t.Fatalf("expected invalid integration targets to fail")
			}
		})
	}
}

func TestProjectionNormalizerAcceptsIntegrationTargets(t *testing.T) {
	bundle := Bundle{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		TurnID:             testTurnID,
		OpeningInputIDs:    []storage.ID{testInputID},
		InputEventSequence: 1,
		IntegrationTargets: []IntegrationTargetRef{
			{
				TargetRef:       "slack-abcd",
				DurableID:       "durable",
				Provider:        "slack",
				ProviderRefKind: "thread",
				Label:           "slack thread",
				IsCurrent:       true,
			},
		},
	}
	if err := (ProjectionNormalizer{}).Normalize(bundle); err != nil {
		t.Fatalf("normalize integration targets: %v", err)
	}
}

func TestProjectionNormalizerAcceptsAssistantMessageRole(t *testing.T) {
	bundle := Bundle{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		TurnID:             testTurnID,
		OpeningInputIDs:    []storage.ID{testInputID},
		InputEventSequence: 1,
		Messages: []Message{
			{
				ID:       testIDN(20).String(),
				Role:     modelprotocol.RoleAssistant,
				Sequence: 1,
				Content:  json.RawMessage(`[{"type":"text","text":"hello"}]`),
			},
		},
	}
	if err := (ProjectionNormalizer{}).Normalize(bundle); err != nil {
		t.Fatalf("normalize assistant message role: %v", err)
	}
}

func TestProjectionNormalizerEnforcesOwnerSpecificContentBlocks(t *testing.T) {
	base := Bundle{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		TurnID:             testTurnID,
		OpeningInputIDs:    []storage.ID{testInputID},
		InputEventSequence: 2,
	}
	validToolResult := ToolResultRef{
		ToolCallID:          "call",
		DurableID:           "result",
		ModelCallContextID:  "context",
		ProviderCallID:      "provider-call",
		Name:                "run_command",
		Input:               json.RawMessage(`{}`),
		Outcome:             executionstore.ToolResultOutcomeSucceeded,
		SourceEventSequence: 1,
		ResultEventSequence: 2,
		ContentParts:        json.RawMessage(`[]`),
	}
	tests := []struct {
		name        string
		messages    []Message
		toolResults []ToolResultRef
	}{
		{
			name: "agent input rejects structured data",
			messages: []Message{{
				ID:       testIDN(20).String(),
				Role:     modelprotocol.RoleUser,
				Sequence: 1,
				Content:  json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
			}},
		},
		{
			name: "agent input rejects provider field",
			messages: []Message{{
				ID:       testIDN(20).String(),
				Role:     modelprotocol.RoleUser,
				Sequence: 1,
				Content:  json.RawMessage(`[{"type":"text","text":"hello","provider_replay":{}}]`),
			}},
		},
		{
			name: "model output rejects tool result data",
			messages: []Message{{
				ID:       testIDN(20).String(),
				Role:     modelprotocol.RoleAssistant,
				Sequence: 1,
				Content:  json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
			}},
		},
		{
			name: "tool result rejects reasoning",
			toolResults: []ToolResultRef{func() ToolResultRef {
				result := validToolResult
				result.ContentParts = json.RawMessage(`[{"type":"reasoning","text":"hidden"}]`)
				return result
			}()},
		},
		{
			name: "tool result rejects misplaced field",
			toolResults: []ToolResultRef{func() ToolResultRef {
				result := validToolResult
				result.ContentParts = json.RawMessage(
					`[{"type":"structured_data","value":{"ok":true},"tool_call_id":"internal"}]`,
				)
				return result
			}()},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := base
			bundle.Messages = test.messages
			bundle.ToolResults = test.toolResults
			if err := (ProjectionNormalizer{}).Normalize(bundle); err == nil {
				t.Fatal("expected owner-specific content validation to fail")
			}
		})
	}
}

func TestProjectionNormalizerPreservesStructuredDataValue(t *testing.T) {
	bundle := Bundle{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		TurnID:             testTurnID,
		OpeningInputIDs:    []storage.ID{testInputID},
		InputEventSequence: 2,
		ToolResults: []ToolResultRef{{
			ToolCallID:          "call",
			DurableID:           "result",
			ModelCallContextID:  "context",
			ProviderCallID:      "provider-call",
			Name:                "run_command",
			Input:               json.RawMessage(`{}`),
			Outcome:             executionstore.ToolResultOutcomeSucceeded,
			SourceEventSequence: 1,
			ResultEventSequence: 2,
			ContentParts: json.RawMessage(`[{"type":"structured_data","value":{` +
				`"runtime_lock_id":"domain-value","items":[1,true,null]}}]`),
		}},
	}
	if err := (ProjectionNormalizer{}).Normalize(bundle); err != nil {
		t.Fatalf("normalize structured tool result: %v", err)
	}
}

func TestProjectionNormalizerRejectsCoreProjectionInvariantBreaks(t *testing.T) {
	sourceMessageID := testIDN(20).String()
	base := Bundle{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		TurnID:             testTurnID,
		OpeningInputIDs:    []storage.ID{testInputID},
		InputEventSequence: 5,
		Messages: []Message{
			{ID: sourceMessageID, Role: "user", Sequence: 5, Content: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
		},
	}
	tests := []struct {
		name   string
		mutate func(*Bundle)
	}{
		{name: "checkpoint tail overlap", mutate: func(bundle *Bundle) {
			bundle.ContextCheckpoint = &CheckpointRef{
				ID: "ccp", SummarizedThroughEventSequence: 5, Summary: "summary",
			}
		}},
		{name: "duplicate tool results", mutate: func(bundle *Bundle) {
			bundle.ToolResults = []ToolResultRef{
				{
					ToolCallID:          "call",
					DurableID:           "tlc",
					ProviderCallID:      "call",
					Name:                "run_command",
					SourceEventSequence: 3,
					ResultEventSequence: 4,
					Outcome:             executionstore.ToolResultOutcomeSucceeded,
					ContentParts:        json.RawMessage(`[]`),
				},
				{
					ToolCallID:          "call",
					DurableID:           "tlc2",
					ProviderCallID:      "call",
					Name:                "run_command",
					SourceEventSequence: 3,
					ResultEventSequence: 4,
					Outcome:             executionstore.ToolResultOutcomeSucceeded,
					ContentParts:        json.RawMessage(`[]`),
				},
			}
		}},
		{name: "missing tool result provider call id", mutate: func(bundle *Bundle) {
			bundle.ToolResults = []ToolResultRef{{
				ToolCallID:          "tlc",
				DurableID:           "tlc",
				Name:                "run_command",
				SourceEventSequence: 3,
				ResultEventSequence: 4,
				Outcome:             executionstore.ToolResultOutcomeSucceeded,
				ContentParts:        json.RawMessage(`[]`),
			}}
		}},
		{name: "missing tool result chronology", mutate: func(bundle *Bundle) {
			bundle.ToolResults = []ToolResultRef{{
				ToolCallID:     "tlc",
				DurableID:      "tlr",
				ProviderCallID: "call",
				Name:           "run_command",
				Outcome:        executionstore.ToolResultOutcomeSucceeded,
				ContentParts:   json.RawMessage(`[]`),
			}}
		}},
		{name: "missing tool result outcome", mutate: func(bundle *Bundle) {
			bundle.ToolResults = []ToolResultRef{{
				ToolCallID:          "call",
				DurableID:           "tlc",
				ProviderCallID:      "call",
				Name:                "run_command",
				SourceEventSequence: 3,
				ResultEventSequence: 4,
				ContentParts:        json.RawMessage(`[]`),
			}}
		}},
		{name: "reversed tool result chronology", mutate: func(bundle *Bundle) {
			bundle.ToolResults = []ToolResultRef{{
				ToolCallID:          "tlc",
				DurableID:           "tlr",
				ProviderCallID:      "call",
				Name:                "run_command",
				ContentParts:        json.RawMessage(`[]`),
				SourceEventSequence: 5,
				ResultEventSequence: 4,
				Outcome:             executionstore.ToolResultOutcomeSucceeded,
			}}
		}},
		{name: "equal tool result chronology", mutate: func(bundle *Bundle) {
			bundle.ToolResults = []ToolResultRef{{
				ToolCallID:          "tlc",
				DurableID:           "tlr",
				ProviderCallID:      "call",
				Name:                "run_command",
				ContentParts:        json.RawMessage(`[]`),
				SourceEventSequence: 4,
				ResultEventSequence: 4,
				Outcome:             executionstore.ToolResultOutcomeSucceeded,
			}}
		}},
		{name: "message past watermark", mutate: func(bundle *Bundle) { bundle.Messages[0].Sequence = 6 }},
		{name: "message missing sequence", mutate: func(bundle *Bundle) { bundle.Messages[0].Sequence = 0 }},
		{
			name: "unsupported message role",
			mutate: func(bundle *Bundle) {
				bundle.Messages[0].Role = modelprotocol.MessageRole("system")
			},
		},
		{
			name: "malformed message content",
			mutate: func(bundle *Bundle) {
				bundle.Messages[0].Content = json.RawMessage(`{"type":"text"}`)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle := base
			bundle.Messages = append([]Message(nil), base.Messages...)
			tt.mutate(&bundle)
			if err := (ProjectionNormalizer{}).Normalize(bundle); err == nil {
				t.Fatal("expected invalid projection to fail")
			}
		})
	}
}

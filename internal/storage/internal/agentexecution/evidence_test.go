package agentexecution

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestBindToolCallsUsesProviderEnvelopeAsCanonicalSource(t *testing.T) {
	id := uuid.New()
	envelope := modelenvelope.ResponseEnvelope{
		Normalized: modelenvelope.ResponseNormalized{
			Content: []modelenvelope.ResponsePart{
				{Type: modelenvelope.ResponsePartTypeText, Text: "before"},
				{
					Type:           modelenvelope.ResponsePartTypeToolCall,
					ProviderCallID: "call_1",
					ToolName:       "lookup_customer",
					ToolInput:      json.RawMessage(`{"email":"ada@example.com"}`),
				},
			},
		},
	}
	_, calls, err := outputParts(envelope, []ToolProposal{{
		ID:             id,
		ProviderCallID: "call_1",
		Type:           toolcatalog.ToolTypeCustom,
	}})
	if err != nil {
		t.Fatalf("bind tool calls: %v", err)
	}
	if len(calls) != 1 ||
		calls[0].ID != id ||
		calls[0].ProviderCallID != "call_1" ||
		calls[0].Name != "lookup_customer" ||
		string(calls[0].Input) != `{"email":"ada@example.com"}` ||
		calls[0].Type != toolcatalog.ToolTypeCustom {
		t.Fatalf("bound tool call = %+v", calls)
	}
}

func TestBindToolCallsRequiresExactProviderCallCoverage(t *testing.T) {
	envelope := modelenvelope.ResponseEnvelope{
		Normalized: modelenvelope.ResponseNormalized{
			Content: []modelenvelope.ResponsePart{{
				Type:           modelenvelope.ResponsePartTypeToolCall,
				ProviderCallID: "call_1",
				ToolName:       "run_command",
				ToolInput:      json.RawMessage(`{"command":"true"}`),
			}},
		},
	}
	for _, bindings := range [][]ToolProposal{
		{{
			ProviderCallID: "call_other",
			Type:           toolcatalog.ToolTypeBuiltIn,
		}},
		{
			{
				ProviderCallID: "call_1",
				Type:           toolcatalog.ToolTypeBuiltIn,
			},
			{
				ProviderCallID: "call_extra",
				Type:           toolcatalog.ToolTypeBuiltIn,
			},
		},
	} {
		if _, _, err := outputParts(envelope, bindings); err == nil {
			t.Fatalf("bindings %+v did not fail", bindings)
		}
	}
}

func TestBindToolCallsRejectsInvalidToolInput(t *testing.T) {
	for _, input := range []json.RawMessage{
		json.RawMessage(`{"command":`),
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`"command"`),
	} {
		envelope := modelenvelope.ResponseEnvelope{
			Normalized: modelenvelope.ResponseNormalized{
				Content: []modelenvelope.ResponsePart{{
					Type:           modelenvelope.ResponsePartTypeToolCall,
					ProviderCallID: "call_1",
					ToolName:       "run_command",
					ToolInput:      input,
				}},
			},
		}
		_, _, err := outputParts(envelope, []ToolProposal{{
			ID:             uuid.New(),
			ProviderCallID: "call_1",
			Type:           toolcatalog.ToolTypeBuiltIn,
		}})
		if err == nil {
			t.Fatalf("outputParts accepted input %s", input)
		}
	}
}

func TestModelUsageForStorageNormalizesProviderUsage(t *testing.T) {
	usage := normalizedUsage(modelenvelope.Usage{
		InputTokens:      20,
		OutputTokens:     7,
		ReasoningTokens:  3,
		CacheReadTokens:  8,
		CacheWriteTokens: 2,
	})
	if usage != (modelenvelope.Usage{
		InputTokens:         20,
		UncachedInputTokens: 10,
		OutputTokens:        7,
		ReasoningTokens:     3,
		CacheReadTokens:     8,
		CacheWriteTokens:    2,
	}) {
		t.Fatalf("normalized usage = %+v", usage)
	}
}

func TestModelUsageForStorageDropsInvalidOrUnrepresentableUsage(t *testing.T) {
	tests := []modelenvelope.Usage{
		{InputTokens: -1},
		{InputTokens: math.MaxInt32 + 1},
		{InputTokens: 10, OutputTokens: 2, ReasoningTokens: 3},
		{InputTokens: 10, CacheReadTokens: 11},
	}
	for _, usage := range tests {
		if got := normalizedUsage(usage); got != (modelenvelope.Usage{}) {
			t.Fatalf("invalid usage %+v normalized to %+v", usage, got)
		}
	}
}
func TestEvidenceIdentityAndCost(t *testing.T) {
	for _, e := range []ModelEvidence{{RequestID: "req"},
		{ResponseID: "response"},
		{Cost: "0.01"},
		{Usage: modelenvelope.Usage{InputTokens: 1}},
		{APIFormat: "openai-responses"},
		{APIFormat: "openai-responses",
			APIVariant: "default",
			Cost:       "invalid"}} {
		_, err := evidenceParams(e)
		require.Error(t, err)
	}
	for _, cost := range []modelenvelope.ProviderReportedCostUSD{"", "0", "0.0000125", "100"} {
		columns, err := evidenceParams(
			ModelEvidence{
				APIFormat:  "openai-responses",
				APIVariant: "default",
				Cost:       cost,
				Usage:      modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 4},
			},
		)
		require.NoError(t, err)
		require.EqualValues(t, cost, valueOrZero(columns.Cost))
		require.EqualValues(t, 10, *columns.InputTokens)
		require.EqualValues(t, 10, *columns.UncachedTokens)
		require.EqualValues(t, 4, *columns.OutputTokens)
		require.Nil(t, columns.CacheReadTokens)
		require.Nil(t, columns.CacheWriteTokens)
		require.Nil(t, columns.ReasoningTokens)
	}
}

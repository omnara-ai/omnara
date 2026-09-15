package executionstore

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestBindToolCallsRejectsAmbiguityBeforeJSONB(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"a":1,"\u0061":2}`,
		`{"outer":[{"a":1,"\u0061":2}]}`,
		`{"omnara_channel":"one","\u006fmnara_channel":"two"}`,
		`{} {}`,
		`{} trailing`,
		"{\"value\":\"\xff\"}",
	} {
		envelope := validToolCallEnvelope(modelenvelope.ResponsePart{
			Type: modelenvelope.ResponsePartTypeToolCall, ProviderCallID: "ambiguous",
			ToolName: "lookup", ToolInput: json.RawMessage(raw),
		})
		calls, err := bindToolCalls(envelope, []ToolCallBindingInput{{
			ProviderCallID: "ambiguous", Type: toolcatalog.ToolTypeCustom,
		}})
		require.ErrorContains(t, err, `provider call id "ambiguous"`)
		require.Nil(t, calls)
		require.Equal(t, raw, string(envelope.Normalized.Content[0].ToolInput))
	}
}

func TestBindToolCallsPreservesOriginalArgumentsAndExistingRejection(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(` {"count":9007199254740993,"decimal":1.2300,"omnara_channel":"literal","nested":{"omnara_channel":true}} `)
	envelope := validToolCallEnvelope(
		modelenvelope.ResponsePart{
			Type: modelenvelope.ResponsePartTypeToolCall, ProviderCallID: "accepted", ToolName: "lookup", ToolInput: raw,
		},
		modelenvelope.ResponsePart{Type: modelenvelope.ResponsePartTypeToolCall, ProviderCallID: "rejected", ToolName: "lookup", ToolInput: json.RawMessage(`{}`), ToolCallError: "existing diagnostic"},
	)
	calls, err := bindToolCalls(envelope, []ToolCallBindingInput{
		{ProviderCallID: "accepted", Type: toolcatalog.ToolTypeCustom},
		{ProviderCallID: "rejected", Type: toolcatalog.ToolTypeCustom},
	})
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.Equal(t, string(raw), string(calls[0].Input))
	require.Empty(t, calls[0].RejectionContent)
	require.JSONEq(t,
		`[{"type":"structured_data","value":{"error":"existing diagnostic","error_code":"malformed"}}]`,
		string(calls[1].RejectionContent),
	)
}

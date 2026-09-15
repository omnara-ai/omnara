package model

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestNewToolCallPartKeepsMalformedCallsRepresentable(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"a":1,"\u0061":2}`,
		`{"nested":[{"a":1,"\u0061":2}]}`,
		`{} {}`,
		`{"incomplete":`,
	} {
		part := NewToolCallPart("call", "lookup", json.RawMessage(raw))
		require.Equal(t, "call", part.ProviderCallID)
		require.Equal(t, "lookup", part.ToolName)
		require.Equal(t, json.RawMessage(`{}`), part.ToolInput)
		require.Equal(t,
			"The tool arguments must be a complete JSON object and were not executed. Retry with valid JSON, splitting large inputs into smaller calls.",
			part.ToolCallError,
		)
		envelope, err := NewResponseEnvelopeForStorage(
			"test-model", modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault,
			Response{StopReason: StopReasonToolUse, Content: []ResponsePart{part}})
		require.NoError(t, err, "strict validation must keep the existing rejected-call diagnostic path usable")
		require.Equal(t, part.ToolCallError, envelope.Normalized.Content[0].ToolCallError)
	}
}

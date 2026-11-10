package anthropicmessages

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func parseAnthropicUsage(t *testing.T, usage string) model.Response {
	t.Helper()
	p := protocol{client: Client{EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test"}}
	resp, err := p.ParseResponse(t.Context(), route.Response{StatusCode: http.StatusOK, Body: []byte(
		`{"id":"msg_1","type":"message","model":"claude-test","content":[{"type":"text","text":"ok"}],` +
			`"stop_reason":"end_turn","usage":` + usage + `}`,
	)})
	require.NoError(t, err)
	return resp
}

func oneHour(value int) *int {
	return &value
}

func TestAnthropicResponseRecordsRateClass(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		want  modelenvelope.AnthropicMetadata
	}{
		{
			name:  "standard values are omitted",
			usage: `{"input_tokens":5,"output_tokens":2,"service_tier":"standard","inference_geo":"global","speed":"standard"}`,
		},
		{
			name: "older models report geo as not available",
			usage: `{"input_tokens":5,"output_tokens":2,` +
				`"service_tier":"standard","inference_geo":"not_available","speed":"standard"}`,
		},
		{
			name:  "absent fields mean standard",
			usage: `{"input_tokens":5,"output_tokens":2}`,
		},
		{
			name:  "premium values are kept",
			usage: `{"input_tokens":5,"output_tokens":2,"service_tier":"priority","inference_geo":"us","speed":"fast"}`,
			want:  modelenvelope.AnthropicMetadata{Speed: "fast", InferenceGeo: "us", ServiceTier: "priority"},
		},
		{
			name: "cache split with writes",
			usage: `{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":30,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":10}}`,
			want: modelenvelope.AnthropicMetadata{CacheWrite1hInputTokens: oneHour(10)},
		},
		{
			name: "explicit zero 1-hour writes",
			usage: `{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":30,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":0}}`,
			want: modelenvelope.AnthropicMetadata{CacheWrite1hInputTokens: oneHour(0)},
		},
		{
			name:  "missing split",
			usage: `{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":30}`,
		},
		{
			name: "split without writes",
			usage: `{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":0,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, parseAnthropicUsage(t, test.usage).ProviderMetadata.Anthropic)
		})
	}
}

func TestAnthropicStreamKeepsRateClassFromMessageStart(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start", `{"message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":15,` +
			`"cache_creation_input_tokens":8582,"cache_read_input_tokens":0,` +
			`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":8582},` +
			`"output_tokens":4,"service_tier":"priority","inference_geo":"us","speed":"fast"}}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"ok"}}`},
		[2]string{"content_block_stop", `{"index":0}`},
		[2]string{"message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":15,` +
			`"cache_creation_input_tokens":8582,"cache_read_input_tokens":0,"output_tokens":4}}`},
		[2]string{"message_stop", `{}`},
	)
	resp, err := consumeAnthropicStream(t, stream, &recordingSink{})
	require.NoError(t, err)
	require.Equal(t, modelenvelope.AnthropicMetadata{
		Speed: "fast", InferenceGeo: "us", ServiceTier: "priority", CacheWrite1hInputTokens: oneHour(8582),
	}, resp.ProviderMetadata.Anthropic)
	require.Equal(t, 8582, resp.Usage.CacheWriteTokens)
	require.False(t, resp.Usage.Incomplete)
}

func TestAnthropicPartialStreamMarksUsageIncomplete(t *testing.T) {
	stream := anthropicSSE(
		[2]string{"message_start",
			`{"message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":15,"output_tokens":1}}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"partial"}}`},
	)
	resp, err := consumeAnthropicStream(t, stream, &recordingSink{})
	require.Error(t, err)
	require.Equal(t, 15, resp.Usage.InputTokens)
	require.True(t, resp.Usage.Incomplete)
}

func TestAnthropicPartialStreamWithoutUsageIsNotMarked(t *testing.T) {
	resp, err := consumeAnthropicStream(t, anthropicSSE(), &recordingSink{})
	require.Error(t, err)
	require.Equal(t, model.Usage{}, resp.Usage)
}

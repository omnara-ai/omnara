package openaichatcompletions

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

func parseChatBody(t *testing.T, fields string) model.Response {
	t.Helper()
	resp, err := (protocol{}).ParseResponse(t.Context(), route.Response{StatusCode: http.StatusOK, Body: []byte(
		`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` + fields + `}`,
	)})
	require.NoError(t, err)
	return resp
}

func TestChatResponseRecordsTierAndXAICost(t *testing.T) {
	resp := parseChatBody(t, `"service_tier":"priority","usage":{"prompt_tokens":187,"completion_tokens":2,`+
		`"total_tokens":189,"cost_in_usd_ticks":1043500}`)
	require.Equal(t, modelenvelope.OpenAIMetadata{ServiceTier: "priority"}, resp.ProviderMetadata.OpenAI)
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("0.00010435"), resp.ProviderReportedCostUSD)

	zero := parseChatBody(t,
		`"service_tier":"default","usage":{"prompt_tokens":1,"completion_tokens":1,"cost_in_usd_ticks":0}`)
	require.Equal(t, modelenvelope.OpenAIMetadata{}, zero.ProviderMetadata.OpenAI)
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("0"), zero.ProviderReportedCostUSD)
}

func TestChatUsageCountsThinkingFromTotalTokens(t *testing.T) {
	tests := []struct {
		name      string
		usage     string
		output    int
		reasoning int
	}{
		{
			name:   "total includes thinking",
			usage:  `{"prompt_tokens":35,"completion_tokens":5,"total_tokens":343}`,
			output: 308,
		},
		{name: "total matches the parts", usage: `{"prompt_tokens":35,"completion_tokens":5,"total_tokens":40}`, output: 5},
		{name: "no total", usage: `{"prompt_tokens":35,"completion_tokens":5}`, output: 5},
		{
			name: "reasoning checked against the corrected output",
			usage: `{"prompt_tokens":35,"completion_tokens":5,"total_tokens":343,` +
				`"completion_tokens_details":{"reasoning_tokens":300}}`,
			output: 308, reasoning: 300,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp := parseChatBody(t, `"usage":`+test.usage)
			require.Equal(t, 35, resp.Usage.InputTokens)
			require.Equal(t, test.output, resp.Usage.OutputTokens)
			require.Equal(t, test.reasoning, resp.Usage.ReasoningTokens)
		})
	}
}

func TestChatStreamReadsTierAndXAICostFromFinalChunk(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"chatcmpl_1","model":"grok-test","service_tier":"flex","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`{"id":"chatcmpl_1","model":"grok-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl_1","model":"grok-test","choices":[],"usage":{"prompt_tokens":187,"completion_tokens":2,`+
			`"total_tokens":189,"cost_in_usd_ticks":1043500}}`,
		`[DONE]`,
	)
	resp, err := consumeChatCompletionsStream(t, stream, &chatRecordingSink{}, modelprotocol.APIVariantDefault)
	require.NoError(t, err)
	require.Equal(t, modelenvelope.OpenAIMetadata{ServiceTier: "flex"}, resp.ProviderMetadata.OpenAI)
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("0.00010435"), resp.ProviderReportedCostUSD)
	require.Equal(t, 2, resp.Usage.OutputTokens)
}

func TestChatLogsInvalidXAICost(t *testing.T) {
	for ticks, logged := range map[string]bool{`null`: false, `-5`: true} {
		var logs bytes.Buffer
		base := logpkg.WithLogger(context.Background(), slog.New(logpkg.NewJSONHandler(&logs, nil)))
		event := logpkg.NewEvent(base, "model.call")
		resp, err := (protocol{}).ParseResponse(logpkg.WithEvent(base, event), route.Response{
			StatusCode: http.StatusOK,
			Body: []byte(`{"id":"chatcmpl_1","model":"grok-test","choices":[{"index":0,` +
				`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":1,"cost_in_usd_ticks":` + ticks + `}}`),
		})
		require.NoError(t, err)
		require.Empty(t, resp.ProviderReportedCostUSD)
		event.Done(context.Background())
		require.Equal(t, logged, strings.Contains(logs.String(), `"invalid_provider_value"`), ticks)
	}
}

package openairesponses

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

func parseResponsesBody(
	t *testing.T,
	fields string,
) (modelenvelope.OpenAIMetadata, modelenvelope.ProviderReportedCostUSD) {
	t.Helper()
	resp, err := (protocol{}).ParseResponse(t.Context(), route.Response{StatusCode: http.StatusOK, Body: []byte(
		`{"id":"resp_1","model":"gpt-test","status":"completed",` +
			`"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"ok"}]}],` + fields + `}`,
	)})
	require.NoError(t, err)
	return resp.ProviderMetadata.OpenAI, resp.ProviderReportedCostUSD
}

func TestResponsesRecordRateClassAndXAICost(t *testing.T) {
	tests := []struct {
		name     string
		fields   string
		metadata modelenvelope.OpenAIMetadata
		cost     modelenvelope.ProviderReportedCostUSD
	}{
		{
			name:     "tier is recorded as reported",
			fields:   `"service_tier":"flex","usage":{"input_tokens":10,"output_tokens":2}`,
			metadata: modelenvelope.OpenAIMetadata{ServiceTier: "flex"},
		},
		{
			name:   "xAI ticks",
			fields: `"usage":{"input_tokens":187,"output_tokens":2,"cost_in_usd_ticks":1043500}`,
			cost:   "0.00010435",
		},
		{
			name:   "xAI zero ticks is a real zero",
			fields: `"usage":{"input_tokens":187,"output_tokens":2,"cost_in_usd_ticks":0}`,
			cost:   "0",
		},
		{
			name:   "non-integer ticks are ignored",
			fields: `"usage":{"input_tokens":187,"output_tokens":2,"cost_in_usd_ticks":1.5}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata, cost := parseResponsesBody(t, test.fields)
			require.Equal(t, test.metadata, metadata)
			require.Equal(t, test.cost, cost)
		})
	}
}

func TestResponsesStreamReadsXAICostFromCompletedEvent(t *testing.T) {
	completed := `{"id":"resp_1","model":"grok-test","status":"completed","service_tier":"default",` +
		`"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"ok"}]}],` +
		`"usage":{"input_tokens":187,"output_tokens":1,"cost_in_usd_ticks":1018500}}`
	stream := openAISSE(
		[2]string{"response.created", `{"response":{"id":"resp_1","model":"grok-test","status":"in_progress"}}`},
		[2]string{"response.completed", `{"response":` + completed + `}`},
	)
	resp, err := consumeOpenAIStream(t, stream, &recordingSink{})
	require.NoError(t, err)
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("0.00010185"), resp.ProviderReportedCostUSD)
}

func TestResponsesLogsInvalidXAICost(t *testing.T) {
	for ticks, logged := range map[string]bool{`null`: false, `"12"`: true} {
		var logs bytes.Buffer
		base := logpkg.WithLogger(context.Background(), slog.New(logpkg.NewJSONHandler(&logs, nil)))
		event := logpkg.NewEvent(base, "model.call")
		resp, err := (protocol{}).ParseResponse(logpkg.WithEvent(base, event), route.Response{
			StatusCode: http.StatusOK,
			Body: []byte(`{"id":"resp_1","model":"grok-test","status":"completed",` +
				`"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"ok"}]}],` +
				`"usage":{"input_tokens":1,"output_tokens":1,"cost_in_usd_ticks":` + ticks + `}}`),
		})
		require.NoError(t, err)
		require.Empty(t, resp.ProviderReportedCostUSD)
		event.Done(context.Background())
		require.Equal(t, logged, strings.Contains(logs.String(), `"invalid_provider_value"`), ticks)
	}
}

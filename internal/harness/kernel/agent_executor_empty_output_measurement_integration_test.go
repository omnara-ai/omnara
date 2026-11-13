//go:build integration

package kernel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorRetainsInputMeasurementAfterEmptyResponsesOutput(t *testing.T) {
	for _, tc := range []struct {
		name         string
		response     string
		stopReason   modelenvelope.StopReason
		outputTokens int
	}{
		{
			name: "empty output", stopReason: modelenvelope.StopReasonEndTurn,
			response: `{"id":"empty-response","status":"completed","model":"empty-output","output":[],` +
				`"usage":{"input_tokens":4200,"output_tokens":0}}`,
		},
		{
			name: "unreplayable reasoning", stopReason: modelenvelope.StopReasonEndTurn, outputTokens: 300,
			response: `{"id":"empty-response","status":"completed","model":"empty-output","output":[` +
				`{"type":"reasoning","id":"empty-reasoning","encrypted_content":"short","summary":[]}],` +
				`"usage":{"input_tokens":4200,"output_tokens":300,"output_tokens_details":{"reasoning_tokens":300}}}`,
		},
		{
			name: "content filter", stopReason: modelenvelope.StopReasonContentFilter,
			response: `{"id":"empty-response","status":"incomplete","incomplete_details":{"reason":"content_filter"},` +
				`"model":"empty-output","output":[],"usage":{"input_tokens":4200,"output_tokens":0}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			f := newKernelFixture(t, ctx)
			agentID, userID := f.createAgentWithModelOptions(t, ctx, "openai/empty-output", f.Now,
				kernelConfiguredModelOptions{ContextWindowTokens: new(20_000), MaxOutputTokens: new(1_000)})
			var providerID string
			require.NoError(t, f.Pool.QueryRow(ctx, `
				SELECT revision.model_provider_config_id
				FROM agents agent
				JOIN agent_configs config ON config.id = agent.current_config_id
				JOIN configured_models model ON model.id = config.configured_model_id
				JOIN configured_model_revisions revision ON revision.id = model.current_revision_id
				WHERE agent.id = $1`, agentID).Scan(&providerID))
			opaque := strings.Repeat("opaque", 20_000)
			responses := []string{
				`{"id":"reasoning-response","status":"completed","model":"empty-output","output":[` +
					`{"type":"reasoning","id":"initial-reasoning","encrypted_content":"` + opaque + `","summary":[]},` +
					`{"type":"message","id":"initial-message","role":"assistant","content":[{"type":"output_text","text":"Ready to continue."}]}],` +
					`"usage":{"input_tokens":4000,"output_tokens":100}}`,
				tc.response,
				`{"id":"continued-response","status":"completed","model":"empty-output","output":[` +
					`{"type":"message","id":"continued-message","role":"assistant","content":[{"type":"output_text","text":"Continuing."}]}],` +
					`"usage":{"input_tokens":4300,"output_tokens":10}}`,
			}
			var requests []json.RawMessage
			client := openairesponses.Client{
				ModelProviderConfigID: providerID, ProviderModelSlug: "empty-output",
				BaseURL: "https://provider.test", EndpointPath: "/responses",
				ModelCapabilities: model.Capabilities{
					ContextWindowTokens: 20_000, MaxOutputTokens: new(1_000), SupportsReasoning: true,
				},
				HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					requests = append(requests, append(json.RawMessage(nil), body...))
					response := responses[min(len(requests)-1, len(responses)-1)]
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
						Body: io.NopCloser(strings.NewReader(response)),
					}, nil
				})},
			}
			now := f.Now.Add(time.Second)
			executor := AgentExecutor{Store: f.Store, ModelResolver: liveTestModelResolver(f.Store, client),
				Now: func() time.Time { return now }}
			for turn := range 3 {
				now = now.Add(time.Second)
				work := f.admitContentInputTurn(t, ctx, agentID, userID, fmt.Sprintf("Continue attempt %d.", turn), now)
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				f.releaseModelRuntimeLock(t, ctx, work)
				require.Len(t, requests, turn+1, "each turn should make one normal request without compaction")
				if turn > 0 {
					require.True(t, strings.Contains(string(requests[turn]), opaque), "historical encrypted replay must remain intact")
				}
				if turn == 1 {
					var inputTokens, outputTokens, contentBlocks int
					var stopReason string
					var hasFingerprint bool
					require.NoError(t, f.Pool.QueryRow(ctx, `
						SELECT context.input_tokens_total, coalesce(context.output_tokens_total, 0),
						       context.input_fingerprint IS NOT NULL, output.stop_reason,
						       (SELECT count(*) FROM content_blocks block WHERE block.owner_model_output_id = output.id)
						FROM model_call_contexts context
						JOIN model_outputs output ON output.model_call_context_id = context.id
						WHERE context.agent_id = $1 AND context.turn_id = $2
						  AND context.operation_kind = 'normal' AND context.state = 'succeeded'`, agentID, work.TurnID).
						Scan(&inputTokens, &outputTokens, &hasFingerprint, &stopReason, &contentBlocks))
					require.Equal(t, 4_200, inputTokens)
					require.Equal(t, tc.outputTokens, outputTokens)
					require.Equal(t, string(tc.stopReason), stopReason)
					require.True(t, hasFingerprint)
					require.Zero(t, contentBlocks, "the valid measured output has no canonical content")
				}
			}
			var compactions, successes int
			require.NoError(t, f.Pool.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE operation_kind = 'compaction'),
				       count(*) FILTER (WHERE operation_kind = 'normal' AND state = 'succeeded')
				FROM model_call_contexts WHERE agent_id = $1`, agentID).Scan(&compactions, &successes))
			require.Zero(t, compactions)
			require.Equal(t, 3, successes)
			require.Zero(t, pendingModelWork(t, ctx, f, agentID))
		})
	}
}

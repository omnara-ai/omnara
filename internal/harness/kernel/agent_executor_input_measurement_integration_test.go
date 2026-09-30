//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorReusesPersistedMeasuredInputForActualAdapterRequest(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/measured-input", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10000), MaxOutputTokens: new(9000)})
	var requests []json.RawMessage
	client := openaichatcompletions.Client{
		InputIdentityScope: "test-resolved-credential-and-route", ProviderModelSlug: "measured-input",
		BaseURL: "https://provider.test", EndpointPath: "/chat/completions",
		ModelCapabilities: model.Capabilities{ContextWindowTokens: 10000, DefaultMaxOutputTokens: 9000},
		HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			requests = append(requests, append(json.RawMessage(nil), body...))
			response := fmt.Sprintf(`{"id":"response_%d","object":"chat.completion","model":"measured-input",
				"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1000,"completion_tokens":4000,
				"completion_tokens_details":{"reasoning_tokens":3990}}}`, len(requests))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": {"application/json"}, "X-Request-Id": {fmt.Sprintf("request_%d", len(requests))},
				},
				Body: io.NopCloser(strings.NewReader(response)),
			}, nil
		})},
	}
	now := fixture.Now.Add(2 * time.Second)
	executor := AgentExecutor{
		Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		Now: func() time.Time { return now },
	}
	largeInput := strings.Repeat("closed historical detail ", 2400)
	first := fixture.admitContentInputTurn(t, ctx, agentID, userID, largeInput, fixture.Now.Add(time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, first))
	require.Len(t, requests, 1)
	require.Contains(t, string(requests[0]), largeInput, "uncertain first input must reach the provider intact")
	var firstID string
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
		SELECT id FROM model_call_contexts
		WHERE agent_id=$1 AND state='succeeded' AND operation_kind='normal'
	`, agentID).Scan(&firstID))
	var identityFingerprint string
	var measuredTokens int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
		SELECT request_input_fingerprint,input_tokens_total FROM model_call_contexts WHERE id=$1
	`, firstID).Scan(&identityFingerprint, &measuredTokens))
	require.Len(t, identityFingerprint, 64)
	require.Equal(t, 1000, measuredTokens)
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, kernelTestProjectID, agentID, first.RuntimeLockID,
	))

	second := fixture.admitContentInputTurn(t, ctx, agentID, userID, "continue", fixture.Now.Add(3*time.Second))
	now = fixture.Now.Add(4 * time.Second)
	executor = AgentExecutor{
		Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		Now: func() time.Time { return now },
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, second))
	require.Len(t, requests, 2, "measured request should go directly to normal generation")
	var compactions, successfulNormal int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE operation_kind='compaction'),
			count(*) FILTER (WHERE operation_kind='normal' AND state='succeeded' AND request_input_fingerprint IS NOT NULL)
		FROM model_call_contexts WHERE agent_id=$1
	`, agentID).Scan(&compactions, &successfulNormal))
	require.Zero(t, compactions, "large local estimate and billed hidden output must not override valid measured input")
	require.Equal(t, 2, successfulNormal)
	require.Contains(t, string(requests[1]), largeInput)
	require.Contains(t, string(requests[1]), "continue")
}

func TestAgentExecutorPersistsPreparedIdentityForToolCallResponse(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%t", malformed), func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgent(t, ctx, "openai/measured-tool", fixture.Now, "read_file")
			client := openaichatcompletions.Client{InputIdentityScope: "test-route", ProviderModelSlug: "measured-tool",
				BaseURL: "https://provider.test", EndpointPath: "/chat/completions",
				ModelCapabilities: model.Capabilities{ContextWindowTokens: 100000, DefaultMaxOutputTokens: 1024},
				HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(*http.Request) (*http.Response, error) {
					response := `{"id":"response_tool","object":"chat.completion","model":"measured-tool",
				"choices":[{"index":0,"message":{"role":"assistant",
				"tool_calls":[{"id":"read_call","type":"function",
				"function":{"name":"read_file","arguments":"{\"path\":\"/tmp/example\"}"}}]},
				"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":1200,"completion_tokens":30}}`
					if malformed {
						response = strings.Replace(response, `{\"path\":\"/tmp/example\"}`, `incomplete arguments`, 1)
					}
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
						Body: io.NopCloser(strings.NewReader(response)),
					}, nil
				})},
			}
			turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "read the file", fixture.Now.Add(time.Second))
			executor := AgentExecutor{
				Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
				Now: func() time.Time { return fixture.Now.Add(2 * time.Second) },
			}
			require.NoError(t, executor.ExecuteModelWork(ctx, turn))
			var fingerprint *string
			var tokens int
			var state, reason string
			require.NoError(t, fixture.Pool.QueryRow(ctx, `
		SELECT context.request_input_fingerprint,context.input_tokens_total,context.state,output.stop_reason
		FROM model_call_contexts context JOIN model_outputs output ON output.model_call_context_id=context.id
		WHERE context.agent_id=$1
	`, agentID).Scan(&fingerprint, &tokens, &state, &reason))
			if malformed {
				require.Nil(t, fingerprint, "sanitized output cannot anchor complete measured reasoning replay")
			} else {
				require.NotNil(t, fingerprint)
				require.Len(t, *fingerprint, 64)
			}
			require.Equal(t, 1200, tokens)
			require.Equal(t, string(executionstore.ModelCallContextSucceeded), state)
			require.Equal(t, string(model.StopReasonToolUse), reason)
		})
	}
}

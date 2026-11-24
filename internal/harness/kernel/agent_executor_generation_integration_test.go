//go:build integration

package kernel

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type canceledStreamBody struct {
	canceled <-chan struct{}
}

func (b canceledStreamBody) Read([]byte) (int, error) {
	<-b.canceled
	return 0, context.Canceled
}

func (b canceledStreamBody) Close() error {
	return nil
}

func TestCanceledModelCallKeepsItsGeneration(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/generation", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10000), MaxOutputTokens: new(9000)})
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "complete the task", fixture.Now)
	client := openaichatcompletions.Client{
		ProviderModelSlug: "generation", APIVariant: modelprotocol.APIVariantOpenRouter,
		BaseURL: "https://provider.test", EndpointPath: "/chat/completions",
		ModelCapabilities: model.Capabilities{ContextWindowTokens: 10000, DefaultMaxOutputTokens: 9000},
		HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Generation-Id": {"gen-kernel"}},
				Body:       canceledStreamBody{canceled: req.Context().Done()},
			}, nil
		})},
	}
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, client),
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	done := make(chan error, 1)
	go func() { done <- executor.ExecuteModelWork(workCtx, work) }()
	require.Eventually(t, func() bool {
		var generationID string
		err := fixture.Pool.QueryRow(ctx, `
SELECT coalesce(inflight_generation_id, '') FROM agent_runtime_locks WHERE agent_id = $1`,
			agentID).Scan(&generationID)
		return err == nil && generationID == "gen-kernel"
	}, 10*time.Second, 10*time.Millisecond, "the stream saves the header generation ID while the call runs")
	_, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: kernelTestProjectID, AgentID: agentID, Actor: kernelTestOmnaraActorParams(t, userID),
	})
	require.NoError(t, err)
	cancelWork()
	require.ErrorIs(t, <-done, context.Canceled)

	var state, format, variant, responseID string
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
SELECT state, api_format, api_variant, provider_response_id FROM model_call_contexts WHERE agent_id = $1`,
		agentID).Scan(&state, &format, &variant, &responseID))
	require.Equal(t, []string{"canceled", "openai-chat-completions", "openrouter", "gen-kernel"},
		[]string{state, format, variant, responseID})
}

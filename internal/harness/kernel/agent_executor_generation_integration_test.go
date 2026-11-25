//go:build integration

package kernel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

func startGenerationModelWork(
	t *testing.T, ctx context.Context, body func(*http.Request) io.ReadCloser,
) (kernelFixture, uuid.UUID, <-chan error, context.CancelFunc) {
	t.Helper()
	fixture := newKernelFixture(t, ctx)
	agentID, _ := fixture.createAgentWithModelOptions(t, ctx, "openai/generation", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(10000), MaxOutputTokens: new(9000)})
	work := fixture.admitContentInputTurn(t, ctx, agentID, kernelTestUserID, "complete the task", fixture.Now)
	client := openaichatcompletions.Client{
		ProviderModelSlug: "generation", APIVariant: modelprotocol.APIVariantOpenRouter,
		BaseURL: "https://provider.test", EndpointPath: "/chat/completions",
		ModelCapabilities: model.Capabilities{ContextWindowTokens: 10000, DefaultMaxOutputTokens: 9000},
		HTTPClient: &http.Client{Transport: kernelSlackRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Generation-Id": {"gen-kernel"}},
				Body:       body(req),
			}, nil
		})},
	}
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, client),
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	t.Cleanup(cancelWork)
	done := make(chan error, 1)
	go func() { done <- executor.ExecuteModelWork(workCtx, work) }()
	return fixture, agentID, done, cancelWork
}

func requireModelCallGeneration(
	t *testing.T, ctx context.Context, fixture kernelFixture, agentID uuid.UUID, state string,
) {
	t.Helper()
	var gotState, format, variant, responseID string
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
SELECT state, api_format, api_variant, provider_response_id FROM model_call_contexts WHERE agent_id = $1`,
		agentID).Scan(&gotState, &format, &variant, &responseID))
	require.Equal(t, []string{state, "openai-chat-completions", "openrouter", "gen-kernel"},
		[]string{gotState, format, variant, responseID})
}

func TestCanceledModelCallKeepsItsGeneration(t *testing.T) {
	ctx := context.Background()
	fixture, agentID, done, cancelWork := startGenerationModelWork(t, ctx,
		func(req *http.Request) io.ReadCloser { return canceledStreamBody{canceled: req.Context().Done()} })
	require.Eventually(t, func() bool {
		var generationID string
		err := fixture.Pool.QueryRow(ctx, `
SELECT coalesce(inflight_generation_id, '') FROM agent_runtime_locks WHERE agent_id = $1`,
			agentID).Scan(&generationID)
		return err == nil && generationID == "gen-kernel"
	}, 10*time.Second, 10*time.Millisecond, "the stream saves the header generation ID while the call runs")
	_, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: kernelTestProjectID, AgentID: agentID, Actor: kernelTestOmnaraActorParams(t, kernelTestUserID),
	})
	require.NoError(t, err)
	cancelWork()
	require.ErrorIs(t, <-done, context.Canceled)
	requireModelCallGeneration(t, ctx, fixture, agentID, "canceled")
}

func TestFailedModelStreamKeepsHeaderGeneration(t *testing.T) {
	ctx := context.Background()
	stream := "data: " + `{"id":"cmpl-kernel","error":{"code":400,"message":"Provider rejected the request"},` +
		`"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}` + "\n\n"
	fixture, agentID, done, _ := startGenerationModelWork(t, ctx,
		func(*http.Request) io.ReadCloser { return io.NopCloser(strings.NewReader(stream)) })
	<-done
	requireModelCallGeneration(t, ctx, fixture, agentID, "failed")
}

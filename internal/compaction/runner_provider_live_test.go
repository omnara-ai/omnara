//go:build integration && live

package compaction

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	modelanthropicmessages "github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	modelopenaichatcompletions "github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	modelopenairesponses "github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/stretchr/testify/require"
)

func TestRunnerLiveOpenAIResponsesCompactionCreatesCheckpoint(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI compaction test")
	}
	runLiveCompactionProvider(t, modelopenairesponses.Client{
		Auth:              route.BearerToken{Token: apiKey},
		BaseURL:           os.Getenv("OPENAI_BASE_URL"),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIResponses),
		ProviderModelSlug: modeltest.LiveOpenAIProviderModelSlug,
		ModelCapabilities: liveCompactionCapabilities(),
		APIVariantOptions: json.RawMessage(`{"reasoning":{"effort":"none"}}`),
	})
}

func TestRunnerLiveOpenAIChatCompletionsCompactionCreatesCheckpoint(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI Chat Completions compaction test")
	}
	runLiveCompactionProvider(t, modelopenaichatcompletions.Client{
		Auth:              route.BearerToken{Token: apiKey},
		BaseURL:           os.Getenv("OPENAI_BASE_URL"),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
		ProviderModelSlug: modeltest.LiveOpenAIProviderModelSlug,
		ModelCapabilities: liveCompactionCapabilities(),
		APIVariantOptions: json.RawMessage(`{"reasoning_effort":"none"}`),
	})
}

func TestRunnerLiveOpenRouterCompactionCreatesCheckpoint(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for live OpenRouter compaction test")
	}
	runLiveCompactionProvider(t, modelopenaichatcompletions.Client{
		Auth: route.Chain{
			route.BearerToken{Token: apiKey},
			route.Headers{
				"HTTP-Referer":       "https://omnara.com",
				"X-OpenRouter-Title": "Omnara Live Test",
			},
		},
		BaseURL:           liveOpenRouterCompactionBaseURL(),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
		ProviderModelSlug: modeltest.LiveOpenRouterProviderModelSlug,
		ModelCapabilities: liveCompactionCapabilities(),
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(`{"reasoning":{"enabled":false}}`),
	})
}

func TestRunnerLiveAnthropicCompactionCreatesCheckpoint(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Fatal("ANTHROPIC_API_KEY is required for live Anthropic compaction test")
	}
	runLiveCompactionProvider(t, modelanthropicmessages.Client{
		Auth:              route.HeaderAuth{Header: "x-api-key", Value: apiKey},
		BaseURL:           os.Getenv("ANTHROPIC_BASE_URL"),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatAnthropicMessages),
		ProviderModelSlug: modeltest.LiveAnthropicProviderModelSlug,
		ModelCapabilities: liveCompactionCapabilities(),
	})
}

func runLiveCompactionProvider(t *testing.T, client model.Client) {
	t.Helper()
	for _, tc := range []struct {
		name       string
		allowance  int
		stopReason model.StopReason
	}{
		{"completed_summary", 4096, model.StopReasonEndTurn},
		{"partial_summary", 32, model.StopReasonMaxTokens},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := &liveCompactionClient{Client: client, allowance: tc.allowance, responses: new([]model.Response)}
			runLiveCompactionSummary(t, observed, tc.stopReason)
		})
	}
}

func runLiveCompactionSummary(t *testing.T, client *liveCompactionClient, stopReason model.StopReason) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	repeatedAuditContext := strings.Repeat(
		"Audit record: durable model contexts must preserve their event frontier, provider send evidence, recovery policy, and checkpoint lineage. ",
		40,
	)
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		textCompactionEvent(
			1,
			"Implemented durable retry evidence, cumulative checkpoints, and model-ready steering admission. "+
				"The next step is provider verification. "+repeatedAuditContext,
		),
	}}
	apiFormat, apiVariant, ok := model.APIIdentityForClient(client)
	require.True(t, ok)
	result, err := testRunner(store, client).Run(ctx, runInput(testPlan(1, 1, 2)))
	require.NoError(t, err)
	require.Len(t, *client.responses, 1)
	response := (*client.responses)[0]
	require.Equal(t, stopReason, response.StopReason)
	require.Empty(t, response.ToolCalls())
	require.NotEmpty(t, strings.TrimSpace(response.Text()))
	require.Empty(t, store.retryFailures)
	require.Empty(t, store.terminalFailures)
	require.Empty(t, store.replacements)
	require.Equal(t, RunCompleted, result.State)
	require.NotNil(t, result.Checkpoint)
	require.Equal(t, strings.TrimSpace(response.Text()), result.Checkpoint.Summary)
	require.Len(t, store.claims, 1)
	require.Equal(t, testIDN(601), store.claims[0].Context.ConfiguredModelRevisionID)
	require.Len(t, store.publishInputs, 1)
	publication := store.publishInputs[0]
	require.Equal(t, apiFormat, publication.APIFormat)
	require.Equal(t, apiVariant, publication.APIVariant)
	require.Equal(t, response.Usage, publication.Usage)
	require.Positive(t, publication.Usage.InputTokens)
	require.Positive(t, publication.Usage.OutputTokens)
	require.Equal(t, modelenvelope.NormalizeUsage(publication.Usage), publication.Usage)
	if apiVariant == modelprotocol.APIVariantOpenRouter {
		_, valid := modelenvelope.ParseProviderReportedCostUSD(string(publication.ProviderReportedCostUSD))
		require.True(t, valid)
	}
}

type liveCompactionClient struct {
	model.Client
	allowance int
	responses *[]model.Response
}

func (c *liveCompactionClient) Capabilities() model.Capabilities {
	caps := c.Client.Capabilities()
	caps.DefaultMaxOutputTokens = c.allowance
	return caps
}

func (c *liveCompactionClient) OutputTokenLimits() (model.OutputTokenLimits, error) {
	return model.OutputTokenLimitsForClient(c.Client, "live-compaction")
}

func (c *liveCompactionClient) WithoutManualThinking() (model.Client, error) {
	if provider, ok := c.Client.(interface{ WithoutManualThinking() (model.Client, error) }); ok {
		client, err := provider.WithoutManualThinking()
		if err != nil {
			return nil, err
		}
		summaryClient := *c
		summaryClient.Client = client
		return &summaryClient, nil
	}
	return c, nil
}

func (c *liveCompactionClient) Respond(ctx context.Context, input model.Request) (model.Response, error) {
	response, err := c.Client.Respond(ctx, input)
	*c.responses = append(*c.responses, response)
	return response, err
}

func liveCompactionCapabilities() model.Capabilities {
	return model.Capabilities{
		ContextWindowTokens:    200_000,
		MaxOutputTokens:        new(8192),
		DefaultMaxOutputTokens: 4096,
	}
}

func liveOpenRouterCompactionBaseURL() string {
	if baseURL := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")); baseURL != "" {
		return baseURL
	}
	return "https://openrouter.ai/api/v1"
}

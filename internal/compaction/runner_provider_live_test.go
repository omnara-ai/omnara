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
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

func TestRunnerLiveOpenAICompactionCreatesCheckpoint(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI compaction test")
	}
	runLiveCompactionProvider(t, modelopenairesponses.Client{
		Auth:              route.BearerToken{Token: apiKey},
		BaseURL:           os.Getenv("OPENAI_BASE_URL"),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIResponses),
		ProviderModelSlug: liveOpenAICompactionProviderModelSlug(),
		ModelCapabilities: liveCompactionCapabilities(),
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
		ProviderModelSlug: liveOpenAIChatCompactionProviderModelSlug(),
		ModelCapabilities: liveCompactionCapabilities(),
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
		ProviderModelSlug: "z-ai/glm-5.2",
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
		ProviderModelSlug: liveAnthropicCompactionProviderModelSlug(),
		ModelCapabilities: liveCompactionCapabilities(),
	})
}

func runLiveCompactionProvider(t *testing.T, client model.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	contentParts := func(text string) json.RawMessage {
		content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
		if err != nil {
			t.Fatalf("marshal live compaction content: %v", err)
		}
		return content
	}
	repeatedAuditContext := strings.Repeat(
		"Audit record: durable model contexts must preserve their event frontier, provider send evidence, recovery policy, and checkpoint lineage. ",
		40,
	)
	store := &fakeStore{events: []executionstore.CompactionSourceEventRecord{
		mustCompactionEvent(
			1,
			"agent_input",
			"content",
			contentParts(
				"Refactor durable model calls while preserving steering boundaries and immutable event history. "+
					repeatedAuditContext,
			),
		),
		mustCompactionEvent(
			2,
			"model_output",
			"output",
			contentParts(
				"Implemented durable retry evidence, cumulative checkpoints, and model-ready steering admission. "+
					"The next step is provider verification. "+repeatedAuditContext,
			),
		),
	}}
	apiFormat, apiVariant, ok := model.APIIdentityForClient(client)
	if !ok {
		t.Fatalf("live compaction client has incomplete API identity")
	}
	plan := Plan{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		InputEventSequence: 3,
		EventSequenceStart: 1,
		EventSequenceEnd:   2,
	}
	result, err := (Runner{
		Store:          store,
		Resolver:       compactionResolver(client),
		ContextBuilder: &fakeContextBuilder{},
		Now:            func() time.Time { return time.Now().UTC() },
	}).Run(
		ctx,
		RunInput{
			Plan:                     plan,
			TurnID:                   testTurnID,
			OpeningInputIDs:          []storage.ID{testOpeningInputID},
			OpeningEventSequence:     3,
			RuntimeLockID:            testRuntimeLockID,
			ParentModelCallContextID: testIDN(777),
		},
	)
	if err != nil {
		t.Fatalf("run live %s compaction: %v", apiFormat, err)
	}
	if result.Checkpoint == nil || strings.TrimSpace(result.Checkpoint.Summary) == "" {
		t.Fatalf(
			"live %s compaction returned empty summary: result=%+v retry_failures=%+v terminal_failures=%+v",
			apiFormat,
			result,
			store.retryFailures,
			store.terminalFailures,
		)
	}
	if len(store.claims) != 1 || store.claims[0].Context.ConfiguredModelRevisionID != testIDN(601) {
		t.Fatalf(
			"model context configured model revision = %+v, want revision %s",
			store.claims,
			testIDN(601),
		)
	}
	if len(store.publishInputs) != 1 {
		t.Fatalf("live compaction publications = %+v, want one", store.publishInputs)
	}
	publication := store.publishInputs[0]
	if publication.APIFormat != apiFormat || publication.APIVariant != apiVariant {
		t.Fatalf(
			"live compaction did not record its provider route: publications=%+v",
			store.publishInputs,
		)
	}
	if publication.Usage.InputTokens <= 0 || publication.Usage.OutputTokens <= 0 ||
		modelenvelope.NormalizeUsage(publication.Usage) != publication.Usage {
		t.Fatalf("live compaction did not record valid provider usage: %+v", publication.Usage)
	}
	if apiVariant == modelprotocol.APIVariantOpenRouter {
		if _, valid := modelenvelope.ParseProviderReportedCostUSD(
			string(publication.ProviderReportedCostUSD),
		); !valid {
			t.Fatalf(
				"live OpenRouter compaction did not record valid provider cost: %q",
				publication.ProviderReportedCostUSD,
			)
		}
	}
}

func liveCompactionCapabilities() model.Capabilities {
	return model.Capabilities{
		ContextWindowTokens:    200_000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: 4096,
	}
}

func liveOpenAICompactionProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "gpt-5-mini"
}

func liveOpenAIChatCompactionProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_CHAT_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "gpt-4.1-mini"
}

func liveOpenRouterCompactionBaseURL() string {
	if baseURL := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")); baseURL != "" {
		return baseURL
	}
	return "https://openrouter.ai/api/v1"
}

func liveAnthropicCompactionProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_ANTHROPIC_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "claude-sonnet-4-6"
}

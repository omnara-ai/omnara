//go:build live

package model_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
)

type livePromptCacheRoute struct {
	name        string
	keyEnv      string
	client      func(apiKey string) model.Client
	writesCache bool
}

func livePromptCacheRoutes() []livePromptCacheRoute {
	openRouterAuth := func(apiKey string) route.Auth {
		return route.Chain{
			route.BearerToken{Token: apiKey},
			route.Headers{"HTTP-Referer": "https://omnara.com", "X-OpenRouter-Title": "Omnara Live Test"},
		}
	}
	openRouterBaseURL := os.Getenv("OPENROUTER_BASE_URL")
	if openRouterBaseURL == "" {
		openRouterBaseURL = "https://openrouter.ai/api/v1"
	}
	return []livePromptCacheRoute{
		{
			name: "anthropic", keyEnv: "ANTHROPIC_API_KEY", writesCache: true,
			client: func(apiKey string) model.Client {
				return anthropicmessages.Client{
					Auth:              route.HeaderAuth{Header: "x-api-key", Value: apiKey},
					BaseURL:           os.Getenv("ANTHROPIC_BASE_URL"),
					EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatAnthropicMessages),
					ProviderModelSlug: modeltest.LiveAnthropicProviderModelSlug,
				}
			},
		},
		{
			name: "openrouter claude", keyEnv: "OPENROUTER_API_KEY", writesCache: true,
			client: func(apiKey string) model.Client {
				return openaichatcompletions.Client{
					Auth:              openRouterAuth(apiKey),
					BaseURL:           openRouterBaseURL,
					EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
					ProviderModelSlug: "anthropic/" + modeltest.LiveAnthropicProviderModelSlug,
					APIVariant:        modelprotocol.APIVariantOpenRouter,
				}
			},
		},
		{
			name: "openrouter automatic", keyEnv: "OPENROUTER_API_KEY",
			client: func(apiKey string) model.Client {
				return openaichatcompletions.Client{
					Auth:              openRouterAuth(apiKey),
					BaseURL:           openRouterBaseURL,
					EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
					ProviderModelSlug: modeltest.LiveOpenRouterProviderModelSlug,
					APIVariant:        modelprotocol.APIVariantOpenRouter,
					APIVariantOptions: json.RawMessage(`{"reasoning":{"enabled":false}}`),
				}
			},
		},
		{
			name: "openai responses", keyEnv: "OPENAI_API_KEY",
			client: func(apiKey string) model.Client {
				return openairesponses.Client{
					Auth:              route.BearerToken{Token: apiKey},
					BaseURL:           os.Getenv("OPENAI_BASE_URL"),
					EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIResponses),
					ProviderModelSlug: modeltest.LiveOpenAIProviderModelSlug,
				}
			},
		},
		{
			name: "openai chat", keyEnv: "OPENAI_API_KEY",
			client: func(apiKey string) model.Client {
				return openaichatcompletions.Client{
					Auth:              route.BearerToken{Token: apiKey},
					BaseURL:           os.Getenv("OPENAI_BASE_URL"),
					EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
					ProviderModelSlug: modeltest.LiveOpenAIProviderModelSlug,
				}
			},
		},
	}
}

func TestLivePromptCacheSecondTurnReadsTheFirstTurnsPrefix(t *testing.T) {
	for _, r := range livePromptCacheRoutes() {
		t.Run(r.name, func(t *testing.T) {
			apiKey := strings.TrimSpace(os.Getenv(r.keyEnv))
			if apiKey == "" {
				t.Skipf("%s is not set", r.keyEnv)
			}
			client := r.client(apiKey)
			var first, second model.Response
			for attempt := 0; attempt < 3; attempt++ {
				first, second = liveConversationPair(t, client)
				if second.Usage.CacheReadTokens > 0 {
					break
				}
				time.Sleep(3 * time.Second)
			}
			if r.writesCache && first.Usage.CacheWriteTokens == 0 {
				t.Fatalf("first turn wrote nothing to the cache: %+v", first.Usage)
			}
			if second.Usage.CacheReadTokens == 0 {
				t.Fatalf("second turn read nothing from the cache: %+v", second.Usage)
			}
			if r.writesCache && second.Usage.CacheReadTokens < first.Usage.CacheWriteTokens {
				t.Fatalf("second turn reused less than the first turn's prefix: first=%+v second=%+v", first.Usage, second.Usage)
			}
			if client.ModelAPIVariant() == modelprotocol.APIVariantOpenRouter &&
				second.ProviderMetadata.OpenRouter.Provider == "" {
				t.Fatalf("openrouter response did not report the serving provider: %+v", second.ProviderMetadata)
			}
			if client.APIFormat() == modelprotocol.APIFormatAnthropicMessages &&
				first.ProviderMetadata.Anthropic.CacheCreation.Ephemeral5mInputTokens == 0 {
				t.Fatalf("anthropic response did not report the cache creation breakdown: %+v", first.ProviderMetadata)
			}
			t.Logf("first=%+v second=%+v", first.Usage, second.Usage)
		})
	}
}

func liveConversationPair(t *testing.T, client model.Client) (model.Response, model.Response) {
	t.Helper()
	bundle := modelcontext.Bundle{
		AgentID:      uuid.New(),
		SystemPrompt: liveStableSystemPrompt(),
		Messages: []modelcontext.Message{
			liveTextMessage(modelprotocol.RoleUser, 10, "Reply with the single word ready."),
		},
	}
	first := liveRespond(t, client, bundle)
	bundle.Messages = append(bundle.Messages,
		liveTextMessage(modelprotocol.RoleAssistant, 20, first.Text()),
		liveTextMessage(modelprotocol.RoleUser, 30, "Reply with the single word again."),
	)
	return first, liveRespond(t, client, bundle)
}

func liveRespond(t *testing.T, client model.Client, bundle modelcontext.Bundle) model.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	prepared, err := client.Prepare(ctx, model.PrepareInput{
		Context: bundle,
		Policy:  model.RequestPolicy{MaxOutputTokens: 16},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	response, err := client.Respond(ctx, model.Request{ProviderRequest: prepared.Body})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	return response
}

func liveStableSystemPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s. You are a terse assistant that answers with exactly one word.\n", uuid.NewString())
	for index := 0; index < 160; index++ {
		fmt.Fprintf(&b, "Rule %d: keep every answer to a single word and never explain your reasoning.\n", index)
	}
	return b.String()
}

func liveTextMessage(role modelprotocol.MessageRole, sequence int64, text string) modelcontext.Message {
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	return modelcontext.Message{Role: role, Sequence: sequence, Content: content}
}

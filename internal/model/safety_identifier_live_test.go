//go:build live

package model_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
)

// The existing live journeys cover OpenRouter Chat Completions. Exercise its
// Responses and Messages endpoints too, which use different attribution fields.
func TestLiveOpenRouterSafetyIdentifiers(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for live OpenRouter safety identifier tests")
	}
	baseURL := os.Getenv("OPENROUTER_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	auth := route.Chain{
		route.BearerToken{Token: apiKey},
		route.Headers{"HTTP-Referer": "https://omnara.com", "X-OpenRouter-Title": "Omnara Live Test"},
	}
	for _, tc := range []struct {
		name   string
		client model.Client
	}{
		{"responses", openairesponses.Client{
			Auth: auth, BaseURL: baseURL,
			EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIResponses),
			ProviderModelSlug: "openai/" + modeltest.LiveOpenAIProviderModelSlug,
		}},
		{"messages", anthropicmessages.Client{
			Auth: auth, BaseURL: baseURL,
			EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatAnthropicMessages),
			ProviderModelSlug: "anthropic/" + modeltest.LiveAnthropicProviderModelSlug,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			prepared, err := tc.client.Prepare(ctx, model.PrepareInput{
				Context: modelcontext.Bundle{
					SafetyIdentifier: modeltest.LiveSafetyIdentifier,
					SystemPrompt:     "Follow the user's response format.",
					Messages: []modelcontext.Message{
						liveTextMessage(modelprotocol.RoleUser, 1, "Reply with the single word ready."),
					},
				},
				Policy: model.RequestPolicy{MaxOutputTokens: 1024},
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			modeltest.RequireSafetyIdentifier(t, prepared.Body)
			response, err := tc.client.Respond(ctx, model.Request{ProviderRequest: prepared.Body})
			if err != nil {
				t.Fatalf("respond with safety identifier: %v", err)
			}
			if response.ID == "" || strings.TrimSpace(response.Text()) == "" {
				t.Fatalf("expected a model response, got id=%q text=%q", response.ID, response.Text())
			}
		})
	}
}

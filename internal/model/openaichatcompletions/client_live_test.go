//go:build live

package openaichatcompletions

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

func TestLiveOpenAIChatCompletionsText(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI Chat Completions test")
	}
	runLiveChatCompletionsText(t, Client{
		Auth:              route.BearerToken{Token: apiKey},
		BaseURL:           os.Getenv("OPENAI_BASE_URL"),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
		ProviderModelSlug: liveOpenAIChatProviderModelSlug(),
	}, model.RequestPolicy{MaxOutputTokens: 64, CacheRetention: model.CacheRetentionLong})
}

func TestLiveOpenRouterChatCompletionsText(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for live OpenRouter Chat Completions test")
	}

	for _, tc := range []struct {
		name              string
		providerModelSlug string
		apiVariantOptions json.RawMessage
	}{
		{
			name:              "glm",
			providerModelSlug: liveOpenRouterProviderModelSlug(),
			apiVariantOptions: json.RawMessage(`{"reasoning":{"enabled":false}}`),
		},
		{
			name:              "openai",
			providerModelSlug: liveOpenRouterOpenAIProviderModelSlug(),
			apiVariantOptions: json.RawMessage(`{}`),
		},
		{
			name:              "anthropic",
			providerModelSlug: liveOpenRouterAnthropicProviderModelSlug(),
			apiVariantOptions: json.RawMessage(`{}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runLiveChatCompletionsText(t, Client{
				Auth:              liveOpenRouterAuth(apiKey),
				BaseURL:           liveOpenRouterBaseURL(),
				EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
				ProviderModelSlug: tc.providerModelSlug,
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				APIVariantOptions: tc.apiVariantOptions,
			}, model.RequestPolicy{MaxOutputTokens: 64})
		})
	}
}

func liveOpenRouterAuth(apiKey string) route.Chain {
	return route.Chain{
		route.BearerToken{Token: apiKey},
		route.Headers{
			"HTTP-Referer":       "https://omnara.com",
			"X-OpenRouter-Title": "Omnara Live Test",
		},
	}
}

func TestLiveOpenRouterChatCompletionsSettings(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for live OpenRouter Chat Completions settings test")
	}
	runLiveChatCompletionsText(t, Client{
		Auth:              liveOpenRouterAuth(apiKey),
		BaseURL:           liveOpenRouterBaseURL(),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
		ProviderModelSlug: liveOpenRouterSettingsProviderModelSlug(),
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(
			`{"provider":{"sort":"price"},"temperature":0,"reasoning":{"enabled":false}}`,
		),
	}, model.RequestPolicy{
		MaxOutputTokens: 128,
		CacheRetention:  model.CacheRetentionShort,
	})
}

func TestLiveOpenRouterChatCompletionsStreamText(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for live OpenRouter Chat Completions stream test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	token := liveChatCompletionsToken()
	client := Client{
		Auth:              liveOpenRouterAuth(apiKey),
		BaseURL:           liveOpenRouterBaseURL(),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
		ProviderModelSlug: liveOpenRouterProviderModelSlug(),
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(`{"reasoning":{"enabled":false}}`),
	}
	prepared, err := client.Prepare(ctx, model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "Follow the user response format exactly.",
			Messages: []modelcontext.Message{{
				Sequence: 1,
				Role:     modelprotocol.RoleUser,
				Content:  liveTextContent(t, "Reply with exactly this token and no extra words: "+token),
			}},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare live OpenRouter streaming request: %v", err)
	}
	sink := &chatRecordingSink{}
	resp, err := client.Respond(ctx, model.Request{ProviderRequest: prepared.Body, DeltaSink: sink})
	if err != nil {
		t.Fatalf("live OpenRouter streaming response: %v", err)
	}
	if strings.TrimSpace(resp.ID) == "" {
		t.Fatalf("live streaming response id is empty: %+v", resp)
	}
	if !strings.Contains(resp.Text(), token) {
		t.Fatalf("live streaming response text %q does not contain token %q", resp.Text(), token)
	}
	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
		t.Fatalf("live streaming response usage not populated: %+v", resp.Usage)
	}
	var streamedText strings.Builder
	for _, event := range sink.events {
		if event.Kind == model.StreamEventTextDelta {
			streamedText.WriteString(event.Delta)
		}
	}
	if !strings.Contains(streamedText.String(), token) {
		t.Fatalf("streamed text %q does not contain token %q", streamedText.String(), token)
	}
	if len(sink.events) == 0 || sink.events[len(sink.events)-1].Kind != model.StreamEventMessageStop {
		t.Fatalf("expected terminal message stop event, got %+v", sink.events)
	}
}

func TestLiveOpenRouterChatCompletionsToolCall(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for live OpenRouter Chat Completions tool test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	token := liveChatCompletionsToken()
	supportsTools := true
	client := Client{
		Auth:              liveOpenRouterAuth(apiKey),
		BaseURL:           liveOpenRouterBaseURL(),
		EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
		ProviderModelSlug: liveOpenRouterToolProviderModelSlug(),
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		APIVariantOptions: json.RawMessage(`{"provider":{"sort":"price"}}`),
	}
	prepared, err := client.Prepare(ctx, model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "Use tools exactly when the user asks for a tool call.",
			Messages: []modelcontext.Message{{
				Sequence: 1,
				Role:     modelprotocol.RoleUser,
				Content: liveTextContent(
					t,
					"Call the record_token tool exactly once with token "+token+". Do not answer in text.",
				),
			}},
			ToolSpecs: []modelcontext.ToolSpec{{
				Name:        "record_token",
				Description: "Records the exact token requested by the user.",
				InputSchema: json.RawMessage(
					`{"type":"object","additionalProperties":false,"properties":{"token":{"type":"string"}},"required":["token"]}`,
				),
			}},
		},
		Policy: model.RequestPolicy{
			MaxOutputTokens: 128,
			SupportsTools:   &supportsTools,
		},
	})
	if err != nil {
		t.Fatalf("prepare live OpenRouter tool request: %v", err)
	}
	resp, err := client.Respond(ctx, model.Request{ProviderRequest: prepared.Body})
	if err != nil {
		t.Fatalf("live OpenRouter tool response: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) == 0 {
		t.Fatalf("live OpenRouter response did not include a tool call: %+v text=%q", resp, resp.Text())
	}
	if calls[0].Name != "record_token" {
		t.Fatalf("live OpenRouter tool name = %q, want record_token; calls=%+v", calls[0].Name, calls)
	}
	if !strings.Contains(string(calls[0].Input), token) {
		t.Fatalf("live OpenRouter tool input %s does not contain token %q", calls[0].Input, token)
	}
}

func runLiveChatCompletionsText(t *testing.T, client Client, policy model.RequestPolicy) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	token := liveChatCompletionsToken()
	prompt := "Reply with exactly this token and no extra words: " + token
	prepared, err := client.Prepare(ctx, model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "Follow the user response format exactly.",
			Messages: []modelcontext.Message{{
				Sequence: 1,
				Role:     modelprotocol.RoleUser,
				Content:  liveTextContent(t, prompt),
			}},
		},
		Policy: policy,
	})
	if err != nil {
		t.Fatalf("prepare live chat completions request: %v", err)
	}
	resp, err := client.Respond(ctx, model.Request{ProviderRequest: prepared.Body})
	if err != nil {
		t.Fatalf("live chat completions response: %v", err)
	}
	if strings.TrimSpace(resp.ID) == "" {
		t.Fatalf("live response id is empty: %+v", resp)
	}
	if !strings.Contains(resp.Text(), token) {
		t.Fatalf("live response text %q does not contain token %q", resp.Text(), token)
	}
	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
		t.Fatalf("live response usage not populated: %+v", resp.Usage)
	}
}

func liveTextContent(t *testing.T, text string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		t.Fatalf("marshal live text content: %v", err)
	}
	return raw
}

func liveChatCompletionsToken() string {
	stamp := time.Now().UTC().Format("20060102T150405000000000")
	return "OMNARA_LIVE_CHAT_" + stamp
}

func liveOpenAIChatProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_CHAT_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "gpt-4.1-mini"
}

func liveOpenRouterProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENROUTER_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "z-ai/glm-5.2"
}

func liveOpenRouterOpenAIProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENROUTER_OPENAI_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "openai/gpt-4.1-mini"
}

func liveOpenRouterAnthropicProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENROUTER_ANTHROPIC_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "anthropic/claude-sonnet-4.6"
}

func liveOpenRouterSettingsProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENROUTER_SETTINGS_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "z-ai/glm-5.2"
}

func liveOpenRouterToolProviderModelSlug() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENROUTER_TOOL_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "z-ai/glm-5.2"
}

func liveOpenRouterBaseURL() string {
	if baseURL := os.Getenv("OPENROUTER_BASE_URL"); baseURL != "" {
		return baseURL
	}
	return "https://openrouter.ai/api/v1"
}

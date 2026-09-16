//go:build live

package model_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/providererrors"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const liveDeferredToolsProviderConfigID = "live-deferred-tools"

type liveDeferredToolsRoute struct {
	name            string
	keyEnv          string
	client          func(apiKey string) model.Client
	unsupportedTurn int
}

func liveDeferredToolsRoutes() []liveDeferredToolsRoute {
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
	anthropic := func(slug string) func(apiKey string) model.Client {
		return func(apiKey string) model.Client {
			return anthropicmessages.Client{
				ModelProviderConfigID: liveDeferredToolsProviderConfigID,
				Auth:                  route.HeaderAuth{Header: "x-api-key", Value: apiKey},
				BaseURL:               os.Getenv("ANTHROPIC_BASE_URL"),
				EndpointPath:          modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatAnthropicMessages),
				ProviderModelSlug:     slug,
			}
		}
	}
	openAIResponses := func(slug string) func(apiKey string) model.Client {
		return func(apiKey string) model.Client {
			return openairesponses.Client{
				ModelProviderConfigID: liveDeferredToolsProviderConfigID,
				Auth:                  route.BearerToken{Token: apiKey},
				BaseURL:               os.Getenv("OPENAI_BASE_URL"),
				EndpointPath:          modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIResponses),
				ProviderModelSlug:     slug,
			}
		}
	}
	openAIChat := func(slug string) func(apiKey string) model.Client {
		return func(apiKey string) model.Client {
			return openaichatcompletions.Client{
				ModelProviderConfigID: liveDeferredToolsProviderConfigID,
				Auth:                  route.BearerToken{Token: apiKey},
				BaseURL:               os.Getenv("OPENAI_BASE_URL"),
				EndpointPath:          modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
				ProviderModelSlug:     slug,
			}
		}
	}
	openRouter := func(slug string) func(apiKey string) model.Client {
		return func(apiKey string) model.Client {
			return openaichatcompletions.Client{
				ModelProviderConfigID: liveDeferredToolsProviderConfigID,
				Auth:                  openRouterAuth(apiKey),
				BaseURL:               openRouterBaseURL,
				EndpointPath:          modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
				ProviderModelSlug:     slug,
				APIVariant:            modelprotocol.APIVariantOpenRouter,
			}
		}
	}
	return []liveDeferredToolsRoute{
		{name: "anthropic opus", keyEnv: "ANTHROPIC_API_KEY", client: anthropic("claude-opus-5")},
		{name: "anthropic sonnet unsupported", keyEnv: "ANTHROPIC_API_KEY", client: anthropic(modeltest.LiveAnthropicProviderModelSlug), unsupportedTurn: 2},
		{name: "openai responses", keyEnv: "OPENAI_API_KEY", client: openAIResponses(modeltest.LiveOpenAIProviderModelSlug)},
		{name: "openai responses gpt-5.4", keyEnv: "OPENAI_API_KEY", client: openAIResponses("gpt-5.4")},
		{name: "openai responses gpt-4.1 unsupported", keyEnv: "OPENAI_API_KEY", client: openAIResponses("gpt-4.1"), unsupportedTurn: 1},
		{name: "openai chat gpt-5.4", keyEnv: "OPENAI_API_KEY", client: openAIChat("gpt-5.4")},
		{name: "openai chat gpt-4.1", keyEnv: "OPENAI_API_KEY", client: openAIChat("gpt-4.1")},
		{name: "openrouter claude", keyEnv: "OPENROUTER_API_KEY", client: openRouter("anthropic/" + modeltest.LiveAnthropicProviderModelSlug)},
		{name: "openrouter automatic", keyEnv: "OPENROUTER_API_KEY", client: openRouter(modeltest.LiveOpenRouterProviderModelSlug)},
	}
}

func TestLiveDeferredToolsLoadAfterSearch(t *testing.T) {
	for _, r := range liveDeferredToolsRoutes() {
		t.Run(r.name, func(t *testing.T) {
			apiKey := strings.TrimSpace(os.Getenv(r.keyEnv))
			if apiKey == "" {
				t.Skipf("%s is not set", r.keyEnv)
			}
			client := r.client(apiKey)
			specs := liveDeferredToolSpecs(t)
			bundle := modelcontext.Bundle{
				AgentID:      uuid.New(),
				SystemPrompt: "You are a tool-calling assistant. Never answer in text when a tool call is requested.",
				Messages: []modelcontext.Message{
					liveTextMessage(
						modelprotocol.RoleUser,
						10,
						"Call the get_weather tool for the city Paris. get_weather is a deferred tool, "+
							"so first call tool_search with the pattern \"weather\" to load it, then call it.",
					),
				},
				ToolSpecs: specs,
			}
			first, err := liveDeferredRespond(client, bundle)
			if r.unsupportedTurn == 1 {
				assertLiveDeferredToolsUnsupported(t, err)
				return
			}
			if err != nil {
				t.Fatalf("first turn: %v", err)
			}
			search := liveSingleToolCall(t, first, toolcatalog.ToolNameToolSearch)
			result := liveToolSearchResult(t, specs, search)
			bundle.Messages = append(bundle.Messages, modelcontext.Message{
				Role:                 modelprotocol.RoleAssistant,
				Sequence:             20,
				ModelCallContextID:   result.ModelCallContextID,
				Content:              json.RawMessage(`[{"type":"tool_call","tool_call_id":"` + result.ToolCallID + `"}]`),
				ProviderReplay:       first.ProviderReplay,
				ProviderReplaySource: model.ProviderReplayIdentityForClient(liveDeferredToolsProviderConfigID, client),
				StopReason:           first.StopReason,
			})
			bundle.ToolResults = []modelcontext.ToolResultRef{result}
			second, err := liveDeferredRespond(client, bundle)
			if r.unsupportedTurn == 2 {
				assertLiveDeferredToolsUnsupported(t, err)
				return
			}
			if err != nil {
				t.Fatalf("second turn: %v", err)
			}
			weather := liveSingleToolCall(t, second, "get_weather")
			if !strings.Contains(strings.ToLower(string(weather.Input)), "paris") {
				t.Fatalf("get_weather input %s does not name Paris", weather.Input)
			}
		})
	}
}

func liveDeferredToolSpecs(t *testing.T) []modelcontext.ToolSpec {
	t.Helper()
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("tool catalog: %v", err)
	}
	entry, ok := catalog.Lookup(toolcatalog.ToolNameToolSearch)
	if !ok {
		t.Fatalf("tool catalog has no %s", toolcatalog.ToolNameToolSearch)
	}
	return []modelcontext.ToolSpec{
		{Name: entry.Name, Description: entry.Description, InputSchema: entry.InputSchema},
		{
			Name:        "get_time",
			Description: "Returns the current time.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		},
		{
			Name:        "get_weather",
			Description: "Returns the current weather for a city.",
			InputSchema: json.RawMessage(
				`{"type":"object","additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"]}`,
			),
			Deferred: true,
		},
	}
}

func liveDeferredRespond(client model.Client, bundle modelcontext.Bundle) (model.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	supportsTools := true
	prepared, err := client.Prepare(ctx, model.PrepareInput{
		Context: bundle,
		Policy:  model.RequestPolicy{MaxOutputTokens: 1024, SupportsTools: &supportsTools},
	})
	if err != nil {
		return model.Response{}, err
	}
	return client.Respond(ctx, model.Request{ProviderRequest: prepared.Body})
}

func liveSingleToolCall(t *testing.T, response model.Response, name string) model.ToolCall {
	t.Helper()
	calls := response.ToolCalls()
	if len(calls) != 1 || calls[0].Name != name {
		t.Fatalf("expected a single %s call, got %+v text=%q", name, calls, response.Text())
	}
	return calls[0]
}

func liveToolSearchResult(t *testing.T, specs []modelcontext.ToolSpec, call model.ToolCall) modelcontext.ToolResultRef {
	t.Helper()
	var input struct {
		Pattern    string `json:"pattern"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(call.Input, &input); err != nil {
		t.Fatalf("decode tool_search input %s: %v", call.Input, err)
	}
	search, err := modelcontext.SearchDeferredTools(specs, input.Pattern, input.MaxResults)
	if err != nil {
		t.Fatalf("search deferred tools: %v", err)
	}
	if len(search.ToolNames) != 1 || search.ToolNames[0] != "get_weather" {
		t.Fatalf("tool_search pattern %q matched %v, want get_weather", input.Pattern, search.ToolNames)
	}
	structured, err := json.Marshal(search)
	if err != nil {
		t.Fatalf("encode search result: %v", err)
	}
	parts, err := json.Marshal([]map[string]any{
		{"type": "text", "text": "Loaded 1 tool(s) matching the pattern: get_weather"},
		{"type": "structured_data", "value": json.RawMessage(structured)},
	})
	if err != nil {
		t.Fatalf("encode result parts: %v", err)
	}
	return modelcontext.ToolResultRef{
		ToolCallID:         "tcl_search",
		ModelCallContextID: "mcc_search",
		ProviderCallID:     call.ID,
		Name:               toolcatalog.ToolNameToolSearch,
		Input:              call.Input,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ContentParts:       parts,
	}
}

func assertLiveDeferredToolsUnsupported(t *testing.T, err error) {
	t.Helper()
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != providererrors.DeferredToolsUnsupportedCode ||
		providerErr.Message != providererrors.DeferredToolsUnsupportedMessage {
		t.Fatalf("expected deferred tools unsupported error, got %+v ok=%v err=%v", providerErr, ok, err)
	}
}

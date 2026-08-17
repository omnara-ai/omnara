package compaction

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestCompactionPolicyOutputReservationMatchesProviderWireCap(t *testing.T) {
	capabilities := model.Capabilities{
		ContextWindowTokens:    200_000,
		MaxOutputTokens:        64_000,
		SupportsReasoning:      true,
		DefaultReasoningEffort: "high",
		SupportedReasoningEfforts: []string{
			"high", "low",
		},
	}
	uppercaseEfforts := capabilities
	uppercaseEfforts.DefaultReasoningEffort = "HIGH"
	uppercaseEfforts.SupportedReasoningEfforts = []string{"HIGH", "LOW"}
	tests := []struct {
		name        string
		client      model.Client
		outputField string
		reasoning   string
	}{
		{
			name: "OpenRouter Chat Completions",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "openai/gpt-test",
				ModelCapabilities: capabilities,
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			},
			outputField: "max_completion_tokens",
			reasoning:   `{"effort":"low"}`,
		},
		{
			name: "OpenAI Responses",
			client: openairesponses.Client{
				EndpointPath:      "/responses",
				ProviderModelSlug: "gpt-test",
				ModelCapabilities: capabilities,
			},
			outputField: "max_output_tokens",
			reasoning:   `{"effort":"low"}`,
		},
		{
			name: "Anthropic Messages",
			client: anthropicmessages.Client{
				EndpointPath:      "/messages",
				ProviderModelSlug: "claude-test",
				ModelCapabilities: capabilities,
			},
			outputField: "max_tokens",
		},
		{
			name: "OpenRouter preserves advertised reasoning wire value",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "openai/gpt-test",
				ModelCapabilities: uppercaseEfforts,
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			},
			outputField: "max_completion_tokens",
			reasoning:   `{"effort":"LOW"}`,
		},
		{
			name: "Responses preserves advertised reasoning wire value",
			client: openairesponses.Client{
				EndpointPath:      "/responses",
				ProviderModelSlug: "gpt-test",
				ModelCapabilities: uppercaseEfforts,
			},
			outputField: "max_output_tokens",
			reasoning:   `{"effort":"LOW"}`,
		},
	}
	bundle := modelcontext.Bundle{
		SystemPrompt: "Summarize the closed history.",
		Messages: []modelcontext.Message{{
			Role:     modelprotocol.RoleUser,
			Sequence: 1,
			Content:  json.RawMessage(`[{"type":"text","text":"closed history"}]`),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := compactionRequestPolicy(test.client)
			if policy.MaxOutputTokens != 16_384 {
				t.Fatalf("reserved output = %d, want 16384", policy.MaxOutputTokens)
			}
			prepared, err := test.client.Prepare(
				context.Background(),
				model.PrepareInput{Context: bundle, Policy: policy},
			)
			if err != nil {
				t.Fatalf("prepare compaction wire request: %v", err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(prepared.Body, &body); err != nil {
				t.Fatalf("decode compaction wire request: %v", err)
			}
			if got := string(body[test.outputField]); got != "16384" {
				t.Fatalf("wire %s = %s, want 16384 in %s", test.outputField, got, prepared.Body)
			}
			if test.reasoning == "" {
				if _, found := body["reasoning"]; found {
					t.Fatalf("unexpected Anthropic reasoning field in %s", prepared.Body)
				}
				return
			}
			if got := string(body["reasoning"]); got != test.reasoning {
				t.Fatalf("wire reasoning = %s, want %s in %s", got, test.reasoning, prepared.Body)
			}
		})
	}
}

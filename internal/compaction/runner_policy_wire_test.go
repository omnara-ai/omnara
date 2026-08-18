package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestCompactionPolicyPreservesResolvedReasoningAtProviderWireBoundary(t *testing.T) {
	highReasoning := model.Capabilities{
		ContextWindowTokens:    200_000,
		MaxOutputTokens:        64_000,
		DefaultMaxOutputTokens: 2_048,
		SupportsReasoning:      true,
		DefaultReasoningEffort: "high",
		SupportedReasoningEfforts: []string{
			"high", "low",
		},
	}
	opaqueReasoning := highReasoning
	opaqueReasoning.DefaultReasoningEffort = "vendor-deep"
	opaqueReasoning.SupportedReasoningEfforts = []string{"vendor-fast", "vendor-deep"}
	explicitNone := highReasoning
	explicitNone.DefaultReasoningEffort = "none"
	explicitNone.SupportedReasoningEfforts = []string{"none", "high"}
	providerDefaultReasoning := highReasoning
	providerDefaultReasoning.DefaultReasoningEffort = ""
	nonReasoning := highReasoning
	nonReasoning.SupportsReasoning = false
	nonReasoning.DefaultReasoningEffort = ""
	nonReasoning.SupportedReasoningEfforts = nil
	tests := []struct {
		name             string
		client           model.Client
		outputField      string
		wantFields       map[string]string
		wantAbsentFields []string
	}{
		{
			name: "OpenAI Chat Completions",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "gpt-test",
				ModelCapabilities: highReasoning,
			},
			outputField: "max_completion_tokens",
			wantFields: map[string]string{
				"reasoning_effort": `"high"`,
			},
			wantAbsentFields: []string{"reasoning"},
		},
		{
			name: "OpenRouter Chat Completions",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "openai/gpt-test",
				ModelCapabilities: opaqueReasoning,
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			},
			outputField: "max_completion_tokens",
			wantFields: map[string]string{
				"reasoning": `{"effort":"vendor-deep"}`,
			},
			wantAbsentFields: []string{"reasoning_effort"},
		},
		{
			name: "OpenAI Responses",
			client: openairesponses.Client{
				EndpointPath:      "/responses",
				ProviderModelSlug: "gpt-test",
				ModelCapabilities: highReasoning,
			},
			outputField: "max_output_tokens",
			wantFields: map[string]string{
				"reasoning": `{"effort":"high"}`,
			},
			wantAbsentFields: []string{"reasoning_effort"},
		},
		{
			name: "explicit model-supported none option",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "gpt-test",
				ModelCapabilities: explicitNone,
			},
			outputField: "max_completion_tokens",
			wantFields: map[string]string{
				"reasoning_effort": `"none"`,
			},
			wantAbsentFields: []string{"reasoning"},
		},
		{
			name: "OpenRouter adapter reasoning option with no selected effort",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "openai/gpt-test",
				ModelCapabilities: providerDefaultReasoning,
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				APIVariantOptions: json.RawMessage(`{"reasoning":{"enabled":false}}`),
			},
			outputField: "max_completion_tokens",
			wantFields: map[string]string{
				"reasoning": `{"enabled":false}`,
			},
			wantAbsentFields: []string{"reasoning_effort"},
		},
		{
			name: "reasoning-capable Responses model with provider default",
			client: openairesponses.Client{
				EndpointPath:      "/responses",
				ProviderModelSlug: "gpt-test",
				ModelCapabilities: providerDefaultReasoning,
			},
			outputField:      "max_output_tokens",
			wantAbsentFields: []string{"reasoning", "reasoning_effort"},
		},
		{
			name: "non-reasoning OpenAI Chat Completions model",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "gpt-test",
				ModelCapabilities: nonReasoning,
			},
			outputField:      "max_completion_tokens",
			wantAbsentFields: []string{"reasoning", "reasoning_effort"},
		},
		{
			name: "non-reasoning Anthropic Messages model",
			client: anthropicmessages.Client{
				EndpointPath:      "/messages",
				ProviderModelSlug: "claude-test",
				ModelCapabilities: nonReasoning,
			},
			outputField:      "max_tokens",
			wantAbsentFields: []string{"reasoning", "reasoning_effort", "thinking"},
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
			compactionPolicy, err := compactionRequestPolicy(test.client, "test")
			if err != nil {
				t.Fatalf("compaction request policy: %v", err)
			}
			compactionBody := preparePolicyWireBody(
				t,
				test.client,
				bundle,
				compactionPolicy,
			)
			normalBody := preparePolicyWireBody(
				t,
				test.client,
				bundle,
				model.RequestPolicyFromCapabilities(model.CapabilitiesForClient(test.client)),
			)
			if got := string(compactionBody[test.outputField]); got != "16384" {
				t.Fatalf("wire %s = %s, want 16384", test.outputField, got)
			}
			for _, field := range []string{"reasoning", "reasoning_effort", "include", "thinking"} {
				compactionValue, compactionHasField := compactionBody[field]
				normalValue, normalHasField := normalBody[field]
				if compactionHasField != normalHasField || string(compactionValue) != string(normalValue) {
					t.Fatalf(
						"compaction %s = %s (present=%v), normal = %s (present=%v)",
						field,
						compactionValue,
						compactionHasField,
						normalValue,
						normalHasField,
					)
				}
			}
			for field, want := range test.wantFields {
				if got := string(compactionBody[field]); got != want {
					t.Fatalf("wire %s = %s, want %s", field, got, want)
				}
			}
			for _, field := range test.wantAbsentFields {
				if _, found := compactionBody[field]; found {
					t.Fatalf("unexpected %s in compaction request: %s", field, compactionBody[field])
				}
			}
		})
	}
}

func TestCompactionPolicyUsesReconciledOutputLimitForWireAndAdmission(t *testing.T) {
	capabilities := model.Capabilities{
		ContextWindowTokens:    200_000,
		MaxOutputTokens:        64_000,
		DefaultMaxOutputTokens: 32_768,
	}
	tests := []struct {
		name        string
		client      model.Client
		outputField string
		absentField string
		optionField string
		wantOutput  int
	}{
		{
			name: "Anthropic Messages",
			client: anthropicmessages.Client{
				EndpointPath:      "/messages",
				ProviderModelSlug: "claude-sonnet-4",
				ModelCapabilities: capabilities,
				APIVariantOptions: json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":24576}}`),
			},
			outputField: "max_tokens",
			optionField: "thinking",
			wantOutput:  32_768,
		},
		{
			name: "OpenRouter Chat Completions",
			client: openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "anthropic/claude-sonnet-4",
				ModelCapabilities: capabilities,
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				APIVariantOptions: json.RawMessage(
					`{"models":["~anthropic/claude-opus-4"],"max_tokens":10000,` +
						`"max_completion_tokens":11000,"reasoning":{"max_tokens":24576}}`,
				),
			},
			outputField: "max_completion_tokens",
			absentField: "max_tokens",
			optionField: "reasoning",
			wantOutput:  16_384,
		},
	}
	bundle := modelcontext.Bundle{Messages: []modelcontext.Message{{
		Role:     modelprotocol.RoleUser,
		Sequence: 1,
		Content:  json.RawMessage(`[{"type":"text","text":"summarize this history"}]`),
	}}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := compactionRequestPolicy(test.client, "test")
			if err != nil {
				t.Fatalf("compaction request policy: %v", err)
			}
			if policy.MaxOutputTokens != test.wantOutput {
				t.Fatalf("max output = %d, want %d", policy.MaxOutputTokens, test.wantOutput)
			}
			prepared, err := model.PrepareForSend(
				context.Background(),
				test.client,
				model.PrepareForSendInput{
					Context:     bundle,
					Policy:      policy,
					ErrorSource: "test",
				},
			)
			if err != nil {
				t.Fatalf("prepare reconciled request: %v", err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(prepared.Body, &body); err != nil {
				t.Fatalf("decode provider request: %v", err)
			}
			wantOutput := fmt.Sprintf("%d", test.wantOutput)
			if got := string(body[test.outputField]); got != wantOutput {
				t.Fatalf("wire %s = %s, want %s", test.outputField, got, wantOutput)
			}
			if test.absentField != "" {
				if _, found := body[test.absentField]; found {
					t.Fatalf("alternate output field %s remained on wire: %s", test.absentField, prepared.Body)
				}
			}
			if _, found := body[test.optionField]; !found {
				t.Fatalf("provider-owned %s option was removed: %s", test.optionField, prepared.Body)
			}
			wantUsable := model.UsableInputTokensForRequest(capabilities, policy)
			if prepared.InputBudget.UsableInputTokens != wantUsable ||
				prepared.InputBudget.EstimatedInputTokens <= 0 {
				t.Fatalf(
					"admission = %+v, want exact wire policy usable input %d",
					prepared.InputBudget,
					wantUsable,
				)
			}
		})
	}
}

func preparePolicyWireBody(
	t *testing.T,
	client model.Client,
	bundle modelcontext.Bundle,
	policy model.RequestPolicy,
) map[string]json.RawMessage {
	t.Helper()
	prepared, err := client.Prepare(
		context.Background(),
		model.PrepareInput{Context: bundle, Policy: policy},
	)
	if err != nil {
		t.Fatalf("prepare wire request: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	return body
}

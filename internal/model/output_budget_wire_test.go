package model_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func outputWireClients(capabilities model.Capabilities) []struct {
	name, field string
	client      model.Client
} {
	return []struct {
		name, field string
		client      model.Client
	}{
		{
			"chat",
			"max_completion_tokens",
			openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "test-model",
				ModelCapabilities: capabilities,
			},
		},
		{
			"openrouter",
			"max_completion_tokens",
			openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "vendor/test-model",
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				ModelCapabilities: capabilities,
			},
		},
		{
			"responses",
			"max_output_tokens",
			openairesponses.Client{
				EndpointPath:      "/responses",
				ProviderModelSlug: "test-model",
				ModelCapabilities: capabilities,
			},
		},
		{
			"anthropic",
			"max_tokens",
			anthropicmessages.Client{
				EndpointPath:      "/messages",
				ProviderModelSlug: "test-model",
				ModelCapabilities: capabilities,
			},
		},
		{
			"anthropic-router",
			"max_tokens",
			anthropicmessages.Client{
				EndpointPath:      "/messages",
				ProviderModelSlug: "anthropic/test-model",
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				ModelCapabilities: capabilities,
			},
		},
	}
}

func TestPreparedOutputAllowanceUsesAdapterWireFields(t *testing.T) {
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": strings.Repeat("input detail ", 6000)}})
	if err != nil {
		t.Fatal(err)
	}
	bundle := modelcontext.Bundle{
		Messages: []modelcontext.Message{
			{
				ID:       "input",
				Sequence: 1,
				Role:     modelprotocol.RoleUser,
				Content:  content,
			},
		},
	}
	for _, known := range []bool{false, true} {
		caps := model.Capabilities{ContextWindowTokens: 100000}
		if known {
			caps.MaxOutputTokens = new(90000)
		}
		for _, tc := range outputWireClients(caps) {
			label := "unknown"
			if known {
				label = "known"
			}
			t.Run(tc.name+"/"+label, func(t *testing.T) {
				client := &countingWirePreparation{Client: tc.client}
				prepared, err := model.PrepareForSend(
					context.Background(),
					client,
					model.PrepareForSendInput{
						Context:     bundle,
						Policy:      model.RequestPolicyFromCapabilities(caps),
						ErrorSource: tc.name,
					},
				)
				if !known && tc.client.APIFormat() == modelprotocol.APIFormatAnthropicMessages {
					var providerErr model.ProviderError
					if !errors.As(
						err,
						&providerErr,
					) ||
						providerErr.Code != model.OutputTokenLimitRequiredCode ||
						client.prepares != 0 {
						t.Fatalf("required allowance error=%v prepares=%d", err, client.prepares)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !prepared.InputBudget.Fits() {
					t.Fatalf("budget=%+v", prepared.InputBudget)
				}
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(prepared.Body, &wire); err != nil {
					t.Fatal(err)
				}
				if !known {
					for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
						if _, found := wire[field]; found {
							t.Fatalf("unknown allowance serialized as %s", field)
						}
					}
					return
				}
				var allowance int
				if err := json.Unmarshal(wire[tc.field], &allowance); err != nil {
					t.Fatal(err)
				}
				remaining := caps.ContextWindowTokens -
					modelcontext.DefaultSafetyMarginTokens(caps.ContextWindowTokens) - prepared.InputTokenEstimate
				if allowance <= 32768 ||
					allowance >= 90000 ||
					allowance > remaining ||
					remaining-allowance > 1 ||
					allowance != prepared.MaxOutputTokens {
					t.Fatalf("allowance=%d remaining=%d recorded=%d", allowance, remaining, prepared.MaxOutputTokens)
				}
			})
		}
	}
}

func TestAdaptersKeepOutputFeedbackAfterPartialAssistant(t *testing.T) {
	for _, partial := range []string{
		`[]`,
		`[{"type":"reasoning","text":"partial reasoning"}]`,
		`[{"type":"text","text":"partial answer"}]`,
	} {
		for _, tc := range outputWireClients(model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     new(64000),
		}) {
			t.Run(tc.name+"/"+partial, func(t *testing.T) {
				bundle := modelcontext.Bundle{Messages: []modelcontext.Message{
					{
						ID:       "input",
						Sequence: 1,
						Role:     modelprotocol.RoleUser,
						Content:  json.RawMessage(`[{"type":"text","text":"complete the task"}]`),
					},
					{
						ID:                 "partial",
						Sequence:           2,
						Role:               modelprotocol.RoleAssistant,
						ModelCallContextID: "source-context",
						Content:            json.RawMessage(partial),
					},
					{
						ID:       "feedback",
						Sequence: 2,
						Role:     modelprotocol.RoleUser,
						Content:  json.RawMessage(`[{"type":"text","text":"use smaller calls"}]`),
					},
				}}
				prepared, err := model.PrepareForSend(
					context.Background(),
					tc.client,
					model.PrepareForSendInput{
						Context: bundle,
						Policy: model.RequestPolicy{
							MaxOutputTokens: 64000,
						},
						ErrorSource: tc.name,
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(prepared.Body, &wire); err != nil {
					t.Fatal(err)
				}
				field := "messages"
				if tc.name == "responses" {
					field = "input"
				}
				var messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				if err := json.Unmarshal(wire[field], &messages); err != nil {
					t.Fatal(err)
				}
				assistantCount := 0
				for _, message := range messages {
					if message.Role == "assistant" {
						assistantCount++
					}
				}
				wantAssistant := 0
				if strings.Contains(partial, "partial answer") {
					wantAssistant = 1
				}
				if assistantCount != wantAssistant {
					t.Fatalf("assistant messages=%d want=%d", assistantCount, wantAssistant)
				}
				last := messages[len(messages)-1]
				if last.Role != "user" || !strings.Contains(string(last.Content), "use smaller calls") {
					t.Fatalf("last message=%+v", last)
				}
				if strings.Contains(
					partial,
					"partial answer",
				) &&
					!strings.Contains(
						string(prepared.Body),
						"partial answer",
					) {
					t.Fatal("partial answer lost")
				}
			})
		}
	}
}

type countingWirePreparation struct {
	model.Client
	prepares int
}

func (c *countingWirePreparation) Prepare(
	ctx context.Context,
	input model.PrepareInput,
) (model.PreparedRequest, error) {
	c.prepares++
	return c.Client.Prepare(ctx, input)
}
func (c *countingWirePreparation) OutputTokenLimits() (model.OutputTokenLimits, error) {
	if provider, ok := c.Client.(model.OutputTokenLimitProvider); ok {
		return provider.OutputTokenLimits()
	}
	return model.OutputTokenLimits{}, nil
}

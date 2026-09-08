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
	"github.com/stretchr/testify/require"
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
	}
}

func TestPreparedOutputAllowanceUsesAdapterWireFields(t *testing.T) {
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": strings.Repeat("input detail ", 6000)}})
	require.NoError(t, err)
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
				prepared, err := model.PrepareForSend(
					context.Background(),
					tc.client,
					model.PrepareForSendInput{
						Context:     bundle,
						Policy:      model.RequestPolicyFromCapabilities(caps),
						ErrorSource: tc.name,
					},
				)
				if !known && tc.client.APIFormat() == modelprotocol.APIFormatAnthropicMessages {
					var providerErr model.ProviderError
					if !errors.As(err, &providerErr) || providerErr.Code != model.OutputTokenLimitRequiredCode {
						t.Fatalf("required allowance error=%v", err)
					}
					return
				}
				require.NoError(t, err)
				if !prepared.InputBudget.Fits() {
					t.Fatalf("budget=%+v", prepared.InputBudget)
				}
				var wire map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(prepared.Body, &wire))
				if !known {
					for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
						if _, found := wire[field]; found {
							t.Fatalf("unknown allowance serialized as %s", field)
						}
					}
					return
				}
				var allowance int
				require.NoError(t, json.Unmarshal(wire[tc.field], &allowance))
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

func TestAdaptersProjectOutputLimitNoticeAfterPartialAssistant(t *testing.T) {
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
						StopReason:         model.StopReasonMaxTokens,
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
				require.NoError(t, err)
				var wire map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(prepared.Body, &wire))
				field := "messages"
				if tc.name == "responses" {
					field = "input"
				}
				var messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				require.NoError(t, json.Unmarshal(wire[field], &messages))
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
				if last.Role != "user" || !strings.Contains(string(last.Content), "Automatic Omnara harness notice") {
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

package openaichatcompletions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type steeredChatMessage struct {
	Role                string          `json:"role"`
	Content             json.RawMessage `json:"content"`
	ConfigurationUpdate *struct {
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	} `json:"configuration_update"`
}

func prepareSteeredChatRequest(
	t *testing.T,
	client Client,
	effort string,
	bundle modelcontext.Bundle,
) (json.RawMessage, []steeredChatMessage) {
	t.Helper()
	client.EndpointPath = testEndpointPath
	if client.APIVariant == "" {
		client.APIVariant = modelprotocol.APIVariantOpenRouter
	}
	client.ModelCapabilities.SupportsReasoning = true
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: bundle,
		Policy:  model.RequestPolicy{MaxOutputTokens: 4096, ReasoningEffort: effort},
	})
	require.NoError(t, err)
	var payload struct {
		Reasoning json.RawMessage      `json:"reasoning"`
		Messages  []steeredChatMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	return payload.Reasoning, payload.Messages
}

func steeredChatText(role modelprotocol.MessageRole, sequence int64, text string) modelcontext.Message {
	return modelcontext.Message{Sequence: sequence, Role: role,
		Content: json.RawMessage(`[{"type":"text","text":"` + text + `"}]`)}
}

func TestOpenRouterRetryEndsWithConfigurationUpdate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		client     Client
		effort     string
		retries    int
		wantEffort string
	}{
		{name: "gpt one level down", client: Client{ProviderModelSlug: "openai/gpt-6-sol"},
			effort: "high", retries: 1, wantEffort: "medium"},
		{name: "claude model default", client: Client{ProviderModelSlug: "anthropic/claude-fable-5.2"},
			retries: 2, wantEffort: "low"},
		{name: "options-supplied low is never raised", retries: 1, client: Client{
			ProviderModelSlug: "anthropic/claude-fable-5.2", APIVariantOptions: json.RawMessage(`{"reasoning":{"effort":"low"}}`),
		}},
		{name: "options-supplied effort", retries: 1, wantEffort: "medium", client: Client{
			ProviderModelSlug: "openai/gpt-6-sol", APIVariantOptions: json.RawMessage(`{"reasoning":{"effort":"high"}}`),
		}},
		{name: "options pro mode", retries: 1, client: Client{
			ProviderModelSlug: "openai/gpt-6-astra",
			APIVariantOptions: json.RawMessage(`{"reasoning":{"effort":"high","mode":"pro"}}`),
		}},
		{name: "options pro mode with reasoning_effort", retries: 1, client: Client{
			ProviderModelSlug: "openai/gpt-6-astra",
			APIVariantOptions: json.RawMessage(`{"reasoning_effort":"high","reasoning":{"mode":"pro"}}`),
		}},
		{name: "policy effort replaces options pro mode", effort: "high", retries: 1, wantEffort: "medium",
			client: Client{
				ProviderModelSlug: "openai/gpt-6-astra",
				APIVariantOptions: json.RawMessage(`{"reasoning":{"effort":"high","mode":"pro"}}`),
			}},
		{name: "options reasoning_effort", retries: 1, wantEffort: "medium", client: Client{
			ProviderModelSlug: "openai/gpt-6-sol", APIVariantOptions: json.RawMessage(`{"reasoning_effort":"high"}`),
		}},
		{name: "options reasoning budget", retries: 1, client: Client{
			ProviderModelSlug: "anthropic/claude-fable-5.2", APIVariantOptions: json.RawMessage(`{"reasoning":{"max_tokens":2048}}`),
		}},
		{name: "first attempt", client: Client{ProviderModelSlug: "openai/gpt-6-sol"}, effort: "high"},
		{name: "unsupported model", client: Client{ProviderModelSlug: "anthropic/claude-opus-5.5"},
			effort: "high", retries: 1},
		{name: "unknown default", client: Client{ProviderModelSlug: "openai/gpt-6-sol"}, retries: 1},
		{name: "not openrouter", effort: "high", retries: 1, client: Client{
			ProviderModelSlug: "gpt-6-sol", APIVariant: modelprotocol.APIVariantDefault,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasoning, messages := prepareSteeredChatRequest(t, tc.client, tc.effort, modelcontext.Bundle{
				Messages:                []modelcontext.Message{steeredChatText(modelprotocol.RoleUser, 1, "hi")},
				OutputNoProgressRetries: tc.retries,
			})
			if tc.effort != "" && tc.client.APIVariant == "" {
				require.JSONEq(t, `{"effort":"`+tc.effort+`"}`, string(reasoning),
					"the request's own effort must stay unchanged")
			}
			if tc.retries > 0 {
				require.Contains(t, string(messages[len(messages)-1-boolToInt(tc.wantEffort != "")].Content),
					modelcontext.OutputNoProgressNotice)
			}
			last := messages[len(messages)-1]
			if tc.wantEffort == "" {
				require.Nil(t, last.ConfigurationUpdate)
				return
			}
			require.Equal(t, "system", last.Role)
			require.JSONEq(t, `""`, string(last.Content))
			require.NotNil(t, last.ConfigurationUpdate)
			require.Equal(t, tc.wantEffort, last.ConfigurationUpdate.Reasoning.Effort)
		})
	}
}

func TestOpenRouterStoredOutputsReplayConfigurationUpdates(t *testing.T) {
	client := Client{ProviderModelSlug: "openai/gpt-6-sol"}
	_, retry := prepareSteeredChatRequest(t, client, "high", modelcontext.Bundle{
		Messages:                []modelcontext.Message{steeredChatText(modelprotocol.RoleUser, 1, "hi")},
		OutputNoProgressRetries: 1,
	})
	output := steeredChatText(modelprotocol.RoleAssistant, 2, "done")
	output.ModelCallContextID = "mcc_1"
	output.OutputNoProgressRetries = 1
	_, later := prepareSteeredChatRequest(t, client, "high", modelcontext.Bundle{Messages: []modelcontext.Message{
		steeredChatText(modelprotocol.RoleUser, 1, "hi"), output, steeredChatText(modelprotocol.RoleUser, 3, "again"),
	}})
	require.Equal(t, retry, later[:len(retry)], "later requests must keep the retry's prefix")
	require.Equal(t, "assistant", later[len(retry)].Role)
	require.Equal(t, "user", later[len(later)-1].Role)
	require.Nil(t, later[len(later)-1].ConfigurationUpdate)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

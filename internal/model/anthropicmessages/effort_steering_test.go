package anthropicmessages

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type effortSteeringRequest struct {
	Messages []struct {
		Role         string            `json:"role"`
		Content      json.RawMessage   `json:"content"`
		OutputConfig map[string]string `json:"output_config"`
	} `json:"messages"`
	OutputConfig map[string]json.RawMessage `json:"output_config"`
}

func prepareEffortSteeringRequest(
	t *testing.T,
	client Client,
	effort string,
	bundle modelcontext.Bundle,
) effortSteeringRequest {
	t.Helper()
	client.EndpointPath = testEndpointPath
	if client.ProviderModelSlug == "" {
		client.ProviderModelSlug = "claude-opus-5-5"
	}
	supportsReasoning := true
	prepared, err := client.Prepare(t.Context(), model.PrepareInput{
		Context: bundle,
		Policy: model.RequestPolicy{
			MaxOutputTokens: 4096, SupportsReasoning: &supportsReasoning, ReasoningEffort: effort,
		},
	})
	require.NoError(t, err)
	var payload effortSteeringRequest
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	return payload
}

func steeringOutput(sequence int64, retries int) modelcontext.Message {
	message := anthropicTextMessage(modelprotocol.RoleAssistant, "done")
	message.Sequence = sequence
	message.ModelCallContextID = "mcc_" + string(rune('a'+sequence))
	message.OutputNoProgressRetries = retries
	return message
}

func steeringInput(sequence int64, text string) modelcontext.Message {
	message := anthropicTextMessage(modelprotocol.RoleUser, text)
	message.Sequence = sequence
	return message
}

func TestRetryEndsWithLoweredEffortAfterTheNotice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		client     Client
		effort     string
		retries    int
		wantEffort string
	}{
		{name: "one level down", effort: "high", retries: 1, wantEffort: "medium"},
		{name: "one level per runaway", effort: "max", retries: 2, wantEffort: "high"},
		{name: "floors at low", effort: "medium", retries: 5, wantEffort: "low"},
		{name: "model default", retries: 1, wantEffort: "low"},
		{name: "default for fable", client: Client{ProviderModelSlug: "claude-fable-5-2"}, retries: 1,
			wantEffort: "medium"},
		{name: "supported efforts only", effort: "max", retries: 1, wantEffort: "high",
			client: Client{ModelCapabilities: model.Capabilities{SupportedReasoningEfforts: []string{"low", "high", "max"}}}},
		{name: "first attempt", effort: "high"},
		{name: "already lowest", effort: "low", retries: 1},
		{name: "model without per-message effort", client: Client{ProviderModelSlug: "claude-fable-5"},
			effort: "high", retries: 1},
		{name: "older claude", client: Client{ProviderModelSlug: "claude-opus-4-8"}, effort: "high", retries: 1},
		{name: "openrouter", effort: "high", retries: 1, client: Client{
			APIVariant: modelprotocol.APIVariantOpenRouter, ProviderModelSlug: "anthropic/claude-opus-5.5",
		}},
		{name: "bedrock", effort: "high", retries: 1, client: Client{
			APIVariant: modelprotocol.APIVariantBedrock, ProviderModelSlug: "anthropic.claude-opus-5-5",
		}},
		{name: "thinking between tools", effort: "high", retries: 1, client: Client{
			ProviderModelSlug: "claude-sonnet-5-5", APIVariantOptions: json.RawMessage(`{"thinking":{"type":"between_tools"}}`),
		}},
		{name: "thinking disabled", retries: 1, client: Client{
			ProviderModelSlug: "claude-haiku-5-5", APIVariantOptions: json.RawMessage(`{"thinking":{"type":"disabled"}}`),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := prepareEffortSteeringRequest(t, tc.client, tc.effort, modelcontext.Bundle{
				Messages:                []modelcontext.Message{steeringInput(1, "hi")},
				OutputNoProgressRetries: tc.retries,
			})
			if tc.effort != "" {
				require.JSONEq(t, `"`+tc.effort+`"`, string(payload.OutputConfig["effort"]),
					"the top-level effort must stay unchanged")
			}
			user := payload.Messages[0]
			require.Equal(t, "user", user.Role)
			if tc.retries > 0 {
				require.Contains(t, string(user.Content), modelcontext.OutputNoProgressNotice)
			} else {
				require.NotContains(t, string(user.Content), modelcontext.OutputNoProgressNotice)
			}
			if tc.wantEffort == "" {
				require.Len(t, payload.Messages, 1)
				return
			}
			require.Len(t, payload.Messages, 2)
			last := payload.Messages[1]
			require.Equal(t, "system", last.Role)
			require.JSONEq(t, `[]`, string(last.Content))
			require.Equal(t, map[string]string{"effort": tc.wantEffort}, last.OutputConfig)
		})
	}
}

func TestStoredOutputsReplayTheirEffortChanges(t *testing.T) {
	retry := prepareEffortSteeringRequest(t, Client{}, "max", modelcontext.Bundle{
		Messages:                []modelcontext.Message{steeringInput(1, "hi")},
		OutputNoProgressRetries: 1,
	})
	later := prepareEffortSteeringRequest(t, Client{}, "max", modelcontext.Bundle{
		Messages: []modelcontext.Message{
			steeringInput(1, "hi"), steeringOutput(2, 1), steeringInput(3, "again"),
		},
		OutputNoProgressRetries: 2,
	})
	require.Len(t, later.Messages, 5)
	for index, sent := range retry.Messages {
		require.Equal(t, sent.Role, later.Messages[index].Role)
		require.Equal(t, sent.OutputConfig, later.Messages[index].OutputConfig,
			"later requests must keep the retry's effort change in place")
	}
	require.Contains(t, string(later.Messages[0].Content), modelcontext.OutputNoProgressNotice)
	require.Equal(t, map[string]string{"effort": "xhigh"}, later.Messages[1].OutputConfig)
	require.Equal(t, "assistant", later.Messages[2].Role)
	require.Equal(t, "user", later.Messages[3].Role)
	require.Contains(t, string(later.Messages[3].Content), "again")
	require.Contains(t, string(later.Messages[3].Content), modelcontext.OutputNoProgressNotice)
	require.Equal(t, map[string]string{"effort": "medium"}, later.Messages[4].OutputConfig,
		"later runaways lower from the effort already in force")
}

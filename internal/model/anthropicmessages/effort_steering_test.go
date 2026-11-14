package anthropicmessages

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
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
		{name: "options-supplied low is never raised", retries: 1, client: Client{
			ProviderModelSlug: "claude-fable-5-2", APIVariantOptions: json.RawMessage(`{"output_config":{"effort":"low"}}`),
		}},
		{name: "options-supplied effort", retries: 1, wantEffort: "low", client: Client{
			ProviderModelSlug: "claude-fable-5-2", APIVariantOptions: json.RawMessage(`{"output_config":{"effort":"medium"}}`),
		}},
		{name: "policy effort wins over options", effort: "max", retries: 1, wantEffort: "xhigh", client: Client{
			APIVariantOptions: json.RawMessage(`{"output_config":{"effort":"low"}}`),
		}},
		{name: "first attempt", effort: "high"},
		{name: "already lowest", effort: "low", retries: 1},
		{name: "model without per-message effort", client: Client{ProviderModelSlug: "claude-fable-5"},
			effort: "high", retries: 1},
		{name: "older claude", client: Client{ProviderModelSlug: "claude-opus-4-8"}, effort: "high", retries: 1},
		{name: "openrouter", effort: "high", retries: 1, client: Client{
			APIVariant: modelprotocol.APIVariantOpenRouter, ProviderModelSlug: "anthropic/claude-opus-5.5",
		}},
		{name: "openrouter base url without update support", effort: "high", retries: 1, client: Client{
			BaseURL: "https://openrouter.ai/api/v1", ProviderModelSlug: "anthropic/claude-opus-5.5",
		}},
		{name: "openrouter base url with update support", effort: "high", retries: 1, wantEffort: "medium",
			client: Client{BaseURL: "https://openrouter.ai/api/v1", ProviderModelSlug: "anthropic/claude-opus-5"}},
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
	require.Len(t, later.Messages, 6)
	for index, sent := range retry.Messages {
		require.Equal(t, sent.Role, later.Messages[index].Role)
		require.Equal(t, sent.OutputConfig, later.Messages[index].OutputConfig,
			"later requests must keep the retry's effort change in place")
	}
	require.Contains(t, string(later.Messages[0].Content), modelcontext.OutputNoProgressNotice)
	require.Equal(t, map[string]string{"effort": "xhigh"}, later.Messages[1].OutputConfig)
	require.Equal(t, "assistant", later.Messages[2].Role)
	require.Equal(t, "system", later.Messages[3].Role)
	require.Equal(t, map[string]string{"effort": "max"}, later.Messages[3].OutputConfig,
		"the configured effort is restored after the steered reply")
	require.Equal(t, "user", later.Messages[4].Role)
	require.Contains(t, string(later.Messages[4].Content), "again")
	require.Contains(t, string(later.Messages[4].Content), modelcontext.OutputNoProgressNotice)
	require.Equal(t, map[string]string{"effort": "high"}, later.Messages[5].OutputConfig,
		"later runaways lower from the configured effort")
}

func TestEffortRestoreFollowsToolResults(t *testing.T) {
	output := messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_1"), 2)
	output.OutputNoProgressRetries = 1
	payload := prepareEffortSteeringRequest(t, Client{}, "high", modelcontext.Bundle{
		Messages: []modelcontext.Message{steeringInput(1, "hi"), output},
		ToolResults: []modelcontext.ToolResultRef{{
			SourceEventSequence: 2, ResultEventSequence: 3, ToolCallID: "tcl_1", ModelCallContextID: "mcc_1",
			ProviderCallID: "call_1", Name: "lookup", Input: json.RawMessage(`{}`),
			ContentParts: json.RawMessage(`[{"type":"text","text":"found"}]`),
		}},
	})
	roles := make([]string, 0, len(payload.Messages))
	for _, message := range payload.Messages {
		roles = append(roles, message.Role)
	}
	require.Equal(t, []string{"user", "system", "assistant", "user", "system"}, roles)
	require.Equal(t, map[string]string{"effort": "medium"}, payload.Messages[1].OutputConfig)
	require.Contains(t, string(payload.Messages[3].Content), "tool_result")
	require.Equal(t, map[string]string{"effort": "high"}, payload.Messages[4].OutputConfig)
}

func TestReplayedReductionNeedsTheSameConfigAndModel(t *testing.T) {
	output := steeringOutput(2, 1)
	output.ConfiguredModelRevisionID = uuid.New()
	payload := prepareEffortSteeringRequest(t, Client{}, "high", modelcontext.Bundle{
		Messages:                  []modelcontext.Message{steeringInput(1, "hi"), output, steeringInput(3, "again")},
		ConfiguredModelRevisionID: uuid.New(),
	})
	require.Len(t, payload.Messages, 3)
	for _, message := range payload.Messages {
		require.NotEqual(t, "system", message.Role)
	}
	require.Contains(t, string(payload.Messages[0].Content), modelcontext.OutputNoProgressNotice,
		"the notice was sent, so it still replays")
}

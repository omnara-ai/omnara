package model_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestProviderBaselineCountsInputArrivingDuringGeneration(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100000, DefaultMaxOutputTokens: 1000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(tc.client)
			concurrent := modelcontext.Message{
				Role: modelprotocol.RoleUser, Sequence: 2,
				Content: json.RawMessage(`[{"type":"text","text":"` + strings.Repeat("new detail ", 1000) + `"}]`),
			}
			bundle.Messages = append(bundle.Messages[:1], append([]modelcontext.Message{concurrent}, bundle.Messages[1:]...)...)
			input := model.PrepareForSendInput{Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps)}
			prepared, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.True(t, prepared.UsesProviderInputUsage)
			require.InDelta(t, 3750, prepared.InputTokenEstimate, 150,
				"the 1000-token input baseline must include the later input, even though it precedes the assistant event")
			require.Contains(t, string(prepared.Body), "new detail")

			for _, changed := range []struct {
				name string
				edit func(*modelcontext.Bundle)
			}{
				{"agent config", func(b *modelcontext.Bundle) { b.AgentConfigID = uuid.New() }},
				{"model revision", func(b *modelcontext.Bundle) { b.ConfiguredModelRevisionID = uuid.New() }},
				{"new checkpoint", func(b *modelcontext.Bundle) {
					b.ContextCheckpoint = &modelcontext.CheckpointRef{Sequence: 4, Summary: "compacted"}
				}},
			} {
				t.Run(changed.name, func(t *testing.T) {
					input.Context = bundle
					changed.edit(&input.Context)
					prepared, err := model.PrepareForSend(context.Background(), tc.client, input)
					require.NoError(t, err)
					require.False(t, prepared.UsesProviderInputUsage)
				})
			}
		})
	}
}

func TestProviderBaselineCountsReplayedReasoningWithoutCountingCiphertext(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100000, DefaultMaxOutputTokens: 1000, SupportsReasoning: true}
	providerID := uuid.New().String()
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, modelProviderConfigID: providerID}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(tc.client)
			message := &bundle.Messages[1]
			message.Content = json.RawMessage(`[{"type":"reasoning","text":"thinking"},{"type":"text","text":"answer"}]`)
			message.Usage.OutputTokens = 5000
			message.Usage.ReasoningTokens = 4990
			message.ProviderReplaySource.ModelProviderConfigID = providerID
			opaque := strings.Repeat("opaque", 10000)
			switch tc.client.APIFormat() {
			case modelprotocol.APIFormatOpenAIChatCompletions:
				message.ProviderReplay = json.RawMessage(`{"role":"assistant","content":"answer","reasoning":"thinking",` +
					`"reasoning_details":[{"type":"reasoning.encrypted","data":"` + opaque + `"}]}`)
			case modelprotocol.APIFormatOpenAIResponses:
				message.ProviderReplay = json.RawMessage(`[{"type":"reasoning","encrypted_content":"` + opaque + `",` +
					`"summary":[{"type":"summary_text","text":"thinking"}]},` +
					`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]`)
			case modelprotocol.APIFormatAnthropicMessages:
				message.ProviderReplay = json.RawMessage(`[{"type":"thinking","thinking":"thinking","signature":"` + opaque + `"},` +
					`{"type":"text","text":"answer"}]`)
			}
			bundle.Messages = append(bundle.Messages, modelcontext.Message{
				Role: modelprotocol.RoleUser, Sequence: 4, Content: json.RawMessage(`[{"type":"text","text":"next"}]`),
			})
			input := model.PrepareForSendInput{Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps)}
			prepared, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.Contains(t, string(prepared.Body), opaque, "fixture must retain reasoning replay")
			require.True(t, prepared.UsesProviderInputUsage)
			require.InDelta(t, 6000, prepared.InputTokenEstimate, 100)

			input.Policy.ProviderReplayCutoffEventSequence = 3
			prepared, err = model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.NotContains(t, string(prepared.Body), opaque)
			require.True(t, prepared.UsesProviderInputUsage)
			require.InDelta(t, 1000, prepared.InputTokenEstimate, 200,
				"hidden output is not charged to input when replay is removed")
		})
	}
}

func measuredInputBundle(client model.Client) modelcontext.Bundle {
	configID, revisionID := uuid.New(), uuid.New()
	return modelcontext.Bundle{
		AgentConfigID: configID, ConfiguredModelRevisionID: revisionID,
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 1, Content: json.RawMessage(`[{"type":"text","text":"question"}]`)},
			{
				Role: modelprotocol.RoleAssistant, Sequence: 3, InputEventSequence: 1,
				AgentConfigID: configID, ConfiguredModelRevisionID: revisionID,
				Content:              json.RawMessage(`[{"type":"text","text":"answer"}]`),
				Usage:                modelenvelope.Usage{InputTokens: 1000, OutputTokens: 10},
				ProviderReplaySource: model.ProviderReplayIdentityForClient("", client),
			},
		},
	}
}

func TestProviderBaselineCountsNewlyLoadedDeferredSchema(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100000, DefaultMaxOutputTokens: 1000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(tc.client)
			bundle.Messages[1].ModelCallContextID = "search-context"
			bundle.Messages[1].Content = json.RawMessage(`[{"type":"tool_call","tool_call_id":"search-call"}]`)
			bundle.ToolSpecs = []modelcontext.ToolSpec{
				{Name: "tool_search", InputSchema: json.RawMessage(`{"type":"object"}`)},
				{Name: "get_weather", Deferred: true, InputSchema: json.RawMessage(`{"type":"object"}`)},
				{Name: "unloaded", Deferred: true, Description: strings.Repeat("unused ", 10000),
					InputSchema: json.RawMessage(`{"type":"object"}`)},
			}
			bundle.ToolResults = []modelcontext.ToolResultRef{{
				ToolCallID: "search-call", ModelCallContextID: "search-context", ProviderCallID: "search",
				SourceEventSequence: 3, ResultEventSequence: 4, Name: "tool_search", Outcome: "succeeded",
				Input: json.RawMessage(`{"pattern":"weather"}`),
			}}
			prepare := func(description string) model.PreparedRequest {
				t.Helper()
				bundle.ToolSpecs[1].Description = description
				result, err := modelcontext.SearchDeferredTools(bundle.ToolSpecs, "weather", 1)
				require.NoError(t, err)
				bundle.ToolResults[0].ContentParts, err = json.Marshal([]any{
					map[string]any{"type": "structured_data", "value": result},
				})
				require.NoError(t, err)
				prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
					Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
				})
				require.NoError(t, err)
				require.True(t, prepared.UsesProviderInputUsage)
				return prepared
			}
			small := prepare("Get weather.")
			large := prepare(strings.Repeat("schema detail ", 2000))
			require.Less(t, small.InputTokenEstimate, 1500, "unused deferred schemas stay out of the estimate")
			require.InDelta(t, 7000, large.InputTokenEstimate-small.InputTokenEstimate, 100)
		})
	}
}

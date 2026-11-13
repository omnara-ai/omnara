package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestProviderBaselineLocallyPricesUnknownReasoningAndCancelsMeasuredPrefix(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, modelProviderConfigID: providerID}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(t, tc.client)
			setReasoningReplay(&bundle.Messages[1], tc.client, providerID, strings.Repeat("opaque", 20_000))
			bundle.Messages[1].Usage.OutputTokens = 0
			bundle.InputEventSequence = 3
			policy := model.RequestPolicyFromCapabilities(caps)
			previous, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
				Context: bundle, Policy: policy,
			})
			require.NoError(t, err)
			require.True(t, previous.UsesProviderInputUsage)
			require.Greater(t, previous.InputTokenEstimate, 20_000, "unknown replay is still locally priced")
			bundle.Messages = append(bundle.Messages, modelcontext.Message{
				Role: modelprotocol.RoleAssistant, Sequence: 5, InputEventSequence: 3,
				AgentConfigID: bundle.AgentConfigID, ConfiguredModelRevisionID: bundle.ConfiguredModelRevisionID,
				Content: json.RawMessage(`[]`), StopReason: modelenvelope.StopReasonEndTurn,
				InputFingerprint: previous.InputFingerprint, Usage: modelenvelope.Usage{InputTokens: 2_000},
				ProviderReplaySource: model.ProviderReplayIdentityForClient(providerID, tc.client),
			})
			bundle.InputEventSequence = 5
			prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
				Context: bundle, Policy: policy,
			})
			require.NoError(t, err)
			require.True(t, prepared.UsesProviderInputUsage, "the provider measured the older unknown replay")
			require.Equal(t, 2_000, prepared.InputTokenEstimate)

			setReasoningReplay(&bundle.Messages[2], tc.client, providerID, strings.Repeat("new-unknown", 2_000))
			prepared, err = model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
				Context: bundle, Policy: policy,
			})
			require.NoError(t, err)
			require.True(t, prepared.UsesProviderInputUsage, "unknown new reasoning does not invalidate measured prefix usage")
			require.InDelta(t, 7_500, prepared.InputTokenEstimate, 200, "new unknown reasoning remains locally priced")
		})
	}
}

func TestFallbackPricesActualReasoningReplayByUsage(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, modelProviderConfigID: providerID}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(t, tc.client)
			bundle.AgentConfigID = uuid.Nil // No eligible measured input: exercise fallback.
			bundle.Messages[1].Usage.OutputTokens = 500
			prepare := func(opaque string) model.PreparedRequest {
				t.Helper()
				setReasoningReplay(&bundle.Messages[1], tc.client, providerID, opaque)
				prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
					Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
				})
				require.NoError(t, err)
				require.False(t, prepared.UsesProviderInputUsage)
				require.Contains(t, string(prepared.Body), opaque)
				return prepared
			}
			small := prepare("opaque-short")
			large := prepare(strings.Repeat("opaque", 20_000))
			require.Equal(t, small.InputTokenEstimate, large.InputTokenEstimate)
			require.InDelta(t, 500, large.InputTokenEstimate, 300)
		})
	}
}

func TestProviderBaselineAllowsReplacingSyntheticInputWithShorterContent(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		if tc.client.APIFormat() == modelprotocol.APIFormatOpenAIResponses {
			continue // Responses permits empty input and does not insert Continue.
		}
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(t, tc.client)
			bundle.Messages[0].Content = json.RawMessage(`[]`)
			bundle.Messages[1].Content = json.RawMessage(`[]`)
			policy := model.RequestPolicy{MaxOutputTokens: 1_000, CacheRetention: model.CacheRetentionNone}
			prefix := bundle
			prefix.Messages = prefix.Messages[:1]
			previous, err := tc.client.Prepare(t.Context(), model.PrepareInput{Context: prefix, Policy: policy})
			require.NoError(t, err)
			require.Contains(t, string(previous.Body), "Continue.")
			bundle.Messages[1].InputFingerprint = previous.InputFingerprint
			bundle.Messages = append(bundle.Messages, modelcontext.Message{
				Role: modelprotocol.RoleUser, Sequence: 4, Content: json.RawMessage(`[{"type":"text","text":"x"}]`),
			})
			bundle.InputEventSequence = 4
			prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
				Context: bundle, Policy: policy,
			})
			require.NoError(t, err)
			require.NotContains(t, string(prepared.Body), "Continue.")
			require.True(t, prepared.UsesProviderInputUsage)
			require.Equal(t, 998, prepared.InputTokenEstimate, "the shorter rendered input has a signed negative delta")
		})
	}
}

func TestResponsesProviderOnlyResultsStayOutsideOutputUsage(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	client := openairesponses.Client{
		ModelProviderConfigID: providerID, ProviderModelSlug: "test-model",
		EndpointPath: "/responses", ModelCapabilities: caps,
	}
	for _, kind := range []string{"function_call_output", "tool_search_output"} {
		t.Run(kind, func(t *testing.T) {
			prepare := func(output string) int {
				t.Helper()
				bundle := measuredInputBundle(t, client)
				setReasoningReplay(&bundle.Messages[1], client, providerID, strings.Repeat("opaque", 2_000))
				bundle.Messages[1].Usage.OutputTokens = 500
				var replay []json.RawMessage
				require.NoError(t, json.Unmarshal(bundle.Messages[1].ProviderReplay, &replay))
				item, err := json.Marshal(map[string]any{
					"type": kind, "id": "server-result", "call_id": "server-call", "output": output,
				})
				require.NoError(t, err)
				replay = append(replay, item)
				bundle.Messages[1].ProviderReplay, err = json.Marshal(replay)
				require.NoError(t, err)
				prepared, err := model.PrepareForSend(t.Context(), client, model.PrepareForSendInput{
					Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
				})
				require.NoError(t, err)
				require.True(t, prepared.UsesProviderInputUsage)
				require.Contains(t, string(prepared.Body), "server-result", "actual replay must be admitted")
				return prepared.InputTokenEstimate
			}
			require.InDelta(t, 4_000, prepare(strings.Repeat("data", 4_000))-prepare(""), 1)
		})
	}
}

func TestUnknownReasoningDoesNotDiscardKnownPrefixBudget(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 20_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, modelProviderConfigID: providerID}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(t, tc.client)
			bundle.Messages[1].Usage = modelenvelope.Usage{InputTokens: 19_000}
			bundle.Messages[1].StopReason = modelenvelope.StopReasonEndTurn
			setReasoningReplay(&bundle.Messages[1], tc.client, providerID, strings.Repeat("opaque", 1_333))
			setMeasuredInputFingerprint(t, tc.client, &bundle)
			bundle.InputEventSequence = bundle.Messages[1].Sequence
			policy := model.RequestPolicyFromCapabilities(caps)
			local, err := tc.client.Prepare(t.Context(), model.PrepareInput{Context: bundle, Policy: policy})
			require.NoError(t, err)
			prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
				Context: bundle, Policy: policy,
			})
			require.NoError(t, err)
			require.Less(t, local.InputTokenEstimate, prepared.InputBudget.UsableInputTokens,
				"the local fallback alone would admit this request")
			require.True(t, prepared.UsesProviderInputUsage)
			require.Greater(t, prepared.InputTokenEstimate, 19_000,
				"unknown output remains locally priced on top of the measured input")
			require.True(t, prepared.InputBudget.OverBudget(),
				"missing output usage must not erase the known cost of the unchanged measured prefix")
		})
	}
}

func TestProviderBaselineSurvivesSuccessfulOutputWithoutCompatibleMeasurement(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		for _, changed := range []struct {
			name string
			edit func(*modelcontext.Message)
		}{
			{"missing input usage", func(m *modelcontext.Message) { m.Usage = modelenvelope.Usage{} }},
			{"missing fingerprint", func(m *modelcontext.Message) { m.InputFingerprint = "" }},
			{"different model", func(m *modelcontext.Message) { m.ProviderReplaySource.RequestedProviderModelSlug = "other" }},
			{"different agent config", func(m *modelcontext.Message) { m.AgentConfigID = uuid.New() }},
		} {
			t.Run(tc.name+"/"+changed.name, func(t *testing.T) {
				bundle := measuredInputBundle(t, tc.client)
				policy := model.RequestPolicyFromCapabilities(caps)
				bundle.InputEventSequence = 3
				previous, err := tc.client.Prepare(t.Context(), model.PrepareInput{Context: bundle, Policy: policy})
				require.NoError(t, err)
				latest := modelcontext.Message{
					Role: modelprotocol.RoleAssistant, Sequence: 5, InputEventSequence: 3,
					AgentConfigID: bundle.AgentConfigID, ConfiguredModelRevisionID: bundle.ConfiguredModelRevisionID,
					StopReason: modelenvelope.StopReasonEndTurn, InputFingerprint: previous.InputFingerprint,
					Usage:                modelenvelope.Usage{InputTokens: 7_000, OutputTokens: 10},
					Content:              json.RawMessage(`[{"type":"text","text":"` + strings.Repeat("later answer ", 400) + `"}]`),
					ProviderReplaySource: model.ProviderReplayIdentityForClient("", tc.client),
				}
				changed.edit(&latest)
				bundle.Messages = append(bundle.Messages, latest)
				bundle.InputEventSequence = 5
				prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
					Context: bundle, Policy: policy,
				})
				require.NoError(t, err)
				require.True(t, prepared.UsesProviderInputUsage, "the earlier compatible measured prefix is unchanged")
				require.InDelta(t, 2_300, prepared.InputTokenEstimate, 100,
					"reuse the older 1000-token measurement and count the later successful output locally")
			})
		}
	}
}

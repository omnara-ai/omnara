package model_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestTextOnlyReplayPricesVisibleInputInsteadOfBilledOutput(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, modelProviderConfigID: providerID}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(t, tc.client)
			bundle.InputEventSequence = 3
			source := &bundle.Messages[1]
			source.StopReason = modelenvelope.StopReasonEndTurn
			source.ProviderReplaySource = model.ProviderReplayIdentityForClient(providerID, tc.client)
			setReplay := func(text string) string {
				t.Helper()
				content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
				require.NoError(t, err)
				source.Content = content
				var replay any
				var replayDetail string
				switch tc.client.APIFormat() {
				case modelprotocol.APIFormatOpenAIChatCompletions:
					replay = map[string]any{"role": "assistant", "content": json.RawMessage(content)}
					replayDetail = `"content":[{"text":`
				case modelprotocol.APIFormatOpenAIResponses:
					replay = []any{map[string]any{
						"type": "message", "id": "text-replay", "role": "assistant",
						"content": []any{map[string]string{"type": "output_text", "text": text}},
					}}
					replayDetail = `"id":"text-replay"`
				case modelprotocol.APIFormatAnthropicMessages:
					replay = []any{map[string]any{"type": "text", "text": text, "citations": []any{}}}
					replayDetail = `"citations":[]`
				}
				source.ProviderReplay, err = json.Marshal(replay)
				require.NoError(t, err)
				return replayDetail
			}
			prepare := func(outputTokens, reasoningTokens int, text string) model.PreparedRequest {
				t.Helper()
				source.Usage = modelenvelope.Usage{InputTokens: 1_000, OutputTokens: outputTokens, ReasoningTokens: reasoningTokens}
				replayDetail := setReplay(text)
				prepared, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
					Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
				})
				require.NoError(t, err)
				require.True(t, prepared.UsesProviderInputUsage)
				require.Contains(t, string(prepared.Body), replayDetail,
					"provider-specific content must survive: canonical fallback would hide a replay-accounting bug")
				return prepared
			}
			ordinary := prepare(10, 0, "answer")
			billedHiddenReasoning := prepare(50_000, 49_990, "answer")
			require.Equal(t, ordinary.Body, billedHiddenReasoning.Body)
			require.Equal(t, ordinary.InputTokenEstimate, billedHiddenReasoning.InputTokenEstimate,
				"billed reasoning absent from replay is not input to the next request")
			visibleGrowth := prepare(50_000, 49_990, "answer"+strings.Repeat("visible ", 500))
			require.InDelta(t, 1_000, visibleGrowth.InputTokenEstimate-billedHiddenReasoning.InputTokenEstimate, 1,
				"the text actually replayed remains locally priced")
		})
	}
}

func TestProviderBaselineReconstructionDoesNotAuthenticate(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100_000, DefaultMaxOutputTokens: 1_000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		t.Run(tc.name, func(t *testing.T) {
			auth := &inputMeasurementAuthCounter{}
			var client model.Client
			switch c := tc.client.(type) {
			case openaichatcompletions.Client:
				c.Auth = auth
				client = c
			case openairesponses.Client:
				c.Auth = auth
				client = c
			case anthropicmessages.Client:
				c.Auth = auth
				client = c
			default:
				t.Fatalf("unsupported adapter %T", tc.client)
			}
			bundle := measuredInputBundle(t, client)
			bundle.Messages = append(bundle.Messages, modelcontext.Message{
				Role: modelprotocol.RoleUser, Sequence: 4,
				Content: json.RawMessage(`[{"type":"text","text":"next question"}]`),
			})
			bundle.InputEventSequence = 4
			auth.calls = 0
			prepared, err := model.PrepareForSend(t.Context(), client, model.PrepareForSendInput{
				Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps),
			})
			require.NoError(t, err)
			require.True(t, prepared.UsesProviderInputUsage, "the measured prefix must actually be reconstructed")
			require.Equal(t, caps.DefaultMaxOutputTokens, prepared.MaxOutputTokens,
				"the fixture needs no output-limit reprepare")
			require.Equal(t, 1, auth.calls, "only preparation of the request to send may consult credentials")
		})
	}
}

type inputMeasurementAuthCounter struct{ calls int }

func (a *inputMeasurementAuthCounter) Apply(*http.Request) error {
	a.calls++
	return nil
}

package model_test

import (
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

func TestProviderBaselineSurvivesFailedOutputsWithOpaqueReplay(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 20_000, DefaultMaxOutputTokens: 1_000, SupportsReasoning: true}
	providerID := uuid.New().String()
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, modelProviderConfigID: providerID}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := measuredInputBundle(t, tc.client)
			bundle.ContextCheckpoint = &modelcontext.CheckpointRef{Sequence: 10, Summary: "Earlier work."}
			bundle.Messages[0].Sequence = 11
			bundle.Messages[1].Sequence = 13
			bundle.Messages[1].InputEventSequence = 11
			setMeasuredInputFingerprint(t, tc.client, &bundle)
			oldReplay := strings.Repeat("opaque", 20_000)
			setReasoningReplay(&bundle.Messages[1], tc.client, providerID, oldReplay)
			bundle.InputEventSequence = 13
			policy := model.RequestPolicyFromCapabilities(caps)
			previous, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
				Context: bundle, Policy: policy,
			})
			require.NoError(t, err)
			bundle.Messages = append(bundle.Messages,
				modelcontext.Message{
					Role: modelprotocol.RoleUser, Sequence: 14,
					Content: json.RawMessage(`[{"type":"text","text":"` + strings.Repeat("concurrent ", 400) + `"}]`),
				},
				modelcontext.Message{
					Role: modelprotocol.RoleAssistant, Sequence: 15, InputEventSequence: 13,
					AgentConfigID: bundle.AgentConfigID, ConfiguredModelRevisionID: bundle.ConfiguredModelRevisionID,
					InputFingerprint: previous.InputFingerprint,
					Usage:            modelenvelope.Usage{InputTokens: 4_000, OutputTokens: 500},
					StopReason:       modelenvelope.StopReasonEndTurn,
				},
			)
			setReasoningReplay(&bundle.Messages[3], tc.client, providerID, strings.Repeat("recent", 2_000))
			bundle.InputEventSequence = 15
			before, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
				Context: bundle, Policy: policy,
			})
			require.NoError(t, err)
			require.True(t, before.UsesProviderInputUsage)
			require.InDelta(t, 5_600, before.InputTokenEstimate, 150, "concurrent input is outside the measured prefix")

			for attempt := range 3 {
				sequence := int64(16 + attempt*2)
				bundle.Messages = append(bundle.Messages, modelcontext.Message{
					Role: modelprotocol.RoleAssistant, Sequence: sequence, StopReason: modelenvelope.StopReasonError,
					Content: json.RawMessage(`[{"type":"error","text":"` + strings.Repeat("failed attempt ", 100) + `"}]`),
				}, modelcontext.Message{
					Role: modelprotocol.RoleUser, Sequence: sequence + 1,
					Content: json.RawMessage(`[{"type":"text","text":"` + strings.Repeat("new input ", 100) + `"}]`),
				})
				if attempt == 1 {
					bundle.Messages[len(bundle.Messages)-2].Usage = modelenvelope.Usage{InputTokens: 90_000}
				}
				bundle.InputEventSequence = sequence + 1
				after, err := model.PrepareForSend(t.Context(), tc.client, model.PrepareForSendInput{
					Context: bundle, Policy: policy,
				})
				require.NoError(t, err)
				require.True(t, after.UsesProviderInputUsage)
				require.True(t, after.InputBudget.Fits())
				require.Greater(t, modelcontext.EstimatePreparedRequest(after.Body, after.RenderedMedia), caps.ContextWindowTokens)
				require.InDelta(t, 625, after.InputTokenEstimate-before.InputTokenEstimate, 100,
					"each failed output and later input must be charged, including adjacent assistant messages")
				require.Contains(t, string(after.Body), oldReplay, "estimation must preserve provider replay")
				require.Contains(t, string(after.Body), "failed attempt")
				before = after
			}

			for _, changed := range []struct {
				name string
				edit func(*modelcontext.Bundle, *model.RequestPolicy)
			}{
				{"agent config", func(b *modelcontext.Bundle, _ *model.RequestPolicy) { b.AgentConfigID = uuid.New() }},
				{"model revision", func(b *modelcontext.Bundle, _ *model.RequestPolicy) {
					b.ConfiguredModelRevisionID = uuid.New()
				}},
				{"system prompt", func(b *modelcontext.Bundle, _ *model.RequestPolicy) { b.SystemPrompt = "changed instructions" }},
				{"prior input", func(b *modelcontext.Bundle, _ *model.RequestPolicy) {
					b.Messages[0].Content = json.RawMessage(`[{"type":"text","text":"edited question"}]`)
				}},
				{"model identity", func(b *modelcontext.Bundle, _ *model.RequestPolicy) {
					b.Messages[3].ProviderReplaySource.RequestedProviderModelSlug = "different-model"
				}},
				{"missing fingerprint", func(b *modelcontext.Bundle, _ *model.RequestPolicy) {
					b.Messages[3].InputFingerprint = ""
				}},
				{"missing input usage", func(b *modelcontext.Bundle, _ *model.RequestPolicy) {
					b.Messages[3].Usage = modelenvelope.Usage{}
				}},
				{"checkpoint", func(b *modelcontext.Bundle, _ *model.RequestPolicy) {
					b.ContextCheckpoint = &modelcontext.CheckpointRef{Sequence: 14, Summary: "compacted"}
				}},
				{"replay policy", func(_ *modelcontext.Bundle, p *model.RequestPolicy) {
					p.ProviderReplayCutoffEventSequence = 13
				}},
				{"missing output usage", func(b *modelcontext.Bundle, _ *model.RequestPolicy) {
					b.Messages[3].Usage.OutputTokens = 0
				}},
			} {
				t.Run(changed.name, func(t *testing.T) {
					input := model.PrepareForSendInput{Context: bundle, Policy: policy}
					input.Context.Messages = append([]modelcontext.Message(nil), bundle.Messages...)
					changed.edit(&input.Context, &input.Policy)
					prepared, err := model.PrepareForSend(t.Context(), tc.client, input)
					require.NoError(t, err)
					require.False(t, prepared.UsesProviderInputUsage)
				})
			}

			for _, role := range []modelprotocol.MessageRole{modelprotocol.RoleUser, modelprotocol.RoleAssistant} {
				t.Run("large "+string(role), func(t *testing.T) {
					input := model.PrepareForSendInput{Context: bundle, Policy: policy}
					input.Context.Messages = append(append([]modelcontext.Message(nil), bundle.Messages...), modelcontext.Message{
						Role: role, Sequence: 22, StopReason: modelenvelope.StopReasonError,
						Content: json.RawMessage(`[{"type":"text","text":"` + strings.Repeat("large addition ", 6_000) + `"}]`),
					})
					input.Context.InputEventSequence = 22
					prepared, err := model.PrepareForSend(t.Context(), tc.client, input)
					require.NoError(t, err)
					require.True(t, prepared.UsesProviderInputUsage)
					require.True(t, prepared.InputBudget.OverBudget(), "measured usage must not hide later growth")
				})
			}
		})
	}
}

func setReasoningReplay(message *modelcontext.Message, client model.Client, providerID, opaque string) {
	message.Content = json.RawMessage(`[{"type":"reasoning","text":"thinking"},{"type":"text","text":"answer"}]`)
	message.ProviderReplaySource = model.ProviderReplayIdentityForClient(providerID, client)
	switch client.APIFormat() {
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
}

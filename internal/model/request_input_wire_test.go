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

func TestPreparedAdaptersReuseMeasuredInputBeforeOutputClamping(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 10000, DefaultMaxOutputTokens: 9000}
	clients := adapterWireClients(wireClientConfig{capabilities: caps})
	for _, tc := range clients {
		t.Run(tc.name, func(t *testing.T) {
			bundle := modelcontext.Bundle{
				AgentConfigID: uuid.New(), ConfiguredModelRevisionID: uuid.New(), InputEventSequence: 1,
				SystemPrompt: strings.Repeat("large instructions ", 1500),
				Messages: []modelcontext.Message{{
					ID: "question", Role: modelprotocol.RoleUser, Sequence: 1,
					Content: json.RawMessage(`[{"type":"text","text":"question"}]`),
				}},
			}
			input := model.PrepareForSendInput{
				Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps), ErrorSource: tc.name,
			}
			first, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.True(t, first.InputBudget.OverBudget(), "fixture must overestimate the static input")
			input.Context.InputEventSequence = 4
			input.Context.Messages = append(input.Context.Messages, modelcontext.Message{
				InputFingerprint: first.InputFingerprint,
				ID:               "answer", Role: modelprotocol.RoleAssistant, Sequence: 2,
				InputEventSequence: 1, AgentConfigID: bundle.AgentConfigID,
				ConfiguredModelRevisionID: bundle.ConfiguredModelRevisionID,
				ProviderReplaySource:      model.ProviderReplayIdentityForClient("", tc.client),
				Content:                   json.RawMessage(`[{"type":"text","text":"answer"}]`),
				Usage:                     modelenvelope.Usage{InputTokens: 1000, OutputTokens: 8000, ReasoningTokens: 7990},
				StopReason:                modelenvelope.StopReasonEndTurn,
			}, modelcontext.Message{
				ID: "error", Role: modelprotocol.RoleAssistant, Sequence: 3,
				StopReason: modelenvelope.StopReasonError,
				Content:    json.RawMessage(`[{"type":"error","text":"request failed"}]`),
			}, modelcontext.Message{
				ID: "next", Role: modelprotocol.RoleUser, Sequence: 4,
				Content: json.RawMessage(`[{"type":"text","text":"next"}]`),
			})
			next, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.True(t, next.UsesProviderInputUsage)
			require.InDelta(t, 1000, next.InputTokenEstimate, 200, "unreplayed reasoning is not input")
			require.True(t, next.InputBudget.Fits())
			require.Greater(t, next.MaxOutputTokens, 7500, "measured input must be applied before output clamping")
			require.LessOrEqual(t,
				next.InputTokenEstimate+next.MaxOutputTokens+modelcontext.DefaultSafetyMarginTokens(caps.ContextWindowTokens),
				caps.ContextWindowTokens, "input and output must fit together after applying measured usage")
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(next.Body, &body))
			var sentLimit int
			require.NoError(t, json.Unmarshal(body[tc.field], &sentLimit))
			require.Equal(t, next.MaxOutputTokens, sentLimit)
		})
	}
}

func TestLatestIneligibleAssistantCannotResurrectOlderUsage(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100000, DefaultMaxOutputTokens: 1000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := modelcontext.Bundle{
				AgentConfigID: uuid.New(), ConfiguredModelRevisionID: uuid.New(), InputEventSequence: 3,
				Messages: []modelcontext.Message{{
					ID: "question", Role: modelprotocol.RoleUser, Sequence: 1,
					Content: json.RawMessage(`[{"type":"text","text":"question"}]`),
				}},
			}
			input := model.PrepareForSendInput{Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps)}
			first, err := model.PrepareForSend(t.Context(), tc.client, input)
			require.NoError(t, err)
			input.Context.Messages = append(input.Context.Messages,
				modelcontext.Message{
					InputFingerprint: first.InputFingerprint,
					ID:               "success", Role: modelprotocol.RoleAssistant, Sequence: 2,
					InputEventSequence: 1, AgentConfigID: bundle.AgentConfigID,
					ConfiguredModelRevisionID: bundle.ConfiguredModelRevisionID,
					ProviderReplaySource:      model.ProviderReplayIdentityForClient("", tc.client),
					Content:                   json.RawMessage(`[{"type":"text","text":"success"}]`),
					Usage:                     modelenvelope.Usage{InputTokens: 1000},
				},
				modelcontext.Message{
					ID: "failure", Role: modelprotocol.RoleAssistant, Sequence: 3,
					Content: json.RawMessage(`[{"type":"text","text":"failed attempt"}]`),
					Usage:   modelenvelope.Usage{InputTokens: 5000},
				},
			)
			next, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.False(t, next.UsesProviderInputUsage)

			input.Context.InputEventSequence = 4
			input.Context.Messages = append(input.Context.Messages, modelcontext.Message{
				Role: modelprotocol.RoleAssistant, Sequence: 4, StopReason: modelenvelope.StopReasonError,
				Content: json.RawMessage(`[{"type":"error","text":"retry failed"}]`),
			})
			next, err = model.PrepareForSend(t.Context(), tc.client, input)
			require.NoError(t, err)
			require.False(t, next.UsesProviderInputUsage, "an error cannot make an older ineligible output safe to skip")
		})
	}
}

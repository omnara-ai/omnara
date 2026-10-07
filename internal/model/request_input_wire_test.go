package model_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestPreparedAdaptersReuseMeasuredInputBeforeOutputClamping(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 10000, DefaultMaxOutputTokens: 9000}
	clients := adapterWireClients(wireClientConfig{capabilities: caps, inputIdentityScope: "resolved-route-version"})
	for _, tc := range clients {
		t.Run(tc.name, func(t *testing.T) {
			bundle := modelcontext.Bundle{
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
			require.NotNil(t, first.RequestInputIdentity)
			require.True(t, first.InputBudget.OverBudget(), "fixture must overestimate the static input")
			input.Context.Messages = append(input.Context.Messages, modelcontext.Message{
				ID: "answer", Role: modelprotocol.RoleAssistant, Sequence: 2,
				Content:              json.RawMessage(`[{"type":"text","text":"answer"}]`),
				RequestInputIdentity: first.RequestInputIdentity, ServedProviderModelSlug: "test-model",
				Usage:      modelenvelope.Usage{InputTokens: 1000, OutputTokens: 8000, ReasoningTokens: 7990},
				StopReason: modelenvelope.StopReasonEndTurn,
			}, modelcontext.Message{
				ID: "next", Role: modelprotocol.RoleUser, Sequence: 3,
				Content: json.RawMessage(`[{"type":"text","text":"next"}]`),
			})
			next, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.True(t, next.HasMeasuredInputPrefix)
			require.InDelta(t, 1000, next.InputTokenEstimate, 200, "unreplayed reasoning is not input")
			require.True(t, next.InputBudget.Fits())
			require.Greater(t, next.MaxOutputTokens, 7500, "measured input must be applied before output clamping")
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(next.Body, &body))
			var sentLimit int
			require.NoError(t, json.Unmarshal(body[tc.field], &sentLimit))
			require.Equal(t, next.MaxOutputTokens, sentLimit)
			require.NotNil(t, next.RequestInputIdentity)

			input.Context.SystemPrompt += " changed instructions"
			changed, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.False(t, changed.HasMeasuredInputPrefix)
			require.Greater(t, changed.InputTokenEstimate, next.InputTokenEstimate)
		})
	}
}

func TestLatestIneligibleAssistantCannotResurrectOlderUsage(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 100000, DefaultMaxOutputTokens: 1000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps, inputIdentityScope: "scope"}) {
		t.Run(tc.name, func(t *testing.T) {
			bundle := modelcontext.Bundle{Messages: []modelcontext.Message{{
				ID: "question", Role: modelprotocol.RoleUser, Sequence: 1,
				Content: json.RawMessage(`[{"type":"text","text":"question"}]`),
			}}}
			input := model.PrepareForSendInput{Context: bundle, Policy: model.RequestPolicyFromCapabilities(caps)}
			first, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			input.Context.Messages = append(input.Context.Messages,
				modelcontext.Message{
					ID: "success", Role: modelprotocol.RoleAssistant, Sequence: 2,
					Content:              json.RawMessage(`[{"type":"text","text":"success"}]`),
					RequestInputIdentity: first.RequestInputIdentity, ServedProviderModelSlug: "test-model",
					Usage: modelenvelope.Usage{InputTokens: 1000},
				},
				modelcontext.Message{
					ID: "failure", Role: modelprotocol.RoleAssistant, Sequence: 3,
					Content: json.RawMessage(`[{"type":"text","text":"failed attempt"}]`),
					Usage:   modelenvelope.Usage{InputTokens: 5000},
				},
			)
			next, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.False(t, next.HasMeasuredInputPrefix)
		})
	}
}

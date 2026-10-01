package model_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestOutputAllowanceIsRecomputedForEachProviderRequest(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 10_000, DefaultMaxOutputTokens: 8_192}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		t.Run(tc.name, func(t *testing.T) {
			input := model.PrepareForSendInput{
				Context: modelcontext.Bundle{
					SystemPrompt: strings.Repeat("setup context ", 600),
					Messages: []modelcontext.Message{{
						ID: "current", Role: modelprotocol.RoleUser, Sequence: 1,
						Content: json.RawMessage(`[{"type":"text","text":"CURRENT_REQUEST_UNCHANGED"}]`),
					}},
				},
				Policy: model.RequestPolicyFromCapabilities(caps),
			}
			reduced, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.Less(t, reduced.MaxOutputTokens, caps.DefaultMaxOutputTokens)
			require.True(t, reduced.InputBudget.Fits())
			input.Context.SystemPrompt = "Small setup context."
			smaller, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.Equal(t, caps.DefaultMaxOutputTokens, smaller.MaxOutputTokens)
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(smaller.Body, &body))
			var allowance int
			require.NoError(t, json.Unmarshal(body[tc.field], &allowance))
			require.Equal(t, caps.DefaultMaxOutputTokens, allowance)
			require.Contains(t, string(smaller.Body), "CURRENT_REQUEST_UNCHANGED")
			input.Context.SystemPrompt = strings.Repeat("large setup context ", 5_000)
			oversized, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.True(t, oversized.InputBudget.OverBudget())
			require.Equal(t, caps.DefaultMaxOutputTokens, oversized.MaxOutputTokens)
			require.Contains(t, string(oversized.Body), "CURRENT_REQUEST_UNCHANGED")
		})
	}
}

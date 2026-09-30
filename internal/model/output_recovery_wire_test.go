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

func TestRestoredOutputAllowanceReachesEveryProviderUnchanged(t *testing.T) {
	caps := model.Capabilities{ContextWindowTokens: 10_000, DefaultMaxOutputTokens: 9_000}
	for _, tc := range adapterWireClients(wireClientConfig{capabilities: caps}) {
		t.Run(tc.name, func(t *testing.T) {
			input := model.PrepareForSendInput{
				Context: modelcontext.Bundle{
					SystemPrompt: strings.Repeat("large setup context ", 5_000),
					Messages: []modelcontext.Message{{
						ID: "current", Role: modelprotocol.RoleUser, Sequence: 1,
						Content: json.RawMessage(`[{"type":"text","text":"CURRENT_REQUEST_UNCHANGED"}]`),
					}},
				},
				Policy: model.RequestPolicyFromCapabilities(caps), AllowUncertainInput: true,
			}
			reduced, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.Less(t, reduced.MaxOutputTokens, caps.DefaultMaxOutputTokens)
			input.PreserveOutputAllowance = true
			restored, err := model.PrepareForSend(context.Background(), tc.client, input)
			require.NoError(t, err)
			require.Equal(t, caps.DefaultMaxOutputTokens, restored.MaxOutputTokens)
			require.Equal(t, reduced.InputBudget, restored.InputBudget)
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(restored.Body, &body))
			var allowance int
			require.NoError(t, json.Unmarshal(body[tc.field], &allowance))
			require.Equal(t, caps.DefaultMaxOutputTokens, allowance)
			require.Contains(t, string(restored.Body), "CURRENT_REQUEST_UNCHANGED")
		})
	}
}

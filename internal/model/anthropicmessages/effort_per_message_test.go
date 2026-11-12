package anthropicmessages

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestPrepareAddsFinalTurnEffortChange(t *testing.T) {
	for _, tc := range []struct {
		name       string
		client     Client
		effort     string
		reduction  int
		wantEffort string
	}{
		{name: "one level down", effort: "high", reduction: 1, wantEffort: "medium"},
		{name: "one level per reduction", effort: "max", reduction: 2, wantEffort: "high"},
		{name: "floors at low", effort: "medium", reduction: 5, wantEffort: "low"},
		{name: "model default", effort: "", reduction: 1, wantEffort: "low"},
		{name: "default for fable", client: Client{ProviderModelSlug: "claude-fable-5-2"}, reduction: 1,
			wantEffort: "medium"},
		{name: "supported efforts only", effort: "max", reduction: 1, wantEffort: "high",
			client: Client{ModelCapabilities: model.Capabilities{SupportedReasoningEfforts: []string{"low", "high", "max"}}}},
		{name: "no reduction", effort: "high"},
		{name: "already lowest", effort: "low", reduction: 1},
		{name: "model without per-message effort", client: Client{ProviderModelSlug: "claude-fable-5"},
			effort: "high", reduction: 1},
		{name: "older claude", client: Client{ProviderModelSlug: "claude-opus-4-8"}, effort: "high", reduction: 1},
		{name: "openrouter", effort: "high", reduction: 1, client: Client{
			APIVariant: modelprotocol.APIVariantOpenRouter, ProviderModelSlug: "anthropic/claude-opus-5.5",
		}},
		{name: "bedrock", effort: "high", reduction: 1, client: Client{
			APIVariant: modelprotocol.APIVariantBedrock, ProviderModelSlug: "anthropic.claude-opus-5-5",
		}},
		{name: "thinking between tools", effort: "high", reduction: 1, client: Client{
			ProviderModelSlug: "claude-sonnet-5-5", APIVariantOptions: json.RawMessage(`{"thinking":{"type":"between_tools"}}`),
		}},
		{name: "thinking disabled", reduction: 1, client: Client{
			ProviderModelSlug: "claude-haiku-5-5", APIVariantOptions: json.RawMessage(`{"thinking":{"type":"disabled"}}`),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.client
			client.EndpointPath = testEndpointPath
			if client.ProviderModelSlug == "" {
				client.ProviderModelSlug = "claude-opus-5-5"
			}
			supportsReasoning := true
			prepared, err := client.Prepare(t.Context(), model.PrepareInput{
				Context: modelcontext.Bundle{Messages: []modelcontext.Message{
					anthropicTextMessage(modelprotocol.RoleUser, "hi"),
				}},
				Policy: model.RequestPolicy{
					MaxOutputTokens:          4096,
					SupportsReasoning:        &supportsReasoning,
					ReasoningEffort:          tc.effort,
					FinalTurnEffortReduction: tc.reduction,
				},
			})
			require.NoError(t, err)
			var payload struct {
				Messages     []json.RawMessage          `json:"messages"`
				OutputConfig map[string]json.RawMessage `json:"output_config"`
			}
			require.NoError(t, json.Unmarshal(prepared.Body, &payload))
			if tc.effort != "" {
				require.JSONEq(t, `"`+tc.effort+`"`, string(payload.OutputConfig["effort"]),
					"the top-level effort must stay unchanged")
			}
			last := payload.Messages[len(payload.Messages)-1]
			if tc.wantEffort == "" {
				require.Len(t, payload.Messages, 1)
				require.NotContains(t, string(last), `"system"`)
				return
			}
			require.Len(t, payload.Messages, 2)
			require.JSONEq(t, `{"role":"system","content":[],"output_config":{"effort":"`+tc.wantEffort+`"}}`,
				string(last))
		})
	}
}

func TestClaudePerMessageEffortSupport(t *testing.T) {
	for slug, want := range map[string]bool{
		"claude-fable-5-2":            true,
		"claude-mythos-5-1":           true,
		"claude-opus-5":               true,
		"claude-opus-5-5-20261001":    true,
		"claude-sonnet-5-5":           true,
		"claude-haiku-5-5":            true,
		"claude-fable-5":              false,
		"claude-mythos-5":             false,
		"claude-sonnet-5":             false,
		"claude-opus-4-8":             false,
		"claude-haiku-4-5-20251001":   false,
		"anthropic/claude-opus-5.5":   true,
		"anthropic.claude-sonnet-5-5": true,
		"MiniMax-M3":                  false,
	} {
		require.Equal(t, want, model.ClaudeSupportsPerMessageEffort(slug), slug)
	}
}

package anthropicmessages

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const adaptiveThinkingDroppingPrefixMismatches = `{"block_binding":{"prefix_mismatch_behavior":"drop_block"},` +
	`"type":"adaptive"}`

func prepareThinkingRequest(t *testing.T, client Client, messages []modelcontext.Message) map[string]json.RawMessage {
	t.Helper()
	client.EndpointPath = testEndpointPath
	if client.ProviderModelSlug == "" {
		client.ProviderModelSlug = "claude-opus-5-5"
	}
	supportsReasoning := true
	if len(messages) == 0 {
		messages = []modelcontext.Message{anthropicTextMessage(modelprotocol.RoleUser, "hi")}
	}
	prepared, err := client.Prepare(t.Context(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: messages},
		Policy: model.RequestPolicy{
			MaxOutputTokens:   4096,
			SupportsReasoning: &supportsReasoning,
			ReasoningEffort:   "high",
		},
	})
	require.NoError(t, err)
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	return payload
}

func TestPrepareDropsPrefixMismatchedThinkingOnlyForClaudeModels(t *testing.T) {
	for _, tc := range []struct {
		name         string
		client       Client
		wantThinking string
	}{
		{
			name:         "default base URL",
			wantThinking: adaptiveThinkingDroppingPrefixMismatches,
		},
		{
			name:         "explicit anthropic base URL",
			client:       Client{BaseURL: "https://api.anthropic.com/v1"},
			wantThinking: adaptiveThinkingDroppingPrefixMismatches,
		},
		{
			name: "openrouter claude slug",
			client: Client{
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				ProviderModelSlug: "anthropic/claude-opus-5.5",
			},
			wantThinking: adaptiveThinkingDroppingPrefixMismatches,
		},
		{
			name: "openrouter dated claude slug",
			client: Client{
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				ProviderModelSlug: "anthropic/claude-sonnet-4.5:beta",
			},
			wantThinking: adaptiveThinkingDroppingPrefixMismatches,
		},
		{
			name:         "bedrock claude slug",
			client:       Client{APIVariant: modelprotocol.APIVariantBedrock},
			wantThinking: adaptiveThinkingDroppingPrefixMismatches,
		},
		{
			name:         "claude behind an anthropic-compatible base URL",
			client:       Client{BaseURL: "https://gateway.example.test/anthropic"},
			wantThinking: adaptiveThinkingDroppingPrefixMismatches,
		},
		{
			name: "configured adaptive thinking",
			client: Client{
				APIVariantOptions: json.RawMessage(`{"thinking":{"type":"adaptive","display":"summarized"}}`),
			},
			wantThinking: `{"block_binding":{"prefix_mismatch_behavior":"drop_block"},` +
				`"display":"summarized","type":"adaptive"}`,
		},
		{
			name: "configured block binding",
			client: Client{APIVariantOptions: json.RawMessage(
				`{"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"error"}}}`,
			)},
			wantThinking: `{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"error"}}`,
		},
		{
			name: "openrouter non-claude slug",
			client: Client{
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				ProviderModelSlug: "minimax/minimax-m3.1-flash",
			},
			wantThinking: `{"type":"adaptive"}`,
		},
		{
			name: "non-claude anthropic-compatible provider",
			client: Client{
				BaseURL:           "https://api.minimax.io/anthropic",
				ProviderModelSlug: "MiniMax-M3",
			},
			wantThinking: `{"type":"adaptive"}`,
		},
		{
			name:         "claude-named non-family slug",
			client:       Client{ProviderModelSlug: "my-claude-proxy"},
			wantThinking: `{"type":"adaptive"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := prepareThinkingRequest(t, tc.client, nil)
			require.JSONEq(t, tc.wantThinking, string(payload["thinking"]))
		})
	}
}

func TestThinkingBindingBetaHeaderOnlyForClaudeModels(t *testing.T) {
	const claude = "claude-opus-5-5"
	operatorBetas := route.Headers{"Anthropic-Beta": "context-1m-2025-08-07, interleaved-thinking-2025-05-14"}
	for _, tc := range []struct {
		name     string
		client   Client
		wantBeta string
	}{
		{name: "default base URL", client: Client{ProviderModelSlug: claude}, wantBeta: thinkingBindingControlsBeta},
		{
			name:     "keeps operator betas",
			client:   Client{ProviderModelSlug: claude, Auth: operatorBetas},
			wantBeta: "context-1m-2025-08-07,interleaved-thinking-2025-05-14," + thinkingBindingControlsBeta,
		},
		{
			name: "operator already sends the beta",
			client: Client{ProviderModelSlug: claude, Auth: route.Headers{
				"Anthropic-Beta": thinkingBindingControlsBeta + ",context-1m-2025-08-07",
			}},
			wantBeta: thinkingBindingControlsBeta + ",context-1m-2025-08-07",
		},
		{
			name: "openrouter claude slug",
			client: Client{
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				ProviderModelSlug: "anthropic/claude-opus-5.5",
			},
			wantBeta: thinkingBindingControlsBeta,
		},
		{
			name: "openrouter non-claude slug keeps operator betas untouched",
			client: Client{
				APIVariant:        modelprotocol.APIVariantOpenRouter,
				ProviderModelSlug: "minimax/minimax-m3.1-flash",
				Auth:              operatorBetas,
			},
			wantBeta: "context-1m-2025-08-07, interleaved-thinking-2025-05-14",
		},
		{
			name:   "non-claude anthropic-compatible provider",
			client: Client{BaseURL: "https://api.minimax.io/anthropic", ProviderModelSlug: "MiniMax-M3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.test", nil)
			require.NoError(t, err)
			auth := tc.client.routeClient().Auth
			require.NoError(t, auth.Apply(req))
			require.NoError(t, auth.Apply(req), "auth may apply again before sending")
			require.Equal(t, APIVersion, req.Header.Get("Anthropic-Version"))
			require.Equal(t, tc.wantBeta, req.Header.Get("Anthropic-Beta"))
		})
	}
}

func thinkingReplayConversation(sourceSlug string) []modelcontext.Message {
	answer := modelcontext.Message{
		Role:               modelprotocol.RoleAssistant,
		Sequence:           2,
		ModelCallContextID: "mcc_1",
		Content:            json.RawMessage(`[{"type":"text","text":"answer one"}]`),
		ProviderReplay: json.RawMessage(
			`[{"type":"thinking","thinking":"","signature":"sig_1"},{"type":"text","text":"answer one"}]`,
		),
		ProviderReplaySource: testProviderReplayIdentity(sourceSlug, modelprotocol.APIFormatAnthropicMessages),
	}
	second := anthropicTextMessage(modelprotocol.RoleUser, "second")
	second.Sequence = 3
	return []modelcontext.Message{anthropicTextMessage(modelprotocol.RoleUser, "first"), answer, second}
}

func TestPrepareReplaysThinkingAcrossClaudeModelSwitches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourceSlug string
		targetSlug string
		wantReplay bool
	}{
		{name: "same model", sourceSlug: "claude-opus-5-5", targetSlug: "claude-opus-5-5", wantReplay: true},
		{name: "claude to claude", sourceSlug: "claude-opus-5", targetSlug: "claude-fable-5-1", wantReplay: true},
		{
			name:       "routed claude slugs",
			sourceSlug: "anthropic/claude-sonnet-5-5",
			targetSlug: "anthropic/claude-opus-5-5",
			wantReplay: true,
		},
		{name: "non-claude source", sourceSlug: "minimax-m3", targetSlug: "claude-opus-5-5"},
		{name: "claude-named non-family slug", sourceSlug: "my-claude-proxy", targetSlug: "claude-opus-5-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := prepareThinkingRequest(
				t,
				Client{ModelProviderConfigID: testModelProviderConfigID, ProviderModelSlug: tc.targetSlug},
				thinkingReplayConversation(tc.sourceSlug),
			)
			messages := string(payload["messages"])
			require.Contains(t, messages, "answer one")
			if tc.wantReplay {
				require.Contains(t, messages, `"signature":"sig_1"`)
			} else {
				require.NotContains(t, messages, `"signature":"sig_1"`)
			}
		})
	}
}

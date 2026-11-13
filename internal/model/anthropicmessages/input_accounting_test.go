package anthropicmessages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestInputAccountingUsesUsageOnlyForEmittedReasoning(t *testing.T) {
	client := accountingClient()
	input := model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{
			anthropicTextMessage(modelprotocol.RoleUser, "question"),
			accountingReasoningMessage(3, "short-signature", 100),
		}},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	}
	short, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	input.Context.Messages[1] = accountingReasoningMessage(3, strings.Repeat("opaque-signature", 2_000), 100)
	large, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, short.InputTokenEstimate, large.InputTokenEstimate)
	require.Contains(t, string(large.Body), strings.Repeat("opaque-signature", 2_000))
	require.False(t, large.UsesProviderInputUsage, "replay output accounting also applies without an input measurement")

	input.Context.Messages[1].Usage.OutputTokens = 0
	unknown, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	require.Greater(t, unknown.InputTokenEstimate, large.InputTokenEstimate+5_000)

	for _, tc := range []struct {
		name   string
		change func(*model.PrepareInput)
	}{
		{"cutoff", func(in *model.PrepareInput) { in.Policy.ProviderReplayCutoffEventSequence = 3 }},
		{"incompatible identity", func(in *model.PrepareInput) {
			in.Context.Messages[1].ProviderReplaySource.RequestedProviderModelSlug = "another-model"
		}},
		{"changed content", func(in *model.PrepareInput) {
			in.Context.Messages[1].Content = json.RawMessage(`[{"type":"text","text":"changed"}]`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := input
			candidate.Context.Messages = append([]modelcontext.Message(nil), input.Context.Messages...)
			tc.change(&candidate)
			withoutUsage, err := client.Project(t.Context(), candidate)
			require.NoError(t, err)
			candidate.Context.Messages[1].Usage.OutputTokens = 50_000
			withUsage, err := client.Project(t.Context(), candidate)
			require.NoError(t, err)
			require.Equal(t, withoutUsage.InputTokenEstimate, withUsage.InputTokenEstimate)
			require.NotContains(t, string(withUsage.Body), "opaque-signature")
		})
	}
}

func TestInputAccountingKeepsMergedFailureAndContinuationNotice(t *testing.T) {
	client := accountingClient()
	input := model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{
			anthropicTextMessage(modelprotocol.RoleUser, "question"),
			accountingReasoningMessage(3, strings.Repeat("opaque", 2_000), 100),
		}},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	}
	before, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	failure := messageAtSequence(anthropicTextMessage(modelprotocol.RoleAssistant, strings.Repeat("failure ", 500)), 4)
	failure.StopReason = modelenvelope.StopReasonError
	input.Context.Messages = append(input.Context.Messages, failure)
	after, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	require.InDelta(t, 1_000, after.InputTokenEstimate-before.InputTokenEstimate, 30)
	var payload struct {
		Messages []struct {
			Role    string         `json:"role"`
			Content []contentBlock `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(after.Body, &payload))
	require.Len(t, payload.Messages, 2)
	require.Len(t, payload.Messages[1].Content, 3)
	require.Equal(t, "thinking", payload.Messages[1].Content[0].Type)
	require.Equal(t, "answer", payload.Messages[1].Content[1].Text)
	require.Equal(t, strings.Repeat("failure ", 500), payload.Messages[1].Content[2].Text)

	input.Context.Messages = input.Context.Messages[:2]
	input.Context.Messages[1].StopReason = modelenvelope.StopReasonMaxTokens
	continued, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	require.Contains(t, string(continued.Body), "preceding response reached its output limit")
	require.Greater(t, continued.InputTokenEstimate, before.InputTokenEstimate+30)
}

func TestInputAccountingFollowsReplayAdmissionWithDuplicateToolIDs(t *testing.T) {
	client := accountingClient()
	firstReplay := testProviderReplay("claude-test", modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[{"type":"tool_use","id":"toolu_shared","name":"run_command","input":{}}]`))
	first := messageAtSequence(withToolCallLinks(anthropicReplayMessage("first", firstReplay), "first-call"), 2)
	secondReplay := testProviderReplay("claude-test", modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[{"type":"thinking","thinking":"thought","signature":"second-signature"},{"type":"tool_use","id":"toolu_shared","name":"run_command","input":{}}]`))
	second := messageAtSequence(anthropicReplayMessage("second", secondReplay), 4)
	second.Content = json.RawMessage(`[{"type":"reasoning","text":"thought"},{"type":"tool_call","tool_call_id":"second-call"}]`)
	input := model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{anthropicTextMessage(modelprotocol.RoleUser, "question"), first, second},
			ToolResults: []modelcontext.ToolResultRef{
				{ToolCallID: "first-call", ModelCallContextID: "first", ProviderCallID: "toolu_shared", Name: "run_command",
					Input: json.RawMessage(`{}`), ContentParts: json.RawMessage(`[{"type":"text","text":"first result"}]`)},
				{ToolCallID: "second-call", ModelCallContextID: "second", ProviderCallID: "toolu_shared", Name: "run_command",
					Input: json.RawMessage(`{}`), ContentParts: json.RawMessage(`[{"type":"text","text":"second result"}]`)},
			},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	}
	unknown, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	require.NotContains(t, string(unknown.Body), "second-signature")
	input.Context.Messages[2].Usage.OutputTokens = 10_000
	known, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, unknown.InputTokenEstimate, known.InputTokenEstimate, "unreplayed output is estimated canonically")
	require.JSONEq(t, string(unknown.Body), string(known.Body))

	input.Context.Messages[1].ProviderReplay = nil
	replayed, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	require.Contains(t, string(replayed.Body), "second-signature",
		"the earlier canonical call leaves the provider ID available")
	require.GreaterOrEqual(t, replayed.InputTokenEstimate, 10_000)
}

func TestAccountingProjectionDoesNotChangeMergedWireBlocks(t *testing.T) {
	canonical := messageAtSequence(anthropicTextMessage(modelprotocol.RoleAssistant, "previous"), 2)
	canonical.Content = json.RawMessage(`[{"type":"reasoning","text":"not replayed"},{"type":"text","text":"previous"}]`)
	source := accountingReasoningMessage(3, "signed-thought", 100)
	source.Content = json.RawMessage(`[{"type":"reasoning","text":"thought"}]`)
	source.ProviderReplay = json.RawMessage(`[{"type":"thinking","thinking":"thought","signature":"signed-thought"}]`)
	failure := messageAtSequence(anthropicTextMessage(modelprotocol.RoleAssistant, "failure"), 4)
	failure.StopReason = modelenvelope.StopReasonError
	prepared, err := accountingClient().Project(t.Context(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{
			anthropicTextMessage(modelprotocol.RoleUser, "question"), canonical, source, failure,
		}},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	require.NoError(t, err)
	var payload struct {
		Messages []struct {
			Content []contentBlock `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	require.Len(t, payload.Messages, 2)
	require.Len(t, payload.Messages[1].Content, 3)
	require.Equal(t, "previous", payload.Messages[1].Content[0].Text)
	require.Equal(t, "signed-thought", payload.Messages[1].Content[1].Signature)
	require.Equal(t, "failure", payload.Messages[1].Content[2].Text)
}

func TestInputAccountingPreservesWireCacheBreakpoints(t *testing.T) {
	question := anthropicTextMessage(modelprotocol.RoleUser, "question")
	question.Content = json.RawMessage(`[{"type":"text","text":"question one"},{"type":"text","text":"question two"}]`)
	prepared, err := accountingClient().Project(t.Context(), model.PrepareInput{
		Context: modelcontext.Bundle{
			SystemPrompt: "stable system prompt",
			Messages: []modelcontext.Message{
				question, accountingReasoningMessage(3, "signed-thought", 100),
			},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64, CacheRetention: model.CacheRetentionShort},
	})
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(prepared.Body), `"cache_control"`),
		"only the system prompt and actual trailing assistant block should receive cache breakpoints")
	var payload struct {
		Messages []struct {
			Content []struct {
				CacheControl json.RawMessage `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	require.Len(t, payload.Messages, 2)
	for _, block := range payload.Messages[0].Content {
		require.Empty(t, block.CacheControl, "the accounting projection's final block must not mutate the wire request")
	}
	require.Len(t, payload.Messages[1].Content, 2)
	require.NotEmpty(t, payload.Messages[1].Content[1].CacheControl)
}

func TestProjectPreservesPrepareBodyWithoutAuthenticating(t *testing.T) {
	client := accountingClient()
	client.Auth = route.HeaderAuth{Header: "x-api-key"}
	input := model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{
			anthropicTextMessage(modelprotocol.RoleUser, "question"),
		}},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	}
	projected, err := client.Project(t.Context(), input)
	require.NoError(t, err)
	_, err = client.Prepare(t.Context(), input)
	require.ErrorContains(t, err, "x-api-key is required")
	client.Auth = route.HeaderAuth{Header: "x-api-key", Value: "test-key"}
	prepared, err := client.Prepare(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, prepared.Body, projected.Body)
	require.Equal(t, prepared.InputFingerprint, projected.InputFingerprint)
	require.Equal(t, prepared.InputTokenEstimate, projected.InputTokenEstimate)
}

func accountingClient() Client {
	return Client{
		ModelProviderConfigID: testModelProviderConfigID, EndpointPath: testEndpointPath, ProviderModelSlug: "claude-test",
	}
}

func accountingReasoningMessage(sequence int64, signature string, outputTokens int) modelcontext.Message {
	replay := testProviderReplay("claude-test", modelprotocol.APIFormatAnthropicMessages,
		json.RawMessage(`[{"type":"thinking","thinking":"thought","signature":"`+signature+`"},`+
			`{"type":"text","text":"answer"}]`))
	message := messageAtSequence(anthropicReplayMessage("", replay), sequence)
	message.Content = json.RawMessage(`[{"type":"reasoning","text":"thought"},{"type":"text","text":"answer"}]`)
	message.StopReason = modelenvelope.StopReasonEndTurn
	message.Usage.OutputTokens = outputTokens
	return message
}

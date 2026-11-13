package openaichatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestPrepareUsesOutputUsageOnlyForAcceptedReasoningReplay(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID, EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test",
	}
	for _, tc := range []struct {
		name   string
		edit   func(*model.PrepareInput)
		replay bool
	}{
		{"compatible replay", func(*model.PrepareInput) {}, true},
		{"replay cutoff", func(input *model.PrepareInput) {
			input.Policy.ProviderReplayCutoffEventSequence = 2
		}, false},
		{"different provider", func(input *model.PrepareInput) {
			input.Context.Messages[1].ProviderReplaySource.ModelProviderConfigID = "different-provider"
		}, false},
		{"different canonical content", func(input *model.PrepareInput) {
			input.Context.Messages[1].Content = json.RawMessage(`[{"type":"text","text":"edited answer"}]`)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := chatReasoningAccountingInput()
			tc.edit(&input)
			first, err := client.Prepare(t.Context(), input)
			require.NoError(t, err)
			require.Equal(t, tc.replay, strings.Contains(string(first.Body), "opaqueopaque"))
			require.Less(t, first.InputTokenEstimate, 700)
			input.Context.Messages[1].Usage.OutputTokens += 500
			second, err := client.Prepare(t.Context(), input)
			require.NoError(t, err)
			require.JSONEq(t, string(first.Body), string(second.Body), "accounting must not rewrite the request")
			if tc.replay {
				require.Equal(t, 500, second.InputTokenEstimate-first.InputTokenEstimate)
			} else {
				require.Equal(t, first.InputTokenEstimate, second.InputTokenEstimate)
			}
		})
	}

	t.Run("missing output usage", func(t *testing.T) {
		input := chatReasoningAccountingInput()
		input.Context.Messages[1].Usage = modelenvelope.Usage{}
		prepared, err := client.Prepare(t.Context(), input)
		require.NoError(t, err)
		require.Greater(t, prepared.InputTokenEstimate, 3_000)
		require.Contains(t, string(prepared.Body), "opaqueopaque")
	})
}

func TestPrepareCountsToolResultsAndFailuresOutsideReplayedOutputUsage(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID, EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test",
	}
	input := chatReasoningAccountingInput()
	input.Context.Messages[1].Content = json.RawMessage(`[{"type":"reasoning","text":"thinking"},` +
		`{"type":"tool_call","tool_call_id":"tool-call"}]`)
	input.Context.Messages[1].ModelCallContextID = "tool-context"
	input.Context.Messages[1].StopReason = modelenvelope.StopReasonToolUse
	input.Context.Messages[1].ProviderReplay = json.RawMessage(`{"role":"assistant","reasoning":"thinking",` +
		`"reasoning_details":[{"type":"reasoning.encrypted","data":"` + strings.Repeat("opaque", 2_000) + `"}],` +
		`"tool_calls":[{"type":"function","id":"provider-call","function":{"name":"run_command","arguments":"{}"}}]}`)
	input.Context.ToolResults = []modelcontext.ToolResultRef{{
		ToolCallID: "tool-call", ModelCallContextID: "tool-context", ProviderCallID: "provider-call",
		SourceEventSequence: 2, ResultEventSequence: 3, Name: "run_command", Input: json.RawMessage(`{}`),
		ContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
	}}
	first, err := client.Prepare(t.Context(), input)
	require.NoError(t, err)
	require.Contains(t, string(first.Body), "opaqueopaque")
	require.Less(t, first.InputTokenEstimate, 800)

	input.Context.ToolResults[0].ContentParts = json.RawMessage(
		`[{"type":"text","text":"` + strings.Repeat("result ", 1_000) + `"}]`)
	withResult, err := client.Prepare(t.Context(), input)
	require.NoError(t, err)
	require.InDelta(t, 1_750, withResult.InputTokenEstimate-first.InputTokenEstimate, 5)
	input.Context.Messages = append(input.Context.Messages, modelcontext.Message{
		Role: modelprotocol.RoleAssistant, Sequence: 4, StopReason: modelenvelope.StopReasonError,
		Content: json.RawMessage(`[{"type":"error","text":"` + strings.Repeat("failure ", 1_000) + `"}]`),
	})
	withFailure, err := client.Prepare(t.Context(), input)
	require.NoError(t, err)
	require.InDelta(t, 2_000, withFailure.InputTokenEstimate-withResult.InputTokenEstimate, 20)
	require.Contains(t, string(withFailure.Body), "failure failure")
}

func TestPrepareCountsOutputLimitNoticeOutsideReplayedOutputUsage(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID, EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test",
	}
	input := chatReasoningAccountingInput()
	before, err := client.Prepare(t.Context(), input)
	require.NoError(t, err)
	input.Context.Messages[1].StopReason = modelenvelope.StopReasonMaxTokens
	after, err := client.Prepare(t.Context(), input)
	require.NoError(t, err)
	require.Contains(t, string(after.Body), "preceding response reached its output limit")
	require.Greater(t, after.InputTokenEstimate, before.InputTokenEstimate+40)
}

func chatReasoningAccountingInput() model.PrepareInput {
	replay := testProviderReplay("gpt-test", modelprotocol.APIFormatOpenAIChatCompletions, modelprotocol.APIVariantDefault,
		json.RawMessage(`{"role":"assistant","content":"answer","reasoning":"thinking",`+
			`"reasoning_details":[{"type":"reasoning.encrypted","data":"`+strings.Repeat("opaque", 2_000)+`"}]}`))
	return model.PrepareInput{Context: modelcontext.Bundle{Messages: []modelcontext.Message{
		{Role: modelprotocol.RoleUser, Sequence: 1, Content: json.RawMessage(`[{"type":"text","text":"question"}]`)},
		{
			Role: modelprotocol.RoleAssistant, Sequence: 2, StopReason: modelenvelope.StopReasonEndTurn,
			Content:        json.RawMessage(`[{"type":"reasoning","text":"thinking"},{"type":"text","text":"answer"}]`),
			ProviderReplay: replay.payload, ProviderReplaySource: replay.source,
			Usage: modelenvelope.Usage{OutputTokens: 500},
		},
	}}}
}

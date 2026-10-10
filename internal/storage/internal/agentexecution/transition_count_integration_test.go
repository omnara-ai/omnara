//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestExecutionTransitionStatementBounds(t *testing.T) {
	f, a, lease := commandFixture(t)
	measured := func(name string, run func(*agentexecution.Handle)) {
		u, h := f.handle(t, a)
		statements, facts, err := agentexecution.CountExecutionTransition(t.Context(), u, func() { run(h) })
		require.NoError(t, err)
		t.Logf("%s statements=%d facts=%d", name, statements, facts)
		limits := map[string]int{"receive": 6, "prepare_initial": 13, "accept_tools": 12,
			"authorize": 6, "run_tool": 7, "complete_prepare": 16, "accept_end": 11}
		require.LessOrEqual(t, statements, limits[name], name)
		maximumFacts := 1
		if name == "authorize" {
			maximumFacts = 0
		}
		require.LessOrEqual(t, facts, maximumFacts, name)
	}
	const cycles = 2
	for range cycles {
		var prepared agentexecution.PreparedModel
		var output agentexecution.AcceptedOutput
		measured("receive", func(h *agentexecution.Handle) { receiveCommand(t, h, "queued", "input") })
		measured("prepare_initial", func(h *agentexecution.Handle) {
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			prepared = prepareCommand(t, h, lease)
		})
		measured("accept_tools", func(h *agentexecution.Handle) {
			response := commandResponse(modelenvelope.StopReasonToolUse)
			response.Normalized.Content = []modelenvelope.ResponsePart{{Type: "tool_call", ProviderCallID: "one", ToolName: "tool", ToolInput: json.RawMessage(`{}`)}}
			var err error
			output, err = h.AcceptOutput(t.Context(), agentexecution.AcceptOutputInput{
				ContextID: prepared.Context.ID, RuntimeLockID: lease, Response: response,
				Tools: []agentexecution.ToolProposal{{ProviderCallID: "one", Type: "built_in"}},
			})
			require.NoError(t, err)
			_, err = h.LoadExecution(t.Context())
			require.NoError(t, err)
		})
		measured("authorize", func(h *agentexecution.Handle) {
			_, err := h.AuthorizeTool(t.Context(), agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: lease})
			require.NoError(t, err)
		})
		measured("run_tool", func(h *agentexecution.Handle) {
			_, err := h.RunTool(t.Context(), agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: lease})
			require.NoError(t, err)
		})
		measured("complete_prepare", func(h *agentexecution.Handle) {
			_, err := h.CompleteTool(t.Context(), agentexecution.ToolCompletion{
				ToolRef: agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: lease},
				Outcome: "succeeded", Content: []agentexecution.Content{{Kind: "text", Text: "done"}},
			})
			require.NoError(t, err)
			prepared = prepareCommand(t, h, lease)
		})
		measured("accept_end", func(h *agentexecution.Handle) {
			_, err := h.AcceptOutput(t.Context(), agentexecution.AcceptOutputInput{
				ContextID: prepared.Context.ID, RuntimeLockID: lease,
				Response: commandResponse(modelenvelope.StopReasonEndTurn),
			})
			require.NoError(t, err)
			_, err = h.LoadExecution(t.Context())
			require.NoError(t, err)
		})
	}
}

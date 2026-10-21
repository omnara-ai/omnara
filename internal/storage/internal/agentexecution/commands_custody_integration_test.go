//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func toolCommand(
	t *testing.T,
	h *agentexecution.Handle,
	lease uuid.UUID,
	kind string,
) agentexecution.ToolRef {
	t.Helper()
	receiveCommand(t, h, "queued", "input")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	prepared := prepareCommand(t, h, lease)
	response := commandResponse(modelenvelope.StopReasonToolUse)
	response.Normalized.Content = []modelenvelope.ResponsePart{
		{Type: "tool_call", ProviderCallID: "call", ToolName: "tool", ToolInput: json.RawMessage(`{}`)},
	}
	output, err := h.AcceptOutput(
		t.Context(),
		agentexecution.AcceptOutputInput{
			RuntimeLockID: lease,
			ContextID:     prepared.Context.ID,
			Response:      response,
			Tools:         []agentexecution.ToolProposal{{ProviderCallID: "call", Type: kind}},
		},
	)
	require.NoError(t, err)
	return agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: lease}
}

func TestExecutionToolCustody(t *testing.T) {
	for _, kind := range []string{"built_in", "mcp", "custom"} {
		t.Run(kind, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			ref := toolCommand(t, h, lease, kind)
			assertCommandState(t, u, h)
			changed, err := h.AuthorizeTool(t.Context(), ref)
			require.NoError(t, err)
			require.True(t, changed)
			assertCommandState(t, u, h)
			changed, err = h.AuthorizeTool(t.Context(), ref)
			require.NoError(t, err)
			require.False(t, changed)
			if kind != "custom" {
				changed, err = h.RunTool(t.Context(), ref)
				require.NoError(t, err)
				require.True(t, changed)
				assertCommandState(t, u, h)
				require.NoError(t, u.Commit(t.Context(), "before rejected command"))
				u, h = f.handle(t, a)
				_, err = h.RunTool(t.Context(), ref)
				require.ErrorIs(t, err, storeerr.ErrToolCallInProgress)
				require.NoError(t, u.Rollback(t.Context()))
				u, h = f.handle(t, a)
				changed, err = h.RequeueTool(t.Context(), ref)
				require.NoError(t, err)
				require.True(t, changed)
				_, err = h.RunTool(t.Context(), ref)
				require.NoError(t, err)
			} else {
				require.NoError(t, h.Release(t.Context(), lease))
			}
			content := []agentexecution.Content{{Kind: "text", Text: "result"}}
			complete := func() (agentexecution.CompletedTool, error) {
				if kind == "custom" {
					return h.CompleteCustomTool(t.Context(), ref.ID, "succeeded", content)
				}
				return h.CompleteTool(
					t.Context(),
					agentexecution.ToolCompletion{ToolRef: ref, Outcome: "succeeded", Content: content},
				)
			}
			result, err := complete()
			require.NoError(t, err)
			require.True(t, result.Changed)
			snapshot := assertCommandState(t, u, h)
			require.Equal(t, agentexecution.WorkModel, snapshot.Selection.Work)

			replay, err := complete()
			require.NoError(t, err)
			require.False(t, replay.Changed)
			require.Equal(t, result.ResultID, replay.ResultID)
			require.NoError(t, u.Commit(t.Context(), "before rejected command"))
			u, h = f.handle(t, a)
			content[0].Text = "other"
			replay, err = complete()
			require.NoError(t, err)
			require.Equal(t, result.ResultID, replay.ResultID)
			require.NoError(t, u.Commit(t.Context(), "replay tool"))
			u, h = f.handle(t, a)
			_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{
				ToolRef: ref, Outcome: "failed", Content: content,
			})
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			require.NoError(t, u.Rollback(t.Context()))
			u, h = f.handle(t, a)
			ref.RuntimeLockID = uuid.New()
			replay, err = complete()
			require.NoError(t, err)
			require.False(t, replay.Changed)
			require.Equal(t, result.ResultID, replay.ResultID)
			require.NoError(t, u.Rollback(t.Context()))
		})
	}
}

func TestExecutionRuntimeRecoveryCommands(t *testing.T) {
	for _, reap := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "reap"}[reap], func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			receiveCommand(t, h, "queued", "input")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			prepared := prepareCommand(t, h, lease)
			if reap {
				_, err = u.DB().
					Exec(t.Context(),
						`UPDATE agent_runtime_locks SET started_at=statement_timestamp()-interval '3 minutes',renewed_at=statement_timestamp()-interval '2 minutes',lease_expires_at=statement_timestamp()-interval '1 minute' WHERE id=$1`,
						lease)
				require.NoError(t, err)
				changed, err := h.Reap(t.Context(), lease)
				require.NoError(t, err)
				require.True(t, changed)
			} else {
				require.NoError(t, h.Release(t.Context(), lease))
			}
			snapshot := assertCommandState(t, u, h)
			require.Equal(t, agentexecution.RecoveryRetry, snapshot.View.NormalContext.Recovery)
			require.Equal(t, prepared.Context.ID, snapshot.View.NormalContext.ID)
			require.Equal(t, agentexecution.WaitModelDeadline, snapshot.Selection.Wait)
			require.NoError(t, u.Commit(t.Context(), "runtime recovery"))
		})
	}
}

func TestExecutionCancelCommand(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	toolCommand(t, h, lease, "built_in")
	receiveCommand(t, h, "queued", "preserved")
	receiveCommand(t, h, "steering", "canceled")
	input := agentexecution.CancelInput{
		Reason:  "agent_canceled",
		Message: "The model call was canceled by an explicit agent cancellation.",
	}
	result, err := h.Cancel(t.Context(), input)
	require.NoError(t, err)
	require.True(t, result.Changed)
	snapshot := assertCommandState(t, u, h)
	require.Equal(t, agentexecution.AdmitOneQueued, snapshot.Selection.Admission)
	replay, err := h.Cancel(t.Context(), input)
	require.NoError(t, err)
	require.False(t, replay.Changed)
	require.NoError(t, h.Release(t.Context(), lease))
	assertCommandState(t, u, h)
	require.NoError(t, u.Commit(t.Context(), "cancel"))
}

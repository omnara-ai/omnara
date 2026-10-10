//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
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
				require.ErrorIs(
					t,
					u.Savepoint(
						t.Context(),
						func(_ *agentexecution.Unit) error { _, err := h.RunTool(t.Context(), ref); return err },
					),
					storeerr.ErrToolCallInProgress,
				)
				changed, err = h.RequeueTool(t.Context(), ref)
				require.NoError(t, err)
				require.True(t, changed)
				_, err = h.RunTool(t.Context(), ref)
				require.NoError(t, err)
			} else {
				work, err := h.Advance(t.Context(), lease, true, true)
				require.NoError(t, err)
				require.True(t, work.Released)
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
			require.ErrorIs(
				t,
				u.Savepoint(
					t.Context(),
					func(_ *agentexecution.Unit) error { content[0].Text = "other"; _, err := complete(); return err },
				),
				storeerr.ErrIdempotencyConflict,
			)
			require.NoError(t, u.Commit(t.Context(), "tool custody"))
		})
	}
}

func TestExecutionQuestionCustody(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolve", true: "supersede"}[cancel], func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			ref := toolCommand(t, h, lease, "built_in")
			_, err := h.AuthorizeTool(t.Context(), ref)
			require.NoError(t, err)
			form := interactionform.Form{
				Title: "Question",
				Questions: []interactionform.Question{
					{Prompt: "Continue?", Options: []interactionform.Option{{Label: "yes"}}},
				},
			}
			opened, err := h.OpenInteraction(
				t.Context(),
				agentexecution.OpenInteractionInput{ToolRef: ref, Question: &form},
			)
			require.NoError(t, err)
			require.True(t, opened.Created)
			assertCommandState(t, u, h)
			replay, err := h.OpenInteraction(
				t.Context(),
				agentexecution.OpenInteractionInput{ToolRef: ref, Question: &form},
			)
			require.NoError(t, err)
			require.False(t, replay.Created)
			work, err := h.Advance(t.Context(), lease, true, true)
			require.NoError(t, err)
			require.True(t, work.Released)
			assertCommandState(t, u, h)
			if cancel {
				input := receiveCommand(t, h, "steering", "cancel question")
				ids, err := h.SupersedeInteractions(t.Context(), input.ID)
				require.NoError(t, err)
				require.Equal(t, []uuid.UUID{opened.ID}, ids)
			} else {
				input := agentexecution.ResolveInteractionInput{ID: opened.ID,
					Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{0}}}}}
				resolved, err := h.ResolveInteraction(t.Context(), input)
				require.NoError(t, err)
				require.Equal(t, "resolved", resolved.State)
				replay, err := h.ResolveInteraction(t.Context(), input)
				require.NoError(t, err)
				require.Equal(t, resolved.InputID, replay.InputID)
			}
			assertCommandState(t, u, h)
			require.NoError(t, u.Commit(t.Context(), "question custody"))
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

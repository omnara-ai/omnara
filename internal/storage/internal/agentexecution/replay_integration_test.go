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

func TestInternalModelReplayIdentityAndFence(t *testing.T) {
	for _, kind := range []string{"output", "retry", "terminal", "compaction"} {
		t.Run(kind, func(t *testing.T) {
			f, agent, lease := commandFixture(t)
			u, h := f.handle(t, agent)
			receiveCommand(t, h, "queued", "work")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			prepared := prepareCommand(t, h, lease)
			output := agentexecution.AcceptOutputInput{
				ContextID: prepared.Context.ID, RuntimeLockID: lease,
				Response: commandResponse(modelenvelope.StopReasonEndTurn),
			}
			failure := agentexecution.ModelFailure{
				ContextID: prepared.Context.ID, RuntimeLockID: lease,
				ErrorKind: "runtime", ErrorMessage: "original",
			}
			if kind == "retry" {
				failure.Recovery = agentexecution.RecoveryRetry
			}
			var accepted agentexecution.AcceptedOutput
			var compact agentexecution.CompactionStart
			run := func(h *agentexecution.Handle, runtime uuid.UUID) error {
				switch kind {
				case "output":
					output.RuntimeLockID = runtime
					result, err := h.AcceptOutput(t.Context(), output)
					if err == nil && accepted.ID != uuid.Nil {
						require.Equal(t, accepted.ID, result.ID)
						require.False(t, result.Created)
					}
					if accepted.ID == uuid.Nil {
						accepted = result
					}
					return err
				case "compaction":
					failure.RuntimeLockID = runtime
					result, err := h.BeginCompaction(t.Context(), agentexecution.BeginCompactionInput{
						Failure: failure, SourceEnd: prepared.Context.InputEventSequence,
					})
					if err == nil && compact.Model.Context.ID != uuid.Nil {
						require.Equal(t, compact.Model.Context.ID, result.Model.Context.ID)
						require.False(t, result.Model.Created)
					}
					if compact.Model.Context.ID == uuid.Nil {
						compact = result
					}
					return err
				default:
					failure.RuntimeLockID = runtime
					_, err := h.FailModel(t.Context(), failure)
					return err
				}
			}
			require.NoError(t, run(h, lease))
			require.NoError(t, u.Commit(t.Context(), "complete attempt"))
			failure.ErrorMessage = "different"
			failure.ErrorCode = "different"
			failure.Evidence.ResponseID = "ignored without API identity"
			output.Response.Normalized.Content = nil
			output.Response.Normalized.Usage.OutputTokens = 100
			u, h = f.handle(t, agent)
			require.NoError(t, run(h, lease))
			require.NoError(t, u.Commit(t.Context(), "replay attempt"))
			if kind == "compaction" {
				u, h = f.handle(t, agent)
				resume := agentexecution.ResumeCompactionInput{
					RuntimeLockID: lease, ParentID: prepared.Context.ID,
					Watermark: prepared.Context.InputEventSequence, SourceEnd: prepared.Context.InputEventSequence,
				}
				replay, err := h.ResumeCompaction(t.Context(), resume)
				require.NoError(t, err)
				require.Equal(t, compact.Model.Context.ID, replay.Model.Context.ID)
				require.False(t, replay.Model.Created)
				require.NoError(t, u.Commit(t.Context(), "replay compaction"))
				u, h = f.handle(t, agent)
				resume.RuntimeLockID = uuid.New()
				_, err = h.ResumeCompaction(t.Context(), resume)
				require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
				require.NoError(t, u.Rollback(t.Context()))
			}
			u, h = f.handle(t, agent)
			require.ErrorIs(t, run(h, uuid.New()), storeerr.ErrRuntimeLockInactive)
			require.NoError(t, u.Rollback(t.Context()))
			u, h = f.handle(t, agent)
			if kind == "output" {
				failure.RuntimeLockID = lease
				_, err = h.FailModel(t.Context(), failure)
				require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			} else {
				output.RuntimeLockID = lease
				_, err = h.AcceptOutput(t.Context(), output)
				require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
			}
			require.NoError(t, u.Rollback(t.Context()))
			if kind != "output" {
				u, h = f.handle(t, agent)
				failure.RuntimeLockID = lease
				failure.Recovery = map[string]agentexecution.RecoveryKind{
					"retry": agentexecution.RecoveryNone, "terminal": agentexecution.RecoveryRetry,
					"compaction": agentexecution.RecoveryRetry,
				}[kind]
				_, err = h.FailModel(t.Context(), failure)
				require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
				require.NoError(t, u.Rollback(t.Context()))
			}
		})
	}
}

func TestInteractionReplayStateAndFence(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{true: "canceled", false: "resolved"}[canceled], func(t *testing.T) {
			f, agent, lease := commandFixture(t)
			u, h := f.handle(t, agent)
			ref := toolCommand(t, h, lease, "built_in")
			_, err := h.AuthorizeTool(t.Context(), ref)
			require.NoError(t, err)
			form := interactionform.Form{Title: "Original", Questions: []interactionform.Question{
				{Prompt: "Choose", Options: []interactionform.Option{{Label: "yes"}, {Label: "no"}}},
			}}
			open := agentexecution.OpenInteractionInput{ToolRef: ref, Question: &form}
			original, err := h.OpenInteraction(t.Context(), open)
			require.NoError(t, err)
			form.Title = "Changed"
			replay, err := h.OpenInteraction(t.Context(), open)
			require.NoError(t, err)
			require.Equal(t, original.ID, replay.ID)
			require.JSONEq(t, string(original.Request), string(replay.Request))
			resolution := agentexecution.ResolveInteractionInput{
				ID:         original.ID,
				Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{0}}}},
			}
			if canceled {
				input := receiveCommand(t, h, "steering", "supersede")
				_, err = h.SupersedeInteractions(t.Context(), input.ID)
				require.NoError(t, err)
			} else {
				original, err = h.ResolveInteraction(t.Context(), resolution)
				require.NoError(t, err)
				replay, err = h.ResolveInteraction(t.Context(), resolution)
				require.NoError(t, err)
				require.Equal(t, original.InputID, replay.InputID)
				require.JSONEq(t, string(original.Resolution), string(replay.Resolution))
				resolution.Resolution.Answers[0].OptionIndices = []int{1}
			}
			require.NoError(t, u.Commit(t.Context(), "interaction replay"))
			u, h = f.handle(t, agent)
			_, err = h.ResolveInteraction(t.Context(), resolution)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			require.NoError(t, u.Rollback(t.Context()))
			u, h = f.handle(t, agent)
			open.RuntimeLockID = uuid.New()
			_, err = h.OpenInteraction(t.Context(), open)
			require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
			require.NoError(t, u.Rollback(t.Context()))
		})
	}
}

func TestStoredExecutionHeadMatchesMultiTurnReconstruction(t *testing.T) {
	f, agent, lease := commandFixture(t)
	for turn := range 3 {
		u, h := f.handle(t, agent)
		ref := toolCommand(t, h, lease, "built_in")
		_, err := h.AuthorizeTool(t.Context(), ref)
		require.NoError(t, err)
		_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{
			ToolRef: ref, Outcome: "succeeded", Content: []agentexecution.Content{{Kind: "text", Text: "done"}},
		})
		require.NoError(t, err)
		assertCommandState(t, u, h)
		prepared := prepareCommand(t, h, lease)
		_, err = h.AcceptOutput(t.Context(), agentexecution.AcceptOutputInput{
			RuntimeLockID: lease, ContextID: prepared.Context.ID,
			Response: commandResponse(modelenvelope.StopReasonEndTurn),
		})
		require.NoError(t, err)
		require.NoError(t, u.Commit(t.Context(), "complete turn"))
		u, h = f.handle(t, agent)
		snapshot := assertCommandState(t, u, h)
		var storedTurn uuid.UUID
		require.NoError(t, u.DB().QueryRow(t.Context(),
			`SELECT current_turn_id FROM agent_execution_state WHERE agent_id=$1`, agent).Scan(&storedTurn))
		require.Equal(t, storedTurn, snapshot.Head.CurrentTurnID)
		require.False(t, snapshot.Head.TurnContinuable, "turn %d", turn)
		require.NoError(t, u.Rollback(t.Context()))
	}
}

func TestOutputReplayRejectsDifferentToolID(t *testing.T) {
	f, agent, lease := commandFixture(t)
	u, h := f.handle(t, agent)
	receiveCommand(t, h, "queued", "work")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	prepared := prepareCommand(t, h, lease)
	response := commandResponse(modelenvelope.StopReasonToolUse)
	response.Normalized.Content = []modelenvelope.ResponsePart{
		{Type: "tool_call", ProviderCallID: "call", ToolName: "tool", ToolInput: json.RawMessage(`{}`)},
	}
	input := agentexecution.AcceptOutputInput{
		RuntimeLockID: lease, ContextID: prepared.Context.ID, Response: response,
		Tools: []agentexecution.ToolProposal{{ID: uuid.New(), ProviderCallID: "call", Type: "built_in"}},
	}
	accepted, err := h.AcceptOutput(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "accept tool output"))
	u, h = f.handle(t, agent)
	replay, err := h.AcceptOutput(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, accepted.Tools, replay.Tools)
	input.Tools[0].ID = uuid.New()
	_, err = h.AcceptOutput(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.NoError(t, u.Rollback(t.Context()))
}

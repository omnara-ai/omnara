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
						require.True(t, result.Created)
						accepted = result
					}
					return err
				case "compaction":
					failure.Recovery = agentexecution.RecoveryCompact
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
			output.Response.ProviderMetadata.OpenRouter.Provider = "different"
			u, h = f.handle(t, agent)
			require.NoError(t, run(h, lease))
			assertCommandState(t, u, h)
			require.NoError(t, u.Commit(t.Context(), "replay attempt"))
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
			require.True(t, original.Created)
			assertCommandState(t, u, h)
			form.Title = "Changed"
			replay, err := h.OpenInteraction(t.Context(), open)
			require.NoError(t, err)
			require.Equal(t, original.ID, replay.ID)
			require.False(t, replay.Created)
			require.JSONEq(t, string(original.Request), string(replay.Request))
			resolution := agentexecution.ResolveInteractionInput{
				ID:         original.ID,
				Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{0}}}},
			}
			if canceled {
				input := receiveCommand(t, h, "steering", "supersede")
				ids, err := h.SupersedeInteractions(t.Context(), input.ID)
				require.NoError(t, err)
				require.Equal(t, []uuid.UUID{original.ID}, ids)
			} else {
				original, err = h.ResolveInteraction(t.Context(), resolution)
				require.NoError(t, err)
				require.Equal(t, "resolved", original.State)
				replay, err = h.ResolveInteraction(t.Context(), resolution)
				require.NoError(t, err)
				require.Equal(t, original.InputID, replay.InputID)
				require.JSONEq(t, string(original.Resolution), string(replay.Resolution))
				resolution.Resolution.Answers[0].OptionIndices = []int{1}
			}
			assertCommandState(t, u, h)
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

func assertPersistedHeadMatchesReconstruction(t *testing.T, f executionFixture, agent uuid.UUID) {
	t.Helper()
	u, h := f.handle(t, agent)
	defer func() { require.NoError(t, u.Rollback(t.Context())) }()
	rebuilt, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	persisted := agentexecution.ExecutionHead{AgentID: agent}
	none := "'00000000-0000-0000-0000-000000000000'::uuid"
	require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT coalesce(current_turn_id,`+none+`),stop_sequence,
 max_normal_input_sequence,max_context_input_sequence,coalesce(normal_context_id,`+none+`),
 coalesce(compaction_context_id,`+none+`),coalesce(pending_tool_output_id,`+none+`),
 coalesce(pending_output_limit_id,`+none+`),coalesce(pending_config_input_id,`+none+`),
 coalesce(pending_checkpoint_id,`+none+`),incomplete_tools,logical_ready_at
 FROM agent_execution_state WHERE agent_id=$1`, agent).Scan(
		&persisted.CurrentTurnID, &persisted.StopSequence, &persisted.MaxNormalInputSequence,
		&persisted.MaxContextInputSequence, &persisted.NormalContextID, &persisted.CompactionContextID,
		&persisted.PendingToolOutputID, &persisted.PendingOutputLimitID, &persisted.PendingConfigInputID,
		&persisted.PendingCheckpointID, &persisted.IncompleteTools,
		&persisted.LogicalReadyAt))
	if persisted.LogicalReadyAt != nil && rebuilt.Head.LogicalReadyAt != nil {
		require.WithinDuration(t, *rebuilt.Head.LogicalReadyAt, *persisted.LogicalReadyAt, 0)
		persisted.LogicalReadyAt = rebuilt.Head.LogicalReadyAt
	}
	require.Equal(t, rebuilt.Head, persisted)
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
		require.NoError(t, u.Commit(t.Context(), "complete tool"))
		assertPersistedHeadMatchesReconstruction(t, f, agent)
		u, h = f.handle(t, agent)
		prepared := prepareCommand(t, h, lease)
		_, err = h.AcceptOutput(t.Context(), agentexecution.AcceptOutputInput{
			RuntimeLockID: lease, ContextID: prepared.Context.ID,
			Response: commandResponse(modelenvelope.StopReasonEndTurn),
		})
		require.NoError(t, err)
		require.NoError(t, u.Commit(t.Context(), "complete turn"))
		assertPersistedHeadMatchesReconstruction(t, f, agent)
		u, h = f.handle(t, agent)
		snapshot := assertCommandState(t, u, h)
		require.False(t, snapshot.Selection.TurnContinuable, "turn %d", turn)
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

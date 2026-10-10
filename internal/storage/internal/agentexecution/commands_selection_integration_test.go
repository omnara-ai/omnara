//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestExecutionBatchConsumption(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "initial")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	prepared := prepareCommand(t, h, lease)
	for cycle := range 4 {
		response := commandResponse(modelenvelope.StopReasonToolUse)
		response.Normalized.Content = []modelenvelope.ResponsePart{
			{Type: "tool_call", ProviderCallID: "one", ToolName: "tool", ToolInput: json.RawMessage(`{}`)},
			{Type: "tool_call", ProviderCallID: "two", ToolName: "tool", ToolInput: json.RawMessage(`{}`)},
		}
		output, err := h.AcceptOutput(t.Context(), agentexecution.AcceptOutputInput{
			ContextID: prepared.Context.ID, RuntimeLockID: lease, Response: response,
			Tools: []agentexecution.ToolProposal{
				{ProviderCallID: "one", Type: "built_in"}, {ProviderCallID: "two", Type: "built_in"},
			},
		})
		require.NoError(t, err)
		var config agentexecution.ActivatedConfig
		if cycle == 1 {
			config, err = h.ActivateConfig(t.Context(), agentexecution.ActivateConfigInput{ID: uuid.New(), ConfigID: f.config})
			require.NoError(t, err)
		}
		for _, tool := range output.Tools {
			snapshot := assertCommandState(t, u, h)
			require.Equal(t, agentexecution.WorkTool, snapshot.Selection.Work)
			require.Equal(t, output.ID, snapshot.Head.PendingToolOutputID)
			ref := agentexecution.ToolRef{ID: tool.ID, RuntimeLockID: lease}
			_, err = h.AuthorizeTool(t.Context(), ref)
			require.NoError(t, err)
			_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{ToolRef: ref, Outcome: "succeeded",
				Content: []agentexecution.Content{{Kind: "text", Text: "done"}}})
			require.NoError(t, err)
		}
		snapshot := assertCommandState(t, u, h)
		if cycle == 1 {
			require.Equal(t, agentexecution.OriginConfig, snapshot.Selection.Model.Origin)
			require.Equal(t, config.InputID, snapshot.Selection.Model.SourceInputID)
		} else {
			require.Equal(t, agentexecution.OriginTools, snapshot.Selection.Model.Origin)
		}
		next := prepareCommand(t, h, lease)
		require.GreaterOrEqual(t, next.Context.InputEventSequence, snapshot.View.ToolBatch.Completion.LastResultSequence)
		snapshot = assertCommandState(t, u, h)
		require.Equal(t, output.ID, snapshot.Head.PendingToolOutputID)
		require.Equal(t, agentexecution.OriginTools, snapshot.Selection.Model.Origin)
		_, err = h.FailModel(t.Context(), agentexecution.ModelFailure{ContextID: next.Context.ID, RuntimeLockID: lease,
			Recovery: agentexecution.RecoveryRetry, ErrorKind: "transient", ErrorMessage: "retry"})
		require.NoError(t, err)
		snapshot = assertCommandState(t, u, h)
		require.Equal(t, agentexecution.OriginRetry, snapshot.Selection.Model.Origin)
		prepared = prepareCommand(t, h, lease)
		snapshot = assertCommandState(t, u, h)
		require.Equal(t, output.ID, snapshot.Head.PendingToolOutputID)
	}
	_, err = h.AcceptOutput(t.Context(), agentexecution.AcceptOutputInput{ContextID: prepared.Context.ID,
		RuntimeLockID: lease, Response: commandResponse(modelenvelope.StopReasonEndTurn)})
	require.NoError(t, err)
	snapshot := assertCommandState(t, u, h)
	require.Nil(t, snapshot.View.ToolBatch)
	require.Nil(t, snapshot.Selection.Model)
	require.NoError(t, u.Commit(t.Context(), "batch consumption"))
}

func TestExecutionSteeringAcrossPreparationAndRetry(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	initial := receiveCommand(t, h, "queued", "initial")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	first := receiveCommand(t, h, "steering", "after claim")
	prepared := prepareCommand(t, h, lease)
	require.Equal(t, []uuid.UUID{initial.ID}, prepared.Context.Opening.InputIDs)
	_, err = h.FailModel(t.Context(), agentexecution.ModelFailure{ContextID: prepared.Context.ID, RuntimeLockID: lease,
		Recovery: agentexecution.RecoveryRetry, ErrorKind: "transient", ErrorMessage: "retry"})
	require.NoError(t, err)
	retry := prepareCommand(t, h, lease)
	require.Equal(t, prepared.Context.Opening, retry.Context.Opening)
	require.Equal(t, prepared.Context.InputEventSequence, retry.Context.InputEventSequence)
	_, err = h.FailModel(t.Context(), agentexecution.ModelFailure{ContextID: retry.Context.ID, RuntimeLockID: lease,
		Recovery: agentexecution.RecoveryRetry, RetryDelay: time.Hour, ErrorKind: "transient", ErrorMessage: "retry later"})
	require.NoError(t, err)
	receiveCommand(t, h, "queued", "later queued")
	second := receiveCommand(t, h, "steering", "second steering")
	snapshot := assertCommandState(t, u, h)
	require.Equal(t, agentexecution.AdmitAllSteering, snapshot.Selection.Admission)
	require.Equal(t, agentexecution.OriginRetry, snapshot.Selection.Model.Origin)
	admitted, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	require.Len(t, admitted.Inputs, 2)
	next := prepareCommand(t, h, lease)
	require.Equal(t, []uuid.UUID{initial.ID, first.ID, second.ID}, next.Context.Opening.InputIDs)
	require.NoError(t, u.Commit(t.Context(), "steering boundary"))
}

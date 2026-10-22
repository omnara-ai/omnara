//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestToolBatchRetainsDeferredAndAsyncOwnership(t *testing.T) {
	f, a := stepFixture(t)
	u, _, modelWork, prepared := claimPreparedStep(t, f)
	require.NoError(t, u.Commit(t.Context(), "claim model"))
	u, h := f.handle(t, a)
	output, err := h.AcceptOutput(t.Context(), stepToolOutput(prepared, modelWork.RuntimeID, 3))
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "model output"))
	u, h = f.handle(t, a)
	work, err := h.Claim(t.Context(), uuid.New(), time.Minute)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "claim tools"))
	ref := func(i int) agentexecution.ToolRef {
		return agentexecution.ToolRef{ID: output.Tools[i].ID, RuntimeLockID: work.RuntimeID}
	}
	u, h = f.handle(t, a)
	_, err = h.AuthorizeTool(t.Context(), ref(0))
	require.NoError(t, err)
	_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{ToolRef: ref(0), Outcome: "succeeded"})
	require.NoError(t, err)
	finished, _, err := h.FinishToolBatch(t.Context(), work.RuntimeID, output.ID)
	require.NoError(t, err)
	require.False(t, finished)
	form, err := toolpermission.NewAllowDenyForm("Permission", nil)
	require.NoError(t, err)
	permission := toolpermission.Request{Permission: toolpermission.Selection{Mode: toolpermission.ModeAlwaysAsk}, Authorization: toolpermission.Authorization{ToolName: "list_agents", Input: json.RawMessage(`{}`)}, Form: form}
	_, err = h.OpenInteraction(t.Context(), agentexecution.OpenInteractionInput{ToolRef: ref(1), Permission: &permission})
	require.NoError(t, err)
	_, err = h.AuthorizeTool(t.Context(), ref(2))
	require.NoError(t, err)
	finished, _, err = h.FinishToolBatch(t.Context(), work.RuntimeID, output.ID)
	require.NoError(t, err)
	require.False(t, finished)
	require.NoError(t, u.Commit(t.Context(), "mixed batch"))
	u, h = f.handle(t, a)
	_, err = h.RunTool(t.Context(), ref(2))
	require.NoError(t, err)
	finished, _, err = h.FinishToolBatch(t.Context(), work.RuntimeID, output.ID)
	require.NoError(t, err)
	require.False(t, finished)
	require.NoError(t, u.Commit(t.Context(), "async dispatch"))
	renewal, err := agentexecution.BeginTestUnit(t.Context(), agentexecution.NewCell(f.pool, nil, nil))
	require.NoError(t, err)
	_, err = renewal.RenewRuntime(
		t.Context(), agentexecution.AgentRoute{ProjectID: f.project, AgentID: a}, work.RuntimeID, time.Minute)
	require.NoError(t, err)
	require.NoError(t, renewal.Commit(t.Context(), "renew async"))
	u, h = f.handle(t, a)
	_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{ToolRef: ref(2), Outcome: "succeeded"})
	require.NoError(t, err)
	finished, transferred, err := h.FinishToolBatch(t.Context(), work.RuntimeID, output.ID)
	require.NoError(t, err)
	require.True(t, finished)
	require.True(t, transferred)
	require.NoError(t, u.Commit(t.Context(), "async complete and transfer permission"))
	_, leases, wakes := stepCounts(t, f, a)
	require.Zero(t, leases)
	require.Zero(t, wakes)
	var states []string
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT array_agg(tool_state ORDER BY created_at,id) FROM event_webhook_deliveries WHERE tool_call_id=$1`, output.Tools[2].ID,
	).Scan(&states))
	require.Equal(t, []string{"awaiting_authorization", "ready", "running", "completed"}, states)
	u, h = f.handle(t, a)
	replay, err := h.CompleteTool(t.Context(), agentexecution.ToolCompletion{ToolRef: ref(2), Outcome: "succeeded"})
	require.NoError(t, err)
	require.False(t, replay.Changed)
	require.NoError(t, u.Rollback(t.Context()))
}

func TestToolCompletionReplayKeepsNewerClaim(t *testing.T) {
	f, a := stepFixture(t)
	u, _, work, prepared := claimPreparedStep(t, f)
	require.NoError(t, u.Commit(t.Context(), "claim model"))
	u, h := f.handle(t, a)
	output, err := h.AcceptOutput(t.Context(), stepToolOutput(prepared, work.RuntimeID, 1))
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "model output"))
	u, h = f.handle(t, a)
	tools, err := h.Claim(t.Context(), uuid.New(), time.Minute)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "claim tool"))
	ref := agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: tools.RuntimeID}
	u, h = f.handle(t, a)
	_, err = h.AuthorizeTool(t.Context(), ref)
	require.NoError(t, err)
	completion := agentexecution.ToolCompletion{ToolRef: ref, Outcome: "succeeded"}
	accepted, err := h.CompleteTool(t.Context(), completion)
	require.NoError(t, err)
	finished, transferred, err := h.FinishToolBatch(t.Context(), tools.RuntimeID, output.ID)
	require.NoError(t, err)
	require.True(t, finished)
	require.False(t, transferred)
	require.NoError(t, u.Commit(t.Context(), "complete tool batch"))
	u, _, newer, _ := claimPreparedStep(t, f)
	require.NoError(t, u.Commit(t.Context(), "next model claim"))
	u, h = f.handle(t, a)
	replay, err := h.CompleteTool(t.Context(), completion)
	require.NoError(t, err)
	require.False(t, replay.Changed)
	require.Equal(t, accepted.ResultID, replay.ResultID)
	require.NoError(t, u.Rollback(t.Context()))
	var current uuid.UUID
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT id FROM agent_runtime_locks WHERE agent_id=$1`, a,
	).Scan(&current))
	require.Equal(t, newer.RuntimeID, current)
	completion.Outcome = "failed"
	u, h = f.handle(t, a)
	_, err = h.CompleteTool(t.Context(), completion)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.NoError(t, u.Rollback(t.Context()))
}

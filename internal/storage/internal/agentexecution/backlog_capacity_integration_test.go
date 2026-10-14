//go:build integration

package agentexecution_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func fillExecutionBacklog(t *testing.T, f executionFixture, agent uuid.UUID, count int) []uuid.UUID {
	t.Helper()
	u, h := f.handle(t, agent)
	ids := make([]uuid.UUID, 0, count)
	for i := range count {
		mode := "queued"
		if i%2 == 1 {
			mode = "steering"
		}
		ids = append(ids, receiveCommand(t, h, mode, "pending").ID)
	}
	require.NoError(t, u.Commit(t.Context(), "fill backlog"))
	return ids
}

func pendingExecutionCount(t *testing.T, f executionFixture, agent uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND state='received' AND input_kind='content'`,
		agent).Scan(&n))
	return n
}

func TestExecutionBacklogCapacity(t *testing.T) {
	f, agent, _ := commandFixture(t)
	ids := fillExecutionBacklog(t, f, agent, 49)
	u, h := f.handle(t, agent)
	last := receiveCommand(t, h, "queued", "last")
	require.NoError(t, u.Commit(t.Context(), "last slot"))
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	t.Run("reject", func(t *testing.T) {
		u, h := f.handle(t, agent)
		err := h.CheckContentCapacity(t.Context())
		require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
		require.NoError(t, u.Rollback(t.Context()))
		require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	})
	t.Run("reorder_and_convert", func(t *testing.T) {
		u, h := f.handle(t, agent)
		changed, err := h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{ID: last.ID,
			DeliveryMode: "queued", Position: "front"})
		require.NoError(t, err)
		require.True(t, changed)
		changed, err = h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{ID: last.ID, DeliveryMode: "steering"})
		require.NoError(t, err)
		require.True(t, changed)
		require.NoError(t, u.Commit(t.Context(), "reorder and convert"))
		require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	})
	t.Run("cancel", func(t *testing.T) {
		u, h := f.handle(t, agent)
		changed, err := h.ChangeBacklog(t.Context(), agentexecution.BacklogChange{ID: ids[0],
			DeliveryMode: "queued", Cancel: true})
		require.NoError(t, err)
		require.True(t, changed)
		receiveCommand(t, h, "queued", "replacement")
		require.NoError(t, u.Commit(t.Context(), "cancel and refill"))
		require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	})
	t.Run("admit", func(t *testing.T) {
		u, h := f.handle(t, agent)
		admitted, err := h.AdmitInputs(t.Context())
		require.NoError(t, err)
		require.Len(t, admitted.Inputs, 25)
		receiveCommand(t, h, "queued", "replacement")
		require.NoError(t, u.Commit(t.Context(), "admit and refill"))
		require.Equal(t, 26, pendingExecutionCount(t, f, agent))
	})
}

func TestExecutionBacklogCapacityDoesNotBlockControls(t *testing.T) {
	f, agent, _ := commandFixture(t)
	fillExecutionBacklog(t, f, agent, 50)
	u, h := f.handle(t, agent)
	_, err := h.ActivateConfig(t.Context(), agentexecution.ActivateConfigInput{ConfigID: f.config})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "config at capacity"))
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	u, h = f.handle(t, agent)
	_, err = h.Cancel(t.Context(), agentexecution.CancelInput{Reason: "user_cancel", Message: "cancel"})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "cancel at capacity"))
}

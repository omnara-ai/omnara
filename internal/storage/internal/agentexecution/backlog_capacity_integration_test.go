//go:build integration

package agentexecution_test

import (
	"context"
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
	receiveCommand(t, h, "queued", "last")
	require.NoError(t, u.Commit(t.Context(), "last slot"))
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	t.Run("reject", func(t *testing.T) {
		u, h := f.handle(t, agent)
		_, err := h.ReceiveContent(t.Context(), agentexecution.ReceiveContentInput{
			DeliveryMode: "queued", Content: []agentexecution.Content{{Kind: "text", Text: "overflow"}},
		})
		require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
		require.NoError(t, u.Rollback(t.Context()))
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
		require.Len(t, admitted.Inputs, 24)
		receiveCommand(t, h, "queued", "replacement")
		require.NoError(t, u.Commit(t.Context(), "admit and refill"))
		require.Equal(t, 27, pendingExecutionCount(t, f, agent))
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

func TestReceiveContentCapacityAndReplay(t *testing.T) {
	f, agent, _ := commandFixture(t)
	u, h := f.handle(t, agent)
	input := agentexecution.ReceiveContentInput{
		DeliveryMode: "queued", IdempotencyScope: "test", IdempotencyKey: "original",
		Content: []agentexecution.Content{{Kind: "text", Text: "original"}},
	}
	original, err := h.ReceiveContent(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "original input"))
	fillExecutionBacklog(t, f, agent, 49)
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))

	u, h = f.handle(t, agent)
	newInput := input
	newInput.IdempotencyKey = "new"
	_, err = h.ReceiveContent(t.Context(), newInput)
	require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
	require.ErrorIs(t, u.Commit(t.Context(), "reject full backlog"), agentexecution.ErrUnitAborted)
	require.NoError(t, u.Rollback(t.Context()))
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))

	u, h = f.handle(t, agent)
	input.DeliveryMode = "steering"
	input.Content = []agentexecution.Content{{Kind: "text", Text: "different"}}
	replayed, err := h.ReceiveContent(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, original.ID, replayed.ID)
	require.NoError(t, u.Commit(t.Context(), "replay at capacity"))
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	var mode, text string
	require.NoError(t, f.pool.QueryRow(t.Context(), `
SELECT i.delivery_mode,c.text_content FROM agent_inputs i
JOIN content_blocks c ON c.owner_agent_input_id=i.id WHERE i.id=$1`, original.ID).Scan(&mode, &text))
	require.Equal(t, "queued", mode)
	require.Equal(t, "original", text)

	u, h = f.handle(t, agent)
	accepted, err := h.ReceiveContent(t.Context(), agentexecution.CapacityExemptForTest(newInput))
	require.NoError(t, err)
	require.NotEqual(t, original.ID, accepted.ID)
	require.NoError(t, u.Commit(t.Context(), "exempt input at capacity"))
	require.Equal(t, 51, pendingExecutionCount(t, f, agent))

	u, h = f.handle(t, agent)
	replayed, err = h.ReceiveContent(t.Context(), newInput)
	require.NoError(t, err)
	require.Equal(t, accepted.ID, replayed.ID)
	newInput.IdempotencyKey = "another"
	_, err = h.ReceiveContent(t.Context(), newInput)
	require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
	require.NoError(t, u.Rollback(t.Context()))
	require.Equal(t, 51, pendingExecutionCount(t, f, agent))
}

func TestReceiveContentPreparesOnlyNewInputs(t *testing.T) {
	f, agent, _ := commandFixture(t)
	u, h := f.handle(t, agent)
	prepared := 0
	input := agentexecution.ReceiveContentInput{
		DeliveryMode: "queued", IdempotencyScope: "test", IdempotencyKey: "prepare-once",
		Prepare: func(context.Context) (uuid.UUID, []agentexecution.Content, error) {
			prepared++
			return uuid.Nil, []agentexecution.Content{{Kind: "text", Text: "prepared"}}, nil
		},
	}
	first, err := h.ReceiveContent(t.Context(), input)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	replay, err := h.ReceiveContent(t.Context(), input)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, first.ID, replay.ID)
	require.Equal(t, 1, prepared)
	require.NoError(t, u.Commit(t.Context(), "prepared input replay"))
}

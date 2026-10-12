//go:build integration

package agentexecution_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
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
	input := agentexecution.ReceiveContentInput{DeliveryMode: "queued", IdempotencyScope: "test",
		IdempotencyKey: "last", Content: []agentexecution.Content{{Kind: "text", Text: "last"}}}
	u, h := f.handle(t, agent)
	last, err := h.ReceiveContent(t.Context(), input)
	require.NoError(t, err)
	require.True(t, last.Created)
	require.NoError(t, u.Commit(t.Context(), "last slot"))
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	t.Run("replay", func(t *testing.T) {
		u, h := f.handle(t, agent)
		got, err := h.ReceiveContent(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, last.ID, got.ID)
		require.False(t, got.Created)
		require.NoError(t, u.Commit(t.Context(), "replay"))
	})
	t.Run("reject", func(t *testing.T) {
		u, h := f.handle(t, agent)
		_, err := h.ReceiveContent(t.Context(), agentexecution.ReceiveContentInput{DeliveryMode: "steering",
			Content: input.Content})
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

func TestExecutionBacklogConcurrentLastSlot(t *testing.T) {
	f, agent, _ := commandFixture(t)
	fillExecutionBacklog(t, f, agent, 49)
	cell := agentexecution.NewCell(f.pool, nil, nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			_, err := agentexecution.Retry(t.Context(), "last slot", func(ctx context.Context) (struct{}, error) {
				u, err := cell.Begin(ctx)
				if err != nil {
					return struct{}{}, err
				}
				defer func() { _ = u.Rollback(ctx) }()
				err = u.LockAgentRefs(ctx, []lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: agent}}, nil)
				if err != nil {
					return struct{}{}, err
				}
				h, err := u.Handle(f.project, agent)
				if err != nil {
					return struct{}{}, err
				}
				_, err = h.ReceiveContent(ctx, agentexecution.ReceiveContentInput{DeliveryMode: "queued",
					Content: []agentexecution.Content{{Kind: "text", Text: "race"}}})
				if err != nil {
					return struct{}{}, err
				}
				return struct{}{}, u.Commit(ctx, "race")
			})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var won, lost int
	for err := range results {
		if err == nil {
			won++
		} else {
			require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
			lost++
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, lost)
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))
}

func TestExecutionBacklogCapacityDoesNotBlockControls(t *testing.T) {
	f, agent, _ := commandFixture(t)
	fillExecutionBacklog(t, f, agent, 50)
	u, h := f.handle(t, agent)
	config, err := h.ActivateConfig(t.Context(), agentexecution.ActivateConfigInput{ID: uuid.New(), ConfigID: f.config})
	require.NoError(t, err)
	require.True(t, config.Created)
	require.NoError(t, u.Commit(t.Context(), "config at capacity"))
	require.Equal(t, 50, pendingExecutionCount(t, f, agent))
	u, h = f.handle(t, agent)
	_, err = h.Cancel(t.Context(), agentexecution.CancelInput{Reason: "user_cancel", Message: "cancel"})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "cancel at capacity"))
}

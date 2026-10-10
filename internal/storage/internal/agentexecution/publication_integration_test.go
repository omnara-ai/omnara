//go:build integration

package agentexecution_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/stretchr/testify/require"
)

func TestExecutionCommitRoundTrips(t *testing.T) {
	for _, step := range []string{"model", "tool"} {
		t.Run(step, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			if step == "model" {
				receiveCommand(t, h, "queued", "input")
				_, err := h.AdmitInputs(t.Context())
				require.NoError(t, err)
				prepared := prepareCommand(t, h, lease)
				_, err = h.AcceptOutput(
					t.Context(),
					agentexecution.AcceptOutputInput{
						RuntimeLockID: lease,
						ContextID:     prepared.Context.ID,
						Response:      commandResponse(modelenvelope.StopReasonMaxTokens),
					},
				)
				require.NoError(t, err)
			} else {
				ref := toolCommand(t, h, lease, "built_in")
				_, err := h.AuthorizeTool(t.Context(), ref)
				require.NoError(t, err)
				_,
					err = h.CompleteTool(t.Context(),
					agentexecution.ToolCompletion{ToolRef: ref,
						Outcome: "succeeded",
						Content: []agentexecution.Content{{Kind: "text",
							Text: "done"}}})
				require.NoError(t, err)
			}
			statements, roundTrips, err := agentexecution.CountCommit(t.Context(), u)
			require.NoError(t, err)
			expected := 2
			if step == "model" {
				expected = 1
			}
			require.Equal(t, expected, statements)
			require.Equal(t, expected, roundTrips)
		})
	}
	for _, size := range []int{2, 1000} {
		t.Run(map[int]string{2: "parent_child", 1000: "wide"}[size], func(t *testing.T) {
			f := migratedExecutionFixture(t)
			root := f.create(t, uuid.New(), uuid.Nil)
			ids := []uuid.UUID{root}
			for i := 1; i < size; i++ {
				id := uuid.New()
				f.create(t, id, root)
				ids = append(ids, id)
			}
			cell := agentexecution.NewCell("test", f.pool, nil, nil)
			u, err := cell.Begin(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { _ = u.Rollback(t.Context()) })
			refs := make([]lifecyclelock.AgentRef, len(ids))
			for i, id := range ids {
				refs[i] = lifecyclelock.AgentRef{ProjectID: f.project, AgentID: id}
			}
			require.NoError(t, u.LockAgentRefs(t.Context(), refs, agentexecution.LifecycleAuthority{}))
			for _, id := range ids {
				h, err := u.Agent(
					agentexecution.AgentRoute{
						CellID:      "test",
						ProjectID:   f.project,
						AgentID:     id,
						RootAgentID: root,
					},
				)
				require.NoError(t, err)
				receiveCommand(t, h, "queued", "input")
			}
			statements, roundTrips, err := agentexecution.CountCommit(t.Context(), u)
			require.NoError(t, err)
			require.Equal(t, 2*size, statements)
			require.Equal(t, 2, roundTrips)
			var heads, wakeups int
			require.NoError(
				t,
				f.pool.QueryRow(
					t.Context(),
					`SELECT (SELECT count(*) FROM agent_execution_state WHERE logical_ready_at IS NOT NULL),(SELECT count(*) FROM agent_wakeups)`,
				).
					Scan(&heads, &wakeups),
			)
			require.Equal(t, size, heads)
			require.Equal(t, size, wakeups)

			u, err = cell.Begin(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { _ = u.Rollback(t.Context()) })
			require.NoError(t, u.LockAgentRefs(t.Context(), refs, agentexecution.LifecycleAuthority{}))
			routes := make([]agentexecution.AgentRoute, len(ids))
			for i, id := range ids {
				routes[i] = agentexecution.AgentRoute{
					CellID:      "test",
					ProjectID:   f.project,
					AgentID:     id,
					RootAgentID: root,
				}
			}
			require.NoError(t, u.ArchiveAgents(t.Context(), routes, uuid.Nil))
			statements, roundTrips, err = agentexecution.CountCommit(t.Context(), u)
			require.NoError(t, err)
			require.Equal(t, 2*size, statements)
			require.Equal(t, 2, roundTrips)
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_execution_state
WHERE logical_ready_at IS NOT NULL OR turn_continuable OR incomplete_tools`).Scan(&heads))
			require.Zero(t, heads)
			require.NoError(
				t,
				f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_wakeups`).Scan(&wakeups),
			)
			require.Zero(t, wakeups)
		})
	}
}

func TestExecutionPublicationFailureRollsBack(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	input := receiveCommand(t, h, "queued", "must roll back")
	_, err := u.DB().Exec(t.Context(), `DELETE FROM agent_execution_state WHERE agent_id=$1`, a)
	require.NoError(t, err)
	require.Error(t, u.Commit(t.Context(), "missing head"))
	require.NoError(t, u.Rollback(t.Context()))
	var exists bool
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM agent_inputs WHERE id=$1)`, input.ID).
			Scan(&exists),
	)
	require.False(t, exists)
}

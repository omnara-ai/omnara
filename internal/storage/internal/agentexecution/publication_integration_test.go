//go:build integration

package agentexecution_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/notifications"
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
				_, err = h.CompleteTool(t.Context(),
					agentexecution.ToolCompletion{ToolRef: ref,
						Outcome: "succeeded",
						Content: []agentexecution.Content{{Kind: "text",
							Text: "done"}}})
				require.NoError(t, err)
			}
			statements, roundTrips, err := agentexecution.CountCommit(t.Context(), u)
			require.NoError(t, err)
			expectedStatements, expectedRoundTrips := 2, 2
			if step == "tool" {
				expectedStatements, expectedRoundTrips = 3, 3
			}
			require.Equal(t, expectedStatements, statements)
			require.Equal(t, expectedRoundTrips, roundTrips)
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
			cell := agentexecution.NewCell(f.pool, nil, nil)
			u, err := agentexecution.BeginTestUnit(t.Context(), cell)
			require.NoError(t, err)
			t.Cleanup(func() { _ = u.Rollback(t.Context()) })
			refs := make([]lifecyclelock.AgentRef, len(ids))
			for i, id := range ids {
				refs[i] = lifecyclelock.AgentRef{ProjectID: f.project, AgentID: id}
			}
			require.NoError(t, u.LockAgentRefs(t.Context(), refs, nil))
			for _, id := range ids {
				h, err := u.Agent(
					agentexecution.AgentRoute{
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
			require.LessOrEqual(t, statements, 2*size)
			require.LessOrEqual(t, roundTrips, 2)
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

			u, err = agentexecution.BeginTestUnit(t.Context(), cell)
			require.NoError(t, err)
			t.Cleanup(func() { _ = u.Rollback(t.Context()) })
			require.NoError(t, u.LockAgentRefs(t.Context(), refs, nil))
			routes := make([]agentexecution.AgentRoute, len(ids))
			for i, id := range ids {
				routes[i] = agentexecution.AgentRoute{
					ProjectID:   f.project,
					AgentID:     id,
					RootAgentID: root,
				}
			}
			for _, route := range routes {
				h, err := u.Agent(route)
				require.NoError(t, err)
				_, err = h.Archive(t.Context(), uuid.Nil)
				require.NoError(t, err)
			}
			statements, roundTrips, err = agentexecution.CountCommit(t.Context(), u)
			require.NoError(t, err)
			require.LessOrEqual(t, statements, 2*size)
			require.LessOrEqual(t, roundTrips, 2)
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_execution_state
WHERE logical_ready_at IS NOT NULL OR incomplete_tools`).Scan(&heads))
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

type publicationCollector struct {
	intents []notifications.PostCommitIntent
}

func (p *publicationCollector) PublishPostCommit(_ context.Context, intent notifications.PostCommitIntent) {
	p.intents = append(p.intents, intent)
}

func TestUnchangedToolReadinessPreservesNotificationsAndWebhooks(t *testing.T) {
	f, a := stepFixture(t)
	u, _, work, prepared := claimPreparedStep(t, f)
	require.NoError(t, u.Commit(t.Context(), "prepare"))
	u, h := f.handle(t, a)
	output, err := h.AcceptOutput(t.Context(), stepToolOutput(prepared, work.RuntimeID, 1))
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "output"))
	var before, after []byte
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT to_jsonb(h) FROM agent_execution_state h WHERE agent_id=$1`, a).Scan(&before))
	u, h = f.handle(t, a)
	trace, err := agentexecution.TraceExecutionCommand(u, func() error {
		work, err = h.Claim(t.Context(), uuid.New(), time.Minute)
		if err != nil {
			return err
		}
		return u.Commit(t.Context(), "claim tools")
	})
	require.NoError(t, err)
	publications := 0
	for _, statement := range trace {
		if strings.Contains(statement.Query.SQL, "name: PublishExecution") {
			publications++
		}
	}
	require.Zero(t, publications)
	var wakeups int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&wakeups))
	require.Zero(t, wakeups)
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT to_jsonb(h) FROM agent_execution_state h WHERE agent_id=$1`, a).Scan(&after))
	require.JSONEq(t, string(before), string(after))
	collector := &publicationCollector{}
	u, err = agentexecution.BeginTestUnit(t.Context(), agentexecution.NewCell(f.pool, collector, nil))
	require.NoError(t, err)
	defer func() { _ = u.Rollback(t.Context()) }()
	h, err = agentexecution.LockTestAgent(t.Context(), u, f.project, a, nil)
	require.NoError(t, err)
	trace, err = agentexecution.TraceExecutionCommand(u, func() error {
		_, err := h.AuthorizeTool(t.Context(), agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: work.RuntimeID})
		if err != nil {
			return err
		}
		require.Empty(t, collector.intents)
		return u.Commit(t.Context(), "authorize")
	})
	require.NoError(t, err)
	for _, statement := range trace {
		require.NotContains(t, statement.Query.SQL, "name: PublishExecution")
		require.NotContains(t, statement.Query.SQL, "name: LoadExecutionFacts")
		require.NotContains(t, statement.Query.SQL, "name: LoadExecutionContexts")
	}
	require.Equal(t, []notifications.PostCommitIntent{
		notifications.ToolCallUpdatedCommitted{AgentID: a, ToolCallID: output.Tools[0].ID, State: "ready"},
	}, collector.intents)
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT to_jsonb(h) FROM agent_execution_state h WHERE agent_id=$1`, a).Scan(&after))
	require.JSONEq(t, string(before), string(after))
	var states []string
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT array_agg(tool_state ORDER BY created_at,id) FROM event_webhook_deliveries WHERE tool_call_id=$1`,
		output.Tools[0].ID).Scan(&states))
	require.Equal(t, []string{"awaiting_authorization", "ready"}, states)
}

func TestCompletedToolBatchFactsMatchReconstruction(t *testing.T) {
	for _, size := range []int{1, 2} {
		for _, inline := range []bool{false, true} {
			t.Run(strconv.Itoa(size)+"/inline="+strconv.FormatBool(inline), func(t *testing.T) {
				f, a := stepFixture(t)
				u, _, work, prepared := claimPreparedStep(t, f)
				require.NoError(t, u.Commit(t.Context(), "prepare"))
				u, h := f.handle(t, a)
				output, err := h.AcceptOutput(t.Context(), stepToolOutput(prepared, work.RuntimeID, size))
				require.NoError(t, err)
				require.NoError(t, u.Commit(t.Context(), "output"))
				u, h = f.handle(t, a)
				tools, err := h.Claim(t.Context(), uuid.New(), time.Minute)
				require.NoError(t, err)
				if !inline {
					require.NoError(t, u.Commit(t.Context(), "claim tools"))
					u, h = f.handle(t, a)
				}
				for _, call := range output.Tools {
					_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{
						ToolRef:   agentexecution.ToolRef{ID: call.ID, RuntimeLockID: tools.RuntimeID},
						Authorize: true, Outcome: "succeeded",
					})
					require.NoError(t, err)
				}
				finished, transferred, err := h.FinishToolBatch(t.Context(), tools.RuntimeID, output.ID)
				require.NoError(t, err)
				require.True(t, finished)
				require.False(t, transferred)
				trace, err := agentexecution.TraceExecutionCommand(u, func() error {
					return u.Commit(t.Context(), "complete batch")
				})
				require.NoError(t, err)
				facts := 0
				for _, statement := range trace {
					require.NotContains(t, statement.Query.SQL, "name: LoadExecutionContexts")
					if strings.Contains(statement.Query.SQL, "name: LoadExecutionFacts") {
						facts++
						require.NotNil(t, statement.Query.Arguments[1])
					}
				}
				wantFacts := 1
				if inline {
					wantFacts = 0
				}
				require.Equal(t, wantFacts, facts)
				u, h = f.handle(t, a)
				rebuilt, err := h.ReconstructExecution(t.Context())
				require.NoError(t, err)
				var ready time.Time
				require.NoError(t, u.DB().QueryRow(t.Context(),
					`SELECT logical_ready_at FROM agent_execution_state WHERE agent_id=$1`, a).Scan(&ready))
				require.Equal(t, rebuilt.Head.LogicalReadyAt, &ready)
			})
		}
	}
}

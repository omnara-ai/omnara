//go:build integration

package agentexecution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestExecutionGoverningCompactionRetainsTerminalIdentity(t *testing.T) {
	f := releasedExecutionFixture(t)
	agent := f.agent(t)
	f.opening(t, agent, 1, uuid.Nil, "content")
	f.opening(t, agent, 2, uuid.Nil, "content")
	normal := f.modelContext(t, agent, 2, "normal", nil, 1)
	f.fail(t, normal, "compact")
	larger := f.modelContext(t, agent, 2, "compaction", new(int64(2)), 1)
	f.fail(t, larger, "reduce_compaction_source")
	smaller := f.modelContext(t, agent, 2, "compaction", new(int64(1)), 1)
	f.fail(t, smaller, "retry")
	latest := f.modelContext(t, agent, 2, "compaction", new(int64(1)), 2)
	_, err := f.pool.Exec(
		t.Context(),
		`UPDATE model_call_contexts SET state='canceled',error_kind='cancel',completed_at=$2 WHERE id=$1`,
		latest,
		f.now,
	)
	require.NoError(t, err)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	_, h := f.handle(t, agent)
	snapshot, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	rebuilt, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, snapshot.Head, rebuilt.Head)
	require.Equal(t, latest, rebuilt.Head.CompactionContextID)
	require.Nil(t, rebuilt.Selection.Model)
	require.Equal(t, agentexecution.WaitIdle, rebuilt.Selection.Wait)
}

func TestExecutionEmptyAndUnansweredConfigLineage(t *testing.T) {
	f := releasedExecutionFixture(t)
	empty, unanswered := f.agent(t), f.agent(t)
	f.opening(t, empty, 1, uuid.Nil, "config_change")
	_, content := f.opening(t, unanswered, 1, uuid.Nil, "content")
	f.opening(t, unanswered, 2, uuid.Nil, "config_change")
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	for _, id := range []uuid.UUID{empty, unanswered} {
		u, h := f.handle(t, id)
		snapshot, err := h.ReconstructExecution(t.Context())
		require.NoError(t, err)
		if id == empty {
			require.Nil(t, snapshot.Selection.Model)
		} else {
			require.Equal(t, agentexecution.OriginInitial, snapshot.Selection.Model.Origin)
			require.Equal(t, []uuid.UUID{content}, snapshot.Selection.Model.Opening.InputIDs)
		}

		require.NoError(t, u.Rollback(t.Context()))
	}
}

func TestExecutionCutoverRejectsOrphanCompaction(t *testing.T) {
	f := releasedExecutionFixture(t)
	agent := f.agent(t)
	f.opening(t, agent, 1, uuid.Nil, "content")
	f.modelContext(t, agent, 1, "compaction", new(int64(1)), 1)
	require.ErrorContains(t, applyExecutionCutover(t.Context(), f.pool), "unrepresented governing context")
}

func TestExecutionCutoverReconcilesOwnershipAgeAndDeadlines(t *testing.T) {
	f := releasedExecutionFixture(t)
	ready, owned, retry, archived := f.agent(t), f.agent(t), f.agent(t), f.agent(t)
	for _, agent := range []uuid.UUID{ready, owned, retry, archived} {
		f.opening(t, agent, 1, uuid.Nil, "content")
		_, err := f.pool.Exec(t.Context(), `INSERT INTO agent_wakeups(agent_id,ready_at,updated_at)
  VALUES($1,$2::timestamptz-interval '1 hour',$2)`, agent, f.now)
		require.NoError(t, err)
	}
	model := f.modelContext(t, retry, 1, "normal", nil, 1)
	f.fail(t, model, "retry")
	_, err := f.pool.Exec(
		t.Context(),
		`INSERT INTO agent_runtime_locks(agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
 VALUES($1,$2,$3,$3,$3::timestamptz+interval '90 seconds')`,
		owned,
		uuid.New(),
		f.now,
	)
	require.NoError(t, err)
	_, err = f.pool.Exec(
		t.Context(),
		`UPDATE agents SET state='archived',archived_at=$2 WHERE id=$1`,
		archived,
		f.now,
	)
	require.NoError(t, err)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	var queueCount int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_wakeups`).Scan(&queueCount))
	require.Equal(t, 2, queueCount)
	var age, deadline time.Time
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, ready).
			Scan(&age),
	)
	require.Equal(t, f.now.Add(-time.Hour), age.UTC())
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, retry).
			Scan(&deadline),
	)
	require.Equal(t, f.now.Add(time.Hour), deadline.UTC())
	var ownedReady bool
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `
SELECT logical_ready_at IS NOT NULL FROM agent_execution_state WHERE agent_id=$1
`, owned).
			Scan(&ownedReady),
	)
	require.True(t, ownedReady)
	_, h := f.handle(t, archived)
	snapshot, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, agentexecution.WaitArchived, snapshot.Selection.Wait)
	require.Nil(t, snapshot.Head.LogicalReadyAt)
}

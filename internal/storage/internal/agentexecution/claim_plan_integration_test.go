//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestClaimReadyIndexPlan(t *testing.T) {
	f := migratedExecutionFixture(t)
	ctx := t.Context()
	ids := make([]uuid.UUID, 10000)
	for i := range ids {
		ids[i] = uuid.New()
	}
	_, err := f.pool.Exec(ctx, `
INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,created_at,updated_at)
 SELECT id,id,$2,$3,'active',$4,statement_timestamp(),statement_timestamp() FROM unnest($1::uuid[]) id`,
		ids, f.org, f.project, f.config)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO agent_wakeups(agent_id,ready_at,updated_at)
 SELECT id,statement_timestamp()-interval '1 minute',statement_timestamp() FROM unnest($1::uuid[]) id`, ids[:5000])
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `
INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
 SELECT gen_random_uuid(),id,$2,statement_timestamp(),statement_timestamp(),statement_timestamp()+interval '90 seconds'
 FROM unnest($1::uuid[]) id`, ids[5000:], uuid.New())
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `ANALYZE agents; ANALYZE agent_wakeups; ANALYZE agent_runtime_locks`)
	require.NoError(t, err)
	sql, err := os.ReadFile("../../queries/agent_inputs.sql")
	require.NoError(t, err)
	query := strings.Split(string(sql), "-- name: LockClaimableAgentWakeup")[0]
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SET LOCAL plan_cache_mode=force_generic_plan`)
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query).Scan(&raw))
	t.Logf("claim EXPLAIN: %s", raw)
	var plan []struct {
		Plan struct {
			SharedHitBlocks  int `json:"Shared Hit Blocks"`
			SharedReadBlocks int `json:"Shared Read Blocks"`
		}
	}
	require.NoError(t, json.Unmarshal(raw, &plan))
	require.NotContains(t, string(raw), `"Node Type": "Sort"`)
	require.NotContains(t, string(raw), `"Node Type": "Hash"`)
	require.NotContains(t, string(raw), `"Node Type": "Hash Join"`)
	require.Contains(t, string(raw), "agent_wakeups_global_ready_idx")
	buffers := plan[0].Plan.SharedHitBlocks + plan[0].Plan.SharedReadBlocks
	require.LessOrEqual(t, buffers, 40)
}

func TestClaimSkipsLockedCandidates(t *testing.T) {
	for _, blocked := range []string{"wake", "agent", "lease"} {
		t.Run(blocked, func(t *testing.T) {
			f := migratedExecutionFixture(t)
			ids := []uuid.UUID{f.create(t, uuid.New(), uuid.Nil), f.create(t, uuid.New(), uuid.Nil), f.create(t,
				uuid.New(), uuid.Nil)}
			for i, id := range ids {
				_, err := f.pool.Exec(t.Context(), `INSERT INTO agent_wakeups(agent_id,ready_at,
					updated_at) VALUES($1,$2,$2)`, id, f.now.Add(time.Duration(i-3)*time.Second))
				require.NoError(t, err)
			}
			blocker, err := f.pool.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = blocker.Rollback(t.Context()) }()
			switch blocked {
			case "wake":
				_, err = blocker.Exec(t.Context(), `SELECT agent_id FROM agent_wakeups WHERE agent_id=$1 FOR UPDATE`, ids[0])
			case "agent":
				_, err = blocker.Exec(t.Context(), `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, ids[0])
			case "lease":
				_, err = f.pool.Exec(t.Context(), `INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,
					started_at,renewed_at,lease_expires_at) VALUES($1,$2,$3,statement_timestamp(),statement_timestamp(),
					statement_timestamp()+interval '1 minute')`, uuid.New(), ids[0], uuid.New())
			}
			require.NoError(t, err)
			for _, want := range ids[1:] {
				u, err := agentexecution.BeginTestUnit(t.Context(), agentexecution.NewCell(f.pool, nil, nil))
				require.NoError(t, err)
				defer func() { _ = u.Rollback(t.Context()) }()
				route, err := u.ClaimAgent(t.Context())
				require.NoError(t, err)
				require.Equal(t, want, route.AgentID)
			}
		})
	}
}

func TestClaimAdmissionPreparationBoundary(t *testing.T) {
	f, a := stepFixture(t)
	var before int64
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT next_event_sequence-1 FROM agents WHERE id=$1`,
		a).Scan(&before))
	u, _, work, prepared := claimPreparedStep(t, f)
	defer func() { _ = u.Rollback(t.Context()) }()
	var after int64
	require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT next_event_sequence-1 FROM agents WHERE id=$1`,
		a).Scan(&after))
	require.Greater(t, after, before)
	require.Equal(t, after, prepared.Context.InputEventSequence)
	require.Greater(t, prepared.Context.Opening.EventSequence, before)
	require.LessOrEqual(t, prepared.Context.Opening.EventSequence, prepared.Context.InputEventSequence)
	require.NotEmpty(t, work.Admission)
	require.NoError(t, u.Commit(t.Context(), "claim admission boundary"))
	var wakes int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_wakeups WHERE agent_id=$1`,
		a).Scan(&wakes))
	require.Zero(t, wakes)
}

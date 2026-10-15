//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

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

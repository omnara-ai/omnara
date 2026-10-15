//go:build integration

package agentexecution_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestExecutionHeadSamplingCancellationCost(t *testing.T) {
	f := releasedExecutionFixture(t)
	agents := []uuid.UUID{f.agent(t), f.agent(t)}
	for index, agent := range agents {
		count := 1
		if index == 1 {
			count = 500
		}
		for i := range count {
			turn, _ := f.opening(t, agent, int64(2*i+1), uuid.Nil, "content")
			tx, err := f.pool.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()
			id, event := uuid.New(), uuid.New()
			_, err = tx.Exec(t.Context(), `INSERT INTO agent_inputs(
 id,project_id,agent_id,state,input_kind,delivery_mode,control_type,queued_at)
   VALUES($1,$2,$3,'received','control','immediate','cancel_current',now())`, id, f.project, agent)
			require.NoError(t, err)
			_, err = tx.Exec(t.Context(), `INSERT INTO agent_events(
 id,agent_id,turn_id,sequence,event_kind,agent_input_id,created_at,idempotency_key)
   VALUES($1,$2,$3,$4,'agent_input',$5::uuid,now(),'agent_input:'||($5::uuid)::text)`, event, agent, turn, 2*i+2, id)
			require.NoError(t, err)
			_, err = tx.Exec(t.Context(), `UPDATE agent_inputs
 SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now() WHERE id=$1`, id, event)
			require.NoError(t, err)
			_, err = tx.Exec(t.Context(), `UPDATE agent_turns
 SET latest_event_id=$2,latest_semantic_event_id=$2 WHERE id=$1`, turn, event)
			require.NoError(t, err)
			require.NoError(t, tx.Commit(t.Context()))
		}
	}
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	_, err := f.pool.Exec(t.Context(), "VACUUM ANALYZE")
	require.NoError(t, err)
	query := integrationdb.GeneratedQuery(t, "internal/executiondb/reconstruction.sql.go", "LoadExecutionBoundaries")

	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), "SET LOCAL plan_cache_mode=force_generic_plan")
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), "PREPARE boundary_plan(uuid,uuid) AS "+query)
	require.NoError(t, err)
	for index, agent := range agents {
		count := 1
		if index == 1 {
			count = 500
		}
		var plan []byte
		sql := fmt.Sprintf("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE boundary_plan('%s','%s')", f.project, agent)
		require.NoError(t, tx.QueryRow(t.Context(), sql).Scan(&plan))
		t.Logf("cancellations=%d buffers=%d plan=%s", count, integrationdb.PlanBuffers(t, plan), plan)
		require.LessOrEqual(t, integrationdb.PlanBuffers(t, plan), count*5+40)
		integrationdb.ReadPlan(t, plan).Walk(func(node integrationdb.PlanNode) {
			require.NotEqual(t, "Hash Join", node.Type)
			if node.Relation == "agent_inputs" || node.Relation == "agent_events" {
				require.Contains(t, node.Type, "Index")
			}
		})
	}
}

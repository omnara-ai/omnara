//go:build integration

package agentexecution_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionLeaseDeleteHistoryBound(t *testing.T) {
	f, agent, lease := commandFixture(t)
	u, h := f.handle(t, agent)
	receiveCommand(t, h, "queued", "input")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	prepared := prepareCommand(t, h, lease)
	require.NoError(t, u.Commit(t.Context(), "prepare fixture"))
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	output := uuid.New()
	_, err = tx.Exec(t.Context(), `
INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at) VALUES($1,$2,$3,
'tool_use',now())
`, output, agent, prepared.Context.ID)
	require.NoError(t, err)
	previous := 0
	for _, size := range []int{1000, 100000} {
		_, err = tx.Exec(t.Context(), `
INSERT INTO tool_calls(agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
SELECT $1,$2,'history-'||n,'tool','{}','built_in','awaiting_authorization',now() FROM
generate_series($3::int,$4::int) n
`, agent, output, previous+1, size)
		require.NoError(t, err)
		_, err = tx.Exec(t.Context(), `
INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,tool_call_id,
created_at)
SELECT agent_id,'model_output',model_output_id,split_part(provider_call_id,'-',2)::int,'tool_call',id,
created_at FROM tool_calls WHERE agent_id=$1 AND state='awaiting_authorization'
`, agent)
		require.NoError(t, err)
		_, err = tx.Exec(t.Context(), `
UPDATE tool_calls SET state='completed' WHERE agent_id=$1 AND state='awaiting_authorization'
`, agent)
		require.NoError(t, err)
		_, err = tx.Exec(t.Context(), `
INSERT INTO tool_call_results(agent_id,tool_call_id,outcome,completed_at)
SELECT agent_id,id,'succeeded',now() FROM tool_calls WHERE agent_id=$1 AND NOT EXISTS(SELECT 1 FROM
tool_call_results r WHERE r.agent_id=$1 AND r.tool_call_id=tool_calls.id)
`, agent)
		require.NoError(t, err)
		previous = size
		_, err = tx.Exec(t.Context(), `ANALYZE tool_calls; SET LOCAL plan_cache_mode=force_generic_plan`)
		require.NoError(t, err)
		var raw []byte
		require.NoError(t, tx.QueryRow(t.Context(), `
EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT 1 FROM ONLY tool_calls x WHERE runtime_lock_id=$1 FOR KEY
SHARE OF x
`, lease).Scan(&raw))
		results, err := tx.Conn().PgConn().Exec(t.Context(), `
EXPLAIN (GENERIC_PLAN,FORMAT JSON)
 SELECT 1 FROM ONLY tool_calls x WHERE runtime_lock_id=$1 FOR KEY SHARE OF x
`).ReadAll()
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Contains(t, string(results[0].Rows[0][0]), "tool_calls_runtime_reference_idx")
		t.Logf("history=%d RI probe: %s", size, raw)
		assert.NotContains(t, string(raw), `"Node Type": "Seq Scan"`)
		assert.LessOrEqual(t, integrationdb.PlanBuffers(t, raw), 10)
		release, err := tx.Begin(t.Context())
		require.NoError(t, err)
		require.NoError(t, release.QueryRow(t.Context(), `
EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) DELETE FROM agent_runtime_locks WHERE agent_id=$1 AND id=$2
`, agent, lease).Scan(&raw))
		t.Logf("history=%d delete: %s", size, raw)
		assert.LessOrEqual(t, integrationdb.PlanBuffers(t, raw), 20)
		require.NoError(t, release.Rollback(t.Context()))
	}
	_, err = tx.Exec(t.Context(), `
WITH call AS (INSERT INTO tool_calls(agent_id,model_output_id,provider_call_id,name,input,type,state,
created_at)
 VALUES($1,$2,'live','tool','{}','built_in','awaiting_authorization',now()) RETURNING id,created_at)
INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,tool_call_id,
created_at)
 SELECT $1,'model_output',$2,100001,'tool_call',id,created_at FROM call
`, agent, output)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `
UPDATE tool_calls SET state='ready' WHERE agent_id=$1 AND provider_call_id='live'
`, agent)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `
UPDATE tool_calls SET state='running',runtime_lock_id=$2 WHERE agent_id=$1 AND provider_call_id='live'
`, agent, lease)
	require.NoError(t, err)
	var raw []byte
	query := integrationdb.GeneratedQuery(
		t, "internal/executiondb/commands_runtime.sql.go", "ListExecutionUnfinishedTools",
	)
	require.NoError(t, tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, agent,
		nil, lease).Scan(&raw))
	t.Logf("running tools plan=%s", raw)
	require.LessOrEqual(t, integrationdb.PlanBuffers(t, raw), 20)
	require.NotContains(t, string(raw), `"Node Type": "Seq Scan"`)
	_, err = tx.Exec(t.Context(), `DELETE FROM agent_runtime_locks WHERE agent_id=$1 AND id=$2`, agent, lease)
	require.ErrorContains(t, err, "tool_calls_runtime_lock_id_fkey")
}

func TestContextInsertAvoidsSharedScopeLocks(t *testing.T) {
	f := migratedExecutionFixture(t)
	_, err := f.pool.Exec(t.Context(), `CREATE EXTENSION pageinspect`)
	require.NoError(t, err)
	query := `
INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
opening_input_ids,opening_event_sequence)
 VALUES($1,$2,$3,$4,$5,'normal',1,$6,$7,1,$8,now(),ARRAY[$9::uuid],1)
`
	type opening struct{ agent, turn, input uuid.UUID }
	var openings []opening
	for range 2 {
		agent := f.create(t, uuid.New(), uuid.Nil)
		turn, input := f.opening(t, agent, 1, uuid.Nil, "content")
		openings = append(openings, opening{agent, turn, input})
	}
	for _, o := range openings {
		agent, turn, input := o.agent, o.turn, o.input
		tx, err := f.pool.Begin(t.Context())
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(t.Context()) })
		_, err = tx.Exec(t.Context(), `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, agent)
		require.NoError(t, err)
		_, err = tx.Exec(t.Context(), query, uuid.New(), f.org, f.project, agent, turn, f.config,
			f.revision, uuid.New(), input)
		require.NoError(t, err)
	}
	for _, table := range []string{"orgs", "projects", "agent_configs"} {
		n := integrationdb.MultiXacts(t, f.pool, table)
		t.Logf("%s MultiXact tuples=%d", table, n)
		if table == "agent_configs" {
			require.Positive(t, n, table)
		} else {
			require.Zero(t, n, table)
		}
	}
}

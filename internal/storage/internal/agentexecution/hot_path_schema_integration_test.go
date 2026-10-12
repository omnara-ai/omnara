//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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
		assert.LessOrEqual(t, planBuffers(t, raw), 10)
		release, err := tx.Begin(t.Context())
		require.NoError(t, err)
		require.NoError(t, release.QueryRow(t.Context(), `
EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) DELETE FROM agent_runtime_locks WHERE agent_id=$1 AND id=$2
`, agent, lease).Scan(&raw))
		t.Logf("history=%d delete: %s", size, raw)
		assert.LessOrEqual(t, planBuffers(t, raw), 20)
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
	query := generatedQuery(t, "internal/executiondb/commands_runtime.sql.go", "ListExecutionUnfinishedTools")
	require.NoError(t, tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, agent,
		nil, lease).Scan(&raw))
	t.Logf("running tools plan=%s", raw)
	require.LessOrEqual(t, planBuffers(t, raw), 20)
	require.NotContains(t, string(raw), `"Node Type": "Seq Scan"`)
	_, err = tx.Exec(t.Context(), `DELETE FROM agent_runtime_locks WHERE agent_id=$1 AND id=$2`, agent, lease)
	require.ErrorContains(t, err, "tool_calls_runtime_lock_id_fkey")
}

func planBuffers(t *testing.T, raw []byte) int {
	t.Helper()
	var plans []struct {
		Plan struct {
			Hit  int `json:"Shared Hit Blocks"`
			Read int `json:"Shared Read Blocks"`
		}
	}
	require.NoError(t, json.Unmarshal(raw, &plans))
	return plans[0].Plan.Hit + plans[0].Plan.Read
}

func TestExecutionHotPathScopeConstraints(t *testing.T) {
	f, agent, lease := commandFixture(t)
	_, err := f.pool.Exec(t.Context(), `UPDATE orgs SET idempotency_key=NULL; UPDATE projects SET idempotency_key=NULL`)
	require.NoError(t, err)
	other := seedExecutionFixture(t, f.pool)
	project, config := uuid.New(), uuid.New()
	_, err = f.pool.Exec(t.Context(), `
INSERT INTO projects(id,org_id,name,created_at,updated_at) VALUES($1,$2,'other',now(),now())
`, project, f.org)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `
INSERT INTO agent_configs SELECT (jsonb_populate_record(NULL::agent_configs,
to_jsonb(c)||jsonb_build_object('id',$2::uuid,'project_id',$3::uuid))).* FROM agent_configs c WHERE
id=$1
`, f.config, config, project)
	require.NoError(t, err)
	u, h := f.handle(t, agent)
	tool := toolCommand(t, h, lease, "built_in")
	process := processOwner(t, f, u, agent, lease, tool.ID, "queued")
	action := uuid.New()
	_, err = u.DB().Exec(t.Context(), `
INSERT INTO process_actions(id,org_id,project_id,agent_id,process_id,tool_call_id,runtime_lock_id,
action_kind,seq,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,'read',1,now(),now())
`, action, f.org, f.project, agent, process, tool.ID, lease)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "scope fixture"))
	for _, scope := range []struct {
		name                           string
		org, project, config, revision uuid.UUID
	}{
		{"same", f.org, f.project, f.config, f.revision},
		{"other_org", other.org, other.project, other.config, other.revision},
		{"wrong_org", other.org, f.project, f.config, other.revision},
		{"other_project", f.org, project, config, f.revision},
	} {
		t.Run("model/"+scope.name, func(t *testing.T) {
			tx, err := f.pool.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()
			_, err = tx.Exec(t.Context(), `
INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
opening_input_ids,opening_event_sequence)
SELECT $1,$2,$3,c.agent_id,c.turn_id,'normal',2,$4,$5,c.input_event_sequence,c.runtime_lock_id,now(),
c.opening_input_ids,c.opening_event_sequence
 FROM model_call_contexts c WHERE agent_id=$6
`, uuid.New(), scope.org, scope.project, scope.config, scope.revision, agent)
			if scope.name == "same" {
				require.NoError(t, err)
			} else {
				var constraint *pgconn.PgError
				require.ErrorAs(t, err, &constraint)
				require.Equal(t, "23503", constraint.Code)
				require.Equal(t, "model_call_contexts_agent_scope_fkey", constraint.ConstraintName)
			}
		})
	}
	for _, tc := range []struct {
		table string
		id    uuid.UUID
	}{{"processes", process}, {"process_actions", action}} {
		for _, scope := range []struct {
			name         string
			org, project uuid.UUID
		}{{"org", other.org, f.project}, {"project", f.org, project}} {
			t.Run(tc.table+"/"+scope.name, func(t *testing.T) {
				_, err := f.pool.Exec(t.Context(), "UPDATE "+tc.table+
					" SET org_id=$1,project_id=$2 WHERE id=$3", scope.org, scope.project, tc.id)
				var constraint *pgconn.PgError
				require.ErrorAs(t, err, &constraint)
				require.Equal(t, "23503", constraint.Code)
				if scope.name == "org" {
					require.Equal(t, tc.table+"_agent_scope_fkey", constraint.ConstraintName)
				}
			})
		}
	}
	for _, scope := range []struct {
		name    string
		project uuid.UUID
	}{{"org", other.project}, {"project", project}} {
		t.Run("input/"+scope.name, func(t *testing.T) {
			_, err := f.pool.Exec(t.Context(), `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at)
 VALUES($1,$2,'received','content','queued',now())
`, scope.project, agent)
			var constraint *pgconn.PgError
			require.ErrorAs(t, err, &constraint)
			require.Equal(t, "23503", constraint.Code)
			require.Equal(t, "agent_inputs_project_id_agent_id_fkey", constraint.ConstraintName)
		})
	}
	var n int
	require.NoError(t, f.pool.QueryRow(t.Context(), `
SELECT count(*) FROM pg_constraint WHERE conname=ANY($1::text[]) AND convalidated
`,
		[]string{"model_call_contexts_agent_scope_fkey", "processes_agent_scope_fkey",
			"process_actions_agent_scope_fkey"}).Scan(&n))
	require.Equal(t, 3, n)
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conname=ANY($1::text[])`,
		[]string{"model_call_contexts_org_id_fkey", "model_call_contexts_org_id_project_id_fkey",
			"model_call_contexts_project_id_agent_id_fkey", "agent_inputs_project_id_fkey",
			"processes_org_id_fkey", "processes_org_id_project_id_fkey", "processes_project_id_agent_id_fkey",
			"process_actions_org_id_fkey", "process_actions_org_id_project_id_fkey"}).Scan(&n))
	require.Zero(t, n)
}

//go:build integration

package agentexecution_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func generatedQuery(t *testing.T, path, name string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	var query string
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(literal.Value)
		require.NoError(t, err)
		if strings.HasPrefix(text, "-- name: "+name+" ") {
			query = text
		}
		return true
	})
	require.NotEmpty(t, query, name)
	return query
}

func TestExecutionHotPathPlans(t *testing.T) {
	f, agent, lease := commandFixture(t)
	ids := make([]uuid.UUID, 2000)
	for i := range ids {
		ids[i] = uuid.New()
	}
	_, err := f.pool.Exec(t.Context(), `
INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,created_at,updated_at)
 SELECT id,id,$2,$3,'active',$4,now(),now() FROM unnest($1::uuid[]) id
`, ids, f.org, f.project, f.config)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at)
 SELECT $2,id,'received','content',CASE WHEN n%2=0 THEN 'steering' ELSE 'queued' END,now()
 FROM unnest($1::uuid[]) id CROSS JOIN generate_series(1,10) n
`, ids, f.project)
	require.NoError(t, err)
	seed, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = seed.Rollback(t.Context()) }()
	_, err = seed.Exec(t.Context(), `
INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,ordinal,block_kind,text_content,
created_at)
SELECT agent_id,'agent_input',id,0,'text','history',queued_at FROM agent_inputs WHERE
agent_id=ANY($1::uuid[])
`, ids)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
INSERT INTO agent_events(agent_id,turn_id,sequence,event_kind,agent_input_id,is_opening_event,
created_at,idempotency_key)
 SELECT agent_id,agent_id,row_number() OVER(PARTITION BY agent_id ORDER BY id),'agent_input',id,
row_number() OVER(PARTITION BY agent_id ORDER BY id)=1,queued_at,'agent_input:'||id FROM agent_inputs
WHERE agent_id=ANY($1::uuid[])
`, ids)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
 SELECT agent_id,agent_id,1,id,id FROM agent_events WHERE agent_id=ANY($1::uuid[]) AND sequence=10
`, ids)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
UPDATE agent_inputs i SET state='resolved',admitted_event_id=e.id,admitted_at=e.created_at,
resolved_at=e.created_at
FROM agent_events e WHERE i.agent_id=ANY($1::uuid[]) AND e.agent_id=i.agent_id AND
e.agent_input_id=i.id
`, ids)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `ANALYZE agent_inputs; ANALYZE agent_events; ANALYZE agent_turns`)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
opening_input_ids,opening_event_sequence)
SELECT e.agent_id,$2,$3,e.agent_id,e.turn_id,'normal',1,$4,$5,10,gen_random_uuid(),now(),
ARRAY[e.agent_input_id],1
 FROM agent_events e WHERE e.agent_id=ANY($1::uuid[]) AND e.sequence=1
`, ids, f.org, f.project, f.config, f.revision)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at)
 SELECT id,id,id,'end_turn',now() FROM unnest($1::uuid[]) id
`, ids)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,text_content,
created_at)
 SELECT id,'model_output',id,0,'text','answer',now() FROM unnest($1::uuid[]) id
`, ids)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
INSERT INTO agent_events(agent_id,turn_id,sequence,event_kind,model_output_id,created_at)
 SELECT id,id,11,'model_output',id,now() FROM unnest($1::uuid[]) id
`, ids)
	require.NoError(t, err)
	_, err = seed.Exec(t.Context(), `
UPDATE model_call_contexts SET state='succeeded',api_format='openai-responses',api_variant='default',
completed_at=now()
 WHERE agent_id=ANY($1::uuid[])
`, ids)
	require.NoError(t, err)
	require.NoError(t, seed.Commit(t.Context()))
	_, err = f.pool.Exec(t.Context(), `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at)
 SELECT $1,$2,'received','content','queued',now() FROM generate_series(1,10000)
`, f.project, agent)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `
UPDATE agent_inputs SET state='canceled',canceled_at=now() WHERE agent_id=$1
`, agent)
	require.NoError(t, err)
	u, h := f.handle(t, agent)
	for range 100 {
		receiveCommand(t, h, "steering", "transcript")
		_, err = h.AdmitInputs(t.Context())
		require.NoError(t, err)
	}
	snapshot, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "history fixture"))
	var input uuid.UUID
	require.NoError(t, f.pool.QueryRow(t.Context(), `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at) VALUES($1,$2,
'received','content','queued',now()) RETURNING id
`, f.project, agent).Scan(&input))
	_, err = f.pool.Exec(t.Context(), `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at)
 SELECT $2,id,'received','content','queued',now() FROM unnest($1::uuid[]) id
`, ids, f.project)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `
ANALYZE agents; ANALYZE agent_inputs; ANALYZE agent_events; ANALYZE content_blocks; ANALYZE
model_call_contexts; ANALYZE model_outputs
`)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `VACUUM ANALYZE agent_inputs`)
	require.NoError(t, err)
	tests := []struct {
		name, path string
		args       []any
	}{
		{"ListContextEvents", "../dbsqlc/agent_turns.sql.go", []any{f.project, agent, int64(0), int64(100), int32(100)}},
		{"CreateExecutionAttempt", "internal/executiondb/commands_models.sql.go", []any{uuid.New(),
			snapshot.Head.CurrentTurnID, "normal", int32(1), f.config, f.revision, int64(100), nil, lease,
			snapshot.Selection.Model.Opening.InputIDs, snapshot.Selection.Model.Opening.EventSequence, agent,
			f.project}},
		{"AppendExecutionEvent", "internal/executiondb/commands_events.sql.go", []any{uuid.New(), agent,
			snapshot.Head.CurrentTurnID, "agent_input", input, nil, nil, nil, false, nil, f.project}},
		{"InsertExecutionContent", "internal/executiondb/commands_events.sql.go", []any{agent,
			"agent_input", input, nil, nil, int32(0), "text", "input", nil, nil, nil, false, []byte(`{}`), f.project}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.pool.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()
			_, err = tx.Exec(t.Context(), `SET LOCAL plan_cache_mode=force_generic_plan`)
			require.NoError(t, err)
			query := generatedQuery(t, tc.path, tc.name)
			var raw []byte
			for iteration := range 2 {
				nested, err := tx.Begin(t.Context())
				require.NoError(t, err)
				require.NoError(t, nested.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query,
					tc.args...).Scan(&raw))
				t.Logf("%s iteration=%d plan=%s", tc.name, iteration, raw)
				require.NoError(t, nested.Rollback(t.Context()))
			}

		})
	}
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
		var n int
		require.NoError(t, f.pool.QueryRow(t.Context(), `
SELECT count(*) FROM generate_series(0,
pg_relation_size($1::text::regclass)/current_setting('block_size')::int-1) block
 CROSS JOIN LATERAL heap_page_items(get_raw_page($1,block)) item
 CROSS JOIN LATERAL heap_tuple_infomask_flags(item.t_infomask,item.t_infomask2) flags
 WHERE flags.raw_flags @> ARRAY['HEAP_XMAX_IS_MULTI']
`, table).Scan(&n))
		t.Logf("%s MultiXact tuples=%d", table, n)
		if table == "agent_configs" {
			require.Positive(t, n, table)
		} else {
			require.Zero(t, n, table)
		}
	}
}

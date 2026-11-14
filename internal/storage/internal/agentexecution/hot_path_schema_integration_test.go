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
INSERT INTO tool_calls(project_id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
SELECT $5,$1,$2,'history-'||n,'tool','{}','built_in','awaiting_authorization',now() FROM
generate_series($3::int,$4::int) n
`, agent, output, previous+1, size, f.project)
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
WITH call AS (INSERT INTO tool_calls(project_id,agent_id,model_output_id,provider_call_id,name,input,type,state,
created_at)
 VALUES($3,$1,$2,'live','tool','{}','built_in','awaiting_authorization',now()) RETURNING id,created_at)
INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,tool_call_id,
created_at)
 SELECT $1,'model_output',$2,100001,'tool_call',id,created_at FROM call
`, agent, output, f.project)
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

func TestExecutionBudgetReachedPlan(t *testing.T) {
	f := migratedExecutionFixture(t)
	ctx := t.Context()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
INSERT INTO projects(org_id,name,created_at,updated_at)
SELECT $1,'budget-plan-'||n,now(),now() FROM generate_series(1,20) n`, f.org)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
INSERT INTO daily_model_costs(org_id,day,project_id,effective_cost_usd)
SELECT $1,day,p.id,1 FROM generate_series(current_date-1095,current_date,interval '1 day') day
CROSS JOIN projects p WHERE p.org_id=$1`, f.org)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
INSERT INTO budgets(org_id,project_id,interval,amount_usd) VALUES
($1,NULL,'month',1000000),($1,NULL,'week',1000000),($1,$2,'day',1000000)`, f.org, f.project)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `ANALYZE projects; ANALYZE daily_model_costs; ANALYZE budgets;
SET LOCAL plan_cache_mode=force_generic_plan`)
	require.NoError(t, err)
	query := integrationdb.GeneratedQuery(
		t, "internal/executiondb/commands_models.sql.go", "ExecutionBudgetReached",
	)
	var raw []byte
	require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, f.org, f.project, nil,
		uuid.New()).Scan(&raw))
	t.Logf("budget check plan=%s", raw)
	integrationdb.ReadPlan(t, raw).Walk(func(node integrationdb.PlanNode) {
		if node.Relation == "daily_model_costs" {
			require.Equal(t, "daily_model_costs_day_call_idx", node.Index, "%s", raw)
		}
	})
	require.LessOrEqual(t, integrationdb.PlanBuffers(t, raw), 40)
}

func TestBudgetSpendFollowsTheTopLevelAgent(t *testing.T) {
	f := migratedExecutionFixture(t)
	ctx := t.Context()
	profile := func(name string) uuid.UUID {
		id, version := uuid.New(), uuid.New()
		_, err := f.pool.Exec(ctx, `WITH profile AS (
INSERT INTO agent_profiles(id,project_id,name,current_version_id,created_at,updated_at) VALUES($1,$2,$3,$4,now(),now()))
INSERT INTO agent_profile_versions(id,project_id,profile_id,generation,agent_config_id,created_at)
VALUES($4,$2,$1,1,$5,now())`, id, f.project, name, version, f.config)
		require.NoError(t, err)
		return id
	}
	rootProfile, childProfile := profile("budget-root"), profile("budget-child")
	root := f.createWithProfile(t, uuid.New(), uuid.Nil, rootProfile)
	child := f.createWithProfile(t, uuid.New(), root, childProfile)
	openings := map[uuid.UUID][2]uuid.UUID{}
	for _, agent := range []uuid.UUID{root, child} {
		turn, input := f.opening(t, agent, 1, uuid.Nil, "content")
		openings[agent] = [2]uuid.UUID{turn, input}
	}
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	for agent, cost := range map[uuid.UUID]string{root: "1", child: "2"} {
		id := uuid.New()
		_, err = tx.Exec(ctx, `
INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
opening_input_ids,opening_event_sequence)
VALUES($1,$2,$3,$4,$5,'normal',1,$6,$7,1,$8,now(),ARRAY[$9::uuid],1)`,
			id, f.org, f.project, agent, openings[agent][0], f.config, f.revision, uuid.New(), openings[agent][1])
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `
UPDATE model_call_contexts SET state='succeeded',api_format='openai-chat-completions',api_variant='default',
input_tokens_total=10,uncached_input_tokens=10,effective_cost_usd=$2::numeric,effective_cost_kind='estimated',
completed_at=now() WHERE id=$1`, id, cost)
		require.NoError(t, err)
	}
	var profiles []uuid.UUID
	rows, err := tx.Query(ctx, `SELECT agent_profile_id FROM daily_model_costs WHERE org_id=$1`, f.org)
	require.NoError(t, err)
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		profiles = append(profiles, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []uuid.UUID{rootProfile, rootProfile}, profiles,
		"a subagent's spend counts toward its root's profile")
	spent := func(profileID, agentID any) string {
		var cost string
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT effective_cost_usd::text FROM budget_interval_cost($1,$2,$3,$4,'lifetime')`,
			f.org, f.project, profileID, agentID).Scan(&cost))
		return cost
	}
	require.Equal(t, "3.000000000000000", spent(rootProfile, nil))
	require.Equal(t, "0", spent(childProfile, nil))
	require.Equal(t, "3.000000000000000", spent(nil, root), "an agent budget covers its subagents")
	require.Equal(t, "0", spent(nil, child), "only top-level agents have budgets")
}

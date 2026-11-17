//go:build integration

package agentexecution_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
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
INSERT INTO daily_model_costs(org_id,project_id,day,effective_cost_usd)
SELECT $1,scope.id,day,1 FROM generate_series(current_date-1095,current_date,interval '1 day') day
CROSS JOIN (SELECT id FROM projects WHERE org_id=$1 UNION ALL SELECT NULL::uuid) scope`, f.org)
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
	usesScopeIndex := false
	integrationdb.ReadPlan(t, raw).Walk(func(node integrationdb.PlanNode) {
		if node.Relation == "daily_model_costs" {
			require.NotEqual(t, "Seq Scan", node.Type, "%s", raw)
		}
		usesScopeIndex = usesScopeIndex || node.Index == "daily_model_costs_scope_day_idx"
	})
	require.True(t, usesScopeIndex, "%s", raw)
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
	loner := f.create(t, uuid.New(), uuid.Nil)
	openings := map[uuid.UUID][2]uuid.UUID{}
	for _, agent := range []uuid.UUID{root, child, loner} {
		turn, input := f.opening(t, agent, 1, uuid.Nil, "content")
		openings[agent] = [2]uuid.UUID{turn, input}
	}
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	for agent, cost := range map[uuid.UUID]string{root: "1", child: "2", loner: "4"} {
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
	pending := map[[2]string]int{}
	rows, err := tx.Query(ctx, `
SELECT coalesce(agent_profile_id::text,''),agent_id::text FROM pending_model_costs WHERE org_id=$1 AND project_id=$2`,
		f.org, f.project)
	require.NoError(t, err)
	for rows.Next() {
		var call [2]string
		require.NoError(t, rows.Scan(&call[0], &call[1]))
		pending[call]++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[[2]string]int{{rootProfile.String(), root.String()}: 2, {"", loner.String()}: 1}, pending,
		"a subagent's spend counts toward its top-level agent and that agent's profile")
	spent := func(projectID, profileID, agentID any) string {
		var cost string
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT effective_cost_usd::text FROM budget_interval_cost($1,$2,$3,$4,'lifetime')`,
			f.org, projectID, profileID, agentID).Scan(&cost))
		return cost
	}
	expectSpend := func(message string) {
		require.Equal(t, "7.000000000000000", spent(nil, nil, nil), message)
		require.Equal(t, "7.000000000000000", spent(f.project, nil, nil), message)
		require.Equal(t, "3.000000000000000", spent(f.project, rootProfile, nil), message)
		require.Equal(t, "0", spent(f.project, childProfile, nil), message)
		require.Equal(t, "3.000000000000000", spent(f.project, nil, root), message)
		require.Equal(t, "0", spent(f.project, nil, child), message)
		require.Equal(t, "4.000000000000000", spent(f.project, nil, loner), message)
	}
	expectSpend("pending calls count before they merge")
	merged, err := dbsqlc.New(tx).MergeDailyModelCosts(ctx, dbsqlc.MergeDailyModelCostsParams{LimitCount: 100})
	require.NoError(t, err)
	require.Equal(t, int64(3), merged)
	expectSpend("merging moves spend into scope totals without changing it")
}

func TestMergeKeepsDrainingOtherOrgsWhenOneOrgsTotalsOutgrowACallsCost(t *testing.T) {
	ctx := t.Context()
	large := migratedExecutionFixture(t)
	_, err := large.pool.Exec(ctx, `UPDATE orgs SET idempotency_key='idem-large-org' WHERE id=$1`, large.org)
	require.NoError(t, err)
	other := seedExecutionFixture(t, large.pool, seedExecutionFixtureConfig(t))
	maxCost := "999999999999999.999999999999999"
	type call struct {
		f     executionFixture
		agent uuid.UUID
		cost  string
	}
	calls := []call{
		{large, large.create(t, uuid.New(), uuid.Nil), maxCost},
		{large, large.create(t, uuid.New(), uuid.Nil), maxCost},
		{other, other.create(t, uuid.New(), uuid.Nil), "1"},
	}
	openings := make([][2]uuid.UUID, len(calls))
	for i, c := range calls {
		turn, input := c.f.opening(t, c.agent, 1, uuid.Nil, "content")
		openings[i] = [2]uuid.UUID{turn, input}
	}
	tx, err := large.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	for i, c := range calls {
		id := uuid.New()
		_, err = tx.Exec(ctx, `
INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
opening_input_ids,opening_event_sequence)
VALUES($1,$2,$3,$4,$5,'normal',1,$6,$7,1,$8,now(),ARRAY[$9::uuid],1)`,
			id, c.f.org, c.f.project, c.agent, openings[i][0], c.f.config, c.f.revision, uuid.New(), openings[i][1])
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `
UPDATE model_call_contexts SET state='succeeded',api_format='openai-chat-completions',api_variant='default',
input_tokens_total=10,uncached_input_tokens=10,effective_cost_usd=$2::numeric,effective_cost_kind='estimated',
completed_at=now() WHERE id=$1`, id, c.cost)
		require.NoError(t, err)
	}

	merged, err := dbsqlc.New(tx).MergeDailyModelCosts(ctx, dbsqlc.MergeDailyModelCostsParams{LimitCount: 100})
	require.NoError(t, err)
	require.Equal(t, int64(3), merged)
	for org, want := range map[uuid.UUID]string{
		large.org: "1999999999999999.999999999999998",
		other.org: "1.000000000000000",
	} {
		var spent, pending string
		require.NoError(t, tx.QueryRow(ctx, `
SELECT (SELECT effective_cost_usd::text FROM budget_interval_cost($1,NULL,NULL,NULL,'lifetime')),
       (SELECT count(*)::text FROM pending_model_costs WHERE org_id=$1)`, org).Scan(&spent, &pending))
		require.Equal(t, want, spent)
		require.Equal(t, "0", pending, "every org's pending costs merge")
	}
}

//go:build integration

package dbmigrate_test

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

const rebuildDailyModelCostsMigration = 78

func TestRebuildDailyModelCostsMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, rebuildDailyModelCostsMigration-2))
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	exec := func(statement string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, statement, args...)
		require.NoError(t, err)
	}
	modelID, first, second, definitionID, configID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`WITH model AS (
		INSERT INTO configured_models(id,org_id,model_provider_config_id,name,current_revision_id,
			management_kind,created_at,updated_at) VALUES($1,$2,$3,'usage',$5,'tenant',now(),now()))
		INSERT INTO configured_model_revisions(id,org_id,configured_model_id,model_provider_config_id,
			provider_model_slug,context_window_tokens,max_output_tokens,created_at)
		VALUES($4,$2,$1,$3,'usage-a',10000,1000,now()),($5,$2,$1,$3,'usage-b',10000,1000,now())`,
		modelID, ids.OrgID, ids.ProviderConfigID, first, second)
	exec(`INSERT INTO agent_config_definitions(id,org_id,project_id,configured_model_id,compiled_definition,
		effective_definition_hash,created_at)
		VALUES($1,$2,$3,$4,jsonb_build_object('instruction','Work','model',
			jsonb_build_object('configured_model_id',$5::text)),'usage-hash',now())`,
		definitionID, ids.OrgID, ids.ProjectID, modelID, modelID.String())
	exec(`INSERT INTO agent_configs(id,org_id,project_id,definition_id,created_at) VALUES($1,$2,$3,$4,now())`,
		configID, ids.OrgID, ids.ProjectID, definitionID)
	profile := func(name string) uuid.UUID {
		id, version := uuid.New(), uuid.New()
		exec(`WITH profile AS (
			INSERT INTO agent_profiles(id,project_id,name,current_version_id,created_at,updated_at)
			VALUES($1,$2,$3,$4,now(),now()))
			INSERT INTO agent_profile_versions(id,project_id,profile_id,generation,agent_config_id,created_at)
			VALUES($4,$2,$1,1,$5,now())`, id, ids.ProjectID, name, version, configID)
		return id
	}
	rootProfile, childProfile := profile("usage-root"), profile("usage-child")
	root, child := uuid.New(), uuid.New()
	exec(`INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,agent_profile_id,
		created_at,updated_at) VALUES($1,$1,$2,$3,'active',$4,$5,now(),now())`,
		root, ids.OrgID, ids.ProjectID, configID, rootProfile)
	exec(`INSERT INTO agents(id,root_agent_id,parent_agent_id,subagent_key,org_id,project_id,state,
		current_config_id,agent_profile_id,created_at,updated_at)
		VALUES($1,$2,$2,'fork',$3,$4,'active',$5,$6,now(),now())`,
		child, root, ids.OrgID, ids.ProjectID, configID, childProfile)
	openings := map[uuid.UUID][2]uuid.UUID{}
	for _, agent := range []uuid.UUID{root, child} {
		input, event, turn := uuid.New(), uuid.New(), uuid.New()
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,queued_at)
				VALUES($1,$2,$3,'received','content','queued',now())`, []any{input, ids.ProjectID, agent}},
			{`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,
				is_opening_event,created_at)
				VALUES($1,$2,$3,1,'agent_input','agent_input:'||$4::text,$4::uuid,true,now())`,
				[]any{event, agent, turn, input}},
			{`INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
				VALUES($1,$2,1,$3,$3)`, []any{turn, agent, event}},
			{`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now()
				WHERE id=$1`, []any{input, event}},
		} {
			_, err = tx.ExecContext(ctx, statement.sql, statement.args...)
			require.NoError(t, err)
		}
		require.NoError(t, tx.Commit())
		openings[agent] = [2]uuid.UUID{turn, input}
	}
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -10)
	at := func(days, hours, minutes int) time.Time {
		return day.AddDate(0, 0, days).Add(time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute)
	}
	type call struct {
		agent, revision                        uuid.UUID
		at                                     time.Time
		input, output, cacheRead, reasoningOut any
		reported, cost, kind                   any
	}
	sequence := 0
	record := func(c call) uuid.UUID {
		sequence++
		id := uuid.New()
		exec(`INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,operation_kind,attempt_number,
			agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at,
			turn_id,opening_input_ids,opening_event_sequence)
			VALUES($1,$2,$3,$4,'normal',1,$5,$6,$7,$8,'started',$9,$10,ARRAY[$11::uuid],1)`,
			id, ids.OrgID, ids.ProjectID, c.agent, configID, c.revision, sequence, uuid.New(), c.at,
			openings[c.agent][0], openings[c.agent][1])
		exec(`UPDATE model_call_contexts SET state='failed',recovery_kind='retry',retry_at=now(),
			error_kind='provider',error_message='cut off',
			api_format='openai-chat-completions',
			api_variant='openai',input_tokens_total=$2,uncached_input_tokens=$2::integer-$3::integer,
			cache_read_input_tokens=$3,output_tokens_total=$4,reasoning_output_tokens=$5,
			provider_reported_cost_usd=$6::numeric,effective_cost_usd=$7::numeric,effective_cost_kind=$8,
			completed_at=created_at+interval '1 second' WHERE id=$1`,
			id, c.input, c.cacheRead, c.output, c.reasoningOut, c.reported, c.cost, c.kind)
		return id
	}
	record(call{agent: root, revision: first, at: at(0, 10, 0), input: 100, output: 20, cacheRead: 40,
		reasoningOut: 0, cost: "0.5", kind: "estimated"})
	record(call{agent: root, revision: first, at: at(0, 23, 30), input: 10, output: 5, cacheRead: 0,
		reasoningOut: 0, cost: "0", kind: "custom"})
	record(call{agent: root, revision: first, at: at(1, 1, 0), input: 7, output: 3, cacheRead: 0, reasoningOut: 0})
	record(call{agent: root, revision: second, at: at(1, 12, 0), input: 50, output: 10, cacheRead: 0,
		reasoningOut: 4, reported: "0.25", cost: "0.25", kind: "provider_reported"})
	record(call{agent: child, revision: first, at: at(2, 5, 0), input: 5, output: 5, cacheRead: 0, reasoningOut: 0,
		reported: "0.1"})
	pending := record(call{agent: child, revision: first, at: at(2, 18, 0), input: 8, output: 2, cacheRead: 0,
		reasoningOut: 0, cost: "0.3", kind: "estimated"})
	record(call{agent: child, revision: second, at: at(3, 9, 0)})
	exec(`INSERT INTO pending_model_costs(model_call_context_id,org_id,project_id,agent_profile_id,agent_id,day,
		effective_cost_usd) VALUES($1,$2,$3,$4,$5,$6,0.3)`,
		pending, ids.OrgID, ids.ProjectID, rootProfile, child, at(2, 0, 0))
	exec(`INSERT INTO daily_model_costs(org_id,day,effective_cost_usd) VALUES($1,$2,99)`, ids.OrgID, at(0, 0, 0))
	exec(`INSERT INTO daily_model_costs(org_id,project_id,agent_id,day,effective_cost_usd) VALUES($1,$2,$3,$4,99)`,
		ids.OrgID, ids.ProjectID, root, at(0, 0, 0))

	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, rebuildDailyModelCostsMigration))

	name := map[uuid.UUID]string{root: "root", child: "child", first: "a", second: "b",
		rootProfile: "root-profile", childProfile: "child-profile"}
	rows := func(query string, args ...any) []string {
		t.Helper()
		result, err := db.QueryContext(ctx, query, args...)
		require.NoError(t, err)
		defer func() { _ = result.Close() }()
		columns, err := result.Columns()
		require.NoError(t, err)
		var out []string
		for result.Next() {
			values := make([]sql.NullString, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			require.NoError(t, result.Scan(pointers...))
			fields := make([]string, len(values))
			for i, value := range values {
				fields[i] = value.String
				if id, err := uuid.Parse(value.String); err == nil {
					fields[i] = name[id]
				}
			}
			out = append(out, strings.Join(fields, " "))
		}
		require.NoError(t, result.Err())
		return out
	}
	scopes := rows(`
SELECT CASE WHEN agent_id IS NOT NULL THEN 'agent' WHEN agent_profile_id IS NOT NULL THEN 'profile'
            WHEN project_id IS NOT NULL THEN 'project' ELSE 'org' END,
       coalesce(agent_profile_id::text, '-'), coalesce(agent_id::text, '-'),
       coalesce(configured_model_revision_id::text, '-'), day - $2::date, model_calls,
       model_calls_with_effective_cost, uncached_input_tokens, cache_read_input_tokens, cache_write_input_tokens,
       output_tokens_total, reasoning_output_tokens, trim_scale(effective_cost_usd), model_calls_with_reported_cost,
       trim_scale(provider_reported_cost_usd)
FROM daily_model_costs WHERE org_id = $1`, ids.OrgID, day)
	sort.Strings(scopes)
	require.Equal(t, []string{
		"agent - child a 2 1 1 5 0 0 5 0 0.1 1 0.1",
		"agent - root a 0 2 2 70 40 0 25 0 0.5 0 0",
		"agent - root a 1 1 0 7 0 0 3 0 0 0 0",
		"agent - root b 1 1 1 50 0 0 10 4 0.25 1 0.25",
		"org - - - 0 2 2 70 40 0 25 0 0.5 0 0",
		"org - - - 1 2 1 57 0 0 13 4 0.25 1 0.25",
		"org - - - 2 1 1 5 0 0 5 0 0.1 1 0.1",
		"profile root-profile - - 0 2 2 70 40 0 25 0 0.5 0 0",
		"profile root-profile - - 1 2 1 57 0 0 13 4 0.25 1 0.25",
		"profile root-profile - - 2 1 1 5 0 0 5 0 0.1 1 0.1",
		"project - - - 0 2 2 70 40 0 25 0 0.5 0 0",
		"project - - - 1 2 1 57 0 0 13 4 0.25 1 0.25",
		"project - - - 2 1 1 5 0 0 5 0 0.1 1 0.1",
	}, scopes, "the rebuild replaces old rows, counts calls without a cost, and leaves pending calls for the merge")

	const totals = `sum(model_calls), sum(model_calls_with_effective_cost), sum(input_tokens_total),
       sum(uncached_input_tokens), sum(cache_read_input_tokens), sum(cache_write_input_tokens),
       sum(output_tokens_total), sum(reasoning_output_tokens), trim_scale(sum(effective_cost_usd)),
       sum(model_calls_with_reported_cost), trim_scale(sum(provider_reported_cost_usd))`
	for _, window := range []struct {
		since, until any
		buckets      bool
	}{
		{nil, nil, true},
		{at(0, 12, 0), at(2, 6, 0), true},
		{at(1, 0, 0), at(2, 0, 0), true},
		{at(0, 23, 0), at(1, 2, 0), true},
		{at(0, 9, 0), at(0, 23, 45), true},
		{at(1, 0, 0), time.Now().Add(time.Hour), true},
		{nil, at(1, 12, 0), true},
		{at(0, 0, 0), at(3, 0, 0), false},
	} {
		label := fmt.Sprintf("since %v until %v buckets %v", window.since, window.until, window.buckets)
		expected := rows(`
SELECT agent_id, configured_model_revision_id, count(*),
       count(coalesce(effective_cost_usd, provider_reported_cost_usd)), coalesce(sum(input_tokens_total), 0),
       coalesce(sum(uncached_input_tokens), 0), coalesce(sum(cache_read_input_tokens), 0),
       coalesce(sum(cache_write_input_tokens), 0), coalesce(sum(output_tokens_total), 0),
       coalesce(sum(reasoning_output_tokens), 0),
       trim_scale(coalesce(sum(coalesce(effective_cost_usd, provider_reported_cost_usd)), 0)),
       count(provider_reported_cost_usd), trim_scale(coalesce(sum(provider_reported_cost_usd), 0))
FROM model_call_contexts
WHERE org_id = $1
  AND (input_tokens_total IS NOT NULL OR output_tokens_total IS NOT NULL OR provider_reported_cost_usd IS NOT NULL)
  AND ($2::timestamptz IS NULL OR created_at >= $2) AND ($3::timestamptz IS NULL OR created_at < $3)
GROUP BY agent_id, configured_model_revision_id ORDER BY agent_id, configured_model_revision_id`,
			ids.OrgID, window.since, window.until)
		require.NotEmpty(t, expected, label)
		require.Equal(t, expected, rows(`
SELECT agent_id, configured_model_revision_id, `+totals+`
FROM model_usage_rows($1, $2, $3, $4)
GROUP BY agent_id, configured_model_revision_id ORDER BY agent_id, configured_model_revision_id`,
			ids.OrgID, window.since, window.until, window.buckets), label)
	}

	for _, window := range []struct {
		since, until time.Time
		calls        int
		index        string
	}{
		{at(0, 12, 0), at(2, 6, 0), 2, "model_call_contexts_org_usage_created_idx"},
		{at(1, 0, 0), at(3, 0, 0), 1, "model_call_contexts_pkey"},
	} {
		label := fmt.Sprintf("since %v until %v", window.since, window.until)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`)
		require.NoError(t, err)
		var raw []byte
		require.NoError(t, tx.QueryRowContext(ctx, `EXPLAIN (ANALYZE, FORMAT JSON)
SELECT * FROM model_usage_rows($1, $2, $3, true)`, ids.OrgID, window.since, window.until).Scan(&raw))
		require.NoError(t, tx.Rollback())
		calls, indexes := 0, map[string]bool{}
		integrationdb.ReadPlan(t, raw).Walk(func(node integrationdb.PlanNode) {
			require.NotEqual(t, "Seq Scan", node.Type, "%s: %s", label, raw)
			if node.Relation == "model_call_contexts" && node.Rows > 0 {
				calls += int(node.Rows * node.Loops)
				indexes[node.Index] = true
			}
		})
		require.Equal(t, window.calls, calls, "%s reads only edge-day and pending calls raw: %s", label, raw)
		require.Equal(t, map[string]bool{window.index: true}, indexes, "%s: %s", label, raw)
	}
}

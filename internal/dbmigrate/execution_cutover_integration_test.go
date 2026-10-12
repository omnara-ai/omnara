//go:build integration

package dbmigrate_test

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/dbmigrate"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestExecutionCutoverRemovesHistoryEngine(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fresh", "released", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if mode == "fresh" {
				pool, _ := openPostgresMigrationTestDB(t, ctx)
				assertExecutionCutoverCatalog(t, pool)
				return
			}
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			defer func() { _ = db.Close() }()
			require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 51))
			if mode == "rollback" {
				body, err := os.ReadFile("../../migrations/000052_agent_execution_state.sql")
				require.NoError(t, err)
				body = append(body, []byte("\nSELECT gate_11_cutover_failure();\n")...)
				files := fstest.MapFS{"000052_agent_execution_state.sql": {Data: body}}
				require.ErrorContains(t, dbmigrate.ApplyPostgres(ctx, db, files), "gate_11_cutover_failure")
				require.EqualValues(t, 51, currentPostgresMigrationVersion(t, ctx, db))
				var restored bool
				require.NoError(t, pool.QueryRow(ctx, `
SELECT to_regclass('tool_calls_runtime_lock_idx') IS NOT NULL
 AND to_regclass('tool_calls_runtime_reference_idx') IS NULL
 AND to_regclass('agent_inputs_received_content_idx') IS NULL
 AND to_regclass('agents_org_project_id_key') IS NULL
 AND to_regclass('agent_execution_state') IS NULL
 AND to_regprocedure('agent_payload_created_at(uuid,uuid,text,uuid,boolean)') IS NULL
 AND (SELECT count(*) FROM pg_constraint WHERE conname=ANY($1::text[]))=9
`, replacedExecutionScopeConstraints()).Scan(&restored))
				require.True(t, restored)
			}
			require.NoError(t, applyProductionPostgresMigrations(ctx, db))
			assertExecutionCutoverCatalog(t, pool)
		})
	}
}

func assertExecutionCutoverCatalog(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	names := []string{
		"agent_scheduler_state",
		"model_call_context_turns",
		"agent_stop_events",
		"agent_turn_id_at_event_sequence",
		"agent_turn_opening_content_inputs",
		"agent_latest_turn_id",
		"agent_model_call_opening_content_inputs",
		"agent_latest_unstarted_model_call_opening_inputs",
		"model_call_context_has_later_semantic_event",
		"agent_continuable_model_contexts",
		"agent_unconsumed_context_checkpoint_frontiers",
		"agent_unconsumed_config_change_frontiers",
		"agent_tool_work_frontiers",
		"agent_has_incomplete_tool_batch",
		"agent_model_result_frontiers",
		"agent_next_model_work",
		"agent_next_wakeup_ready_at",
		"refresh_agent_scheduler",
		"stage_agent_scheduler",
		"read_agent_scheduler",
		"flush_scheduler_transaction",
		"omnara.scheduler",
	}
	rows, err := pool.Query(
		ctx,
		`SELECT 'function',p.proname,pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind='f'
 UNION ALL SELECT 'view',c.relname,pg_get_viewdef(c.oid) FROM pg_class c JOIN pg_namespace
 n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('v','m')
 UNION ALL SELECT 'relation',c.relname,'' FROM pg_class c JOIN pg_namespace n ON
 n.oid=c.relnamespace WHERE n.nspname='public'
 UNION ALL SELECT 'trigger',t.tgname,pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN
 pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE
 n.nspname='public' AND NOT t.tgisinternal`,
	)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var kind, name, body string
		require.NoError(t, rows.Scan(&kind, &name, &body))
		for _, removed := range names {
			require.NotEqual(t, removed, name, "%s", kind)
			require.False(
				t,
				strings.Contains(body, removed),
				"%s %s still references %s",
				kind,
				name,
				removed,
			)
		}
	}
	require.NoError(t, rows.Err())
	for _, table := range []string{"model_call_contexts", "processes", "process_actions"} {
		var definition string
		var validated bool
		require.NoError(t, pool.QueryRow(ctx, `
SELECT pg_get_constraintdef(oid),convalidated FROM pg_constraint
 WHERE conrelid=$1::regclass AND conname=$2
`, table, table+"_agent_scope_fkey").Scan(&definition, &validated))
		require.True(t, validated)
		require.Equal(t, "FOREIGN KEY (org_id, project_id, agent_id) REFERENCES agents(org_id, project_id, id)", definition)
	}
	var replaced int
	require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*) FROM pg_constraint WHERE conname=ANY($1::text[])
`, replacedExecutionScopeConstraints()).Scan(&replaced))
	require.Zero(t, replaced)
	for _, index := range []struct {
		name, definition string
		unique           bool
	}{
		{"tool_calls_runtime_reference_idx", "USING btree (runtime_lock_id) WHERE (runtime_lock_id IS NOT NULL)", false},
		{"agent_inputs_received_content_idx", "USING btree (agent_id, input_rank, queued_at, id)", false},
		{"agents_org_project_id_key", "USING btree (org_id, project_id, id)", true},
	} {
		var definition string
		var ready, valid, unique bool
		require.NoError(t, pool.QueryRow(ctx, `
SELECT pg_get_indexdef(indexrelid),indisready,indisvalid,indisunique FROM pg_index WHERE indexrelid=$1::regclass
`, index.name).Scan(&definition, &ready, &valid, &unique))
		require.True(t, ready)
		require.True(t, valid)
		require.Equal(t, index.unique, unique)
		require.Contains(t, definition, index.definition)
		if index.name == "agent_inputs_received_content_idx" {
			require.Contains(t, definition, "state = 'received'::text")
			require.Contains(t, definition, "input_kind = 'content'::text")
		}
	}
	var payloadInstalled, oldIndexAbsent bool
	require.NoError(t, pool.QueryRow(ctx, `
SELECT to_regprocedure('agent_payload_created_at(uuid,uuid,text,uuid,boolean)') IS NOT NULL,
 to_regclass('tool_calls_runtime_lock_idx') IS NULL
`).Scan(&payloadInstalled, &oldIndexAbsent))
	require.True(t, payloadInstalled)
	require.True(t, oldIndexAbsent)
}

func replacedExecutionScopeConstraints() []string {
	return []string{
		"model_call_contexts_org_id_fkey", "model_call_contexts_org_id_project_id_fkey",
		"model_call_contexts_project_id_agent_id_fkey", "agent_inputs_project_id_fkey",
		"processes_org_id_fkey", "processes_org_id_project_id_fkey", "processes_project_id_agent_id_fkey",
		"process_actions_org_id_fkey", "process_actions_org_id_project_id_fkey",
	}
}

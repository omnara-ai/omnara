//go:build integration

package dbmigrate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecutionCutoverRemovesHistoryEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, _ := openPostgresMigrationTestDB(t, ctx)
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
}

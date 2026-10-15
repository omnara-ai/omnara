//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestDaemonPollingPlans(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	startQueuedExpiryProcess(t, ctx, f)
	tx := integrationdb.BeginTx(t, ctx, f.Store.pool)
	query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/processes.sql.go", "ListDaemonProcessOffers")
	prefix := fmt.Sprintf("'%s','%s',600,", f.OrgID, f.MachineID)
	runtime := fmt.Sprintf("'%s','%s'", f.RuntimeID, f.TokenID)
	for _, test := range []struct{ name, sql, args string }{
		{"previous", previousDaemonProcessOffers, prefix + "100," + runtime},
		{"current", query, prefix + runtime + ",100"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := tx.Exec(ctx, "PREPARE polling AS "+test.sql)
			require.NoError(t, err)
			for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
				_, err = tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode)
				require.NoError(t, err)
				var times []float64
				for i := range 5 {
					var raw json.RawMessage
					require.NoError(t, tx.QueryRow(ctx,
						"EXPLAIN (ANALYZE,SUMMARY,FORMAT JSON) EXECUTE polling("+test.args+")").Scan(&raw))
					var plans []struct {
						Planning  float64 `json:"Planning Time"`
						Execution float64 `json:"Execution Time"`
					}
					require.NoError(t, json.Unmarshal(raw, &plans))
					times = append(times, plans[0].Planning)
					t.Logf("%s %s sample %d: planning=%.3fms execution=%.3fms",
						test.name, mode, i, plans[0].Planning, plans[0].Execution)
				}
				if test.name == "current" && mode == "force_custom_plan" {
					slices.Sort(times)
					require.Less(t, times[len(times)/2], 20.0)
				}
			}
			_, err = tx.Exec(ctx, "DEALLOCATE polling")
			require.NoError(t, err)
		})
	}
}

const previousDaemonProcessOffers = `-- name: ListDaemonProcessOffers :many
WITH runtime AS MATERIALIZED (
  SELECT runtime.org_id, runtime.machine_id
  FROM online_daemon_runtimes runtime
  WHERE runtime.id = $5::uuid
    AND runtime.org_id = $1
    AND runtime.machine_id = $2
    AND runtime.daemon_token_id = $6::uuid
)
SELECT process.id,
       process.org_id,
       process.project_id,
       process.agent_id,
       process.tool_call_id,
       process.runtime_lock_id,
       process.agent_machine_binding_id,
       process.machine_id,
       process.execution_granted_at,
       process.cwd,
       process.env,
       process.secret_env,
       process.timeout_seconds,
       process.initial_wait_ms,
       process.default_output_cursor,
       process.state,
       process.state_reason_code,
       process.state_reason_message,
       process.source_started_at,
       process.source_ended_at,
       process.state_changed_at,
       process.exit_code,
       process.exit_signal,
       process.created_at,
       process.updated_at,
       process.last_activity_at,
       process.execution_spec,
       coalesce((CASE WHEN process.execution_spec->>'kind' = 'shell'
                     THEN config.compiled_definition->'git_credentials'->>'integration_id' END)::uuid,
                '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS git_credentials_integration_id
FROM processes process
JOIN runtime ON runtime.org_id = process.org_id
  AND runtime.machine_id = process.machine_id
JOIN agent_machine_bindings binding ON binding.project_id = process.project_id
  AND binding.agent_id = process.agent_id
  AND binding.id = process.agent_machine_binding_id
  AND binding.machine_id = process.machine_id
  AND binding.state = 'attached'
JOIN project_machine_grants pmgrant ON pmgrant.project_id = binding.project_id
  AND pmgrant.machine_id = binding.machine_id
JOIN tool_call_read_projection tool_call ON tool_call.project_id = process.project_id
  AND tool_call.agent_id = process.agent_id AND tool_call.id = process.tool_call_id
JOIN model_call_contexts context ON context.project_id = process.project_id
  AND context.agent_id = process.agent_id AND context.id = tool_call.model_call_context_id
JOIN agent_configs config ON config.project_id = process.project_id AND config.id = context.agent_config_id
WHERE process.org_id = $1
  AND process.machine_id = $2
  AND process.state = 'queued'
  AND process.created_at > statement_timestamp() - ($3::int * interval '1 second')
ORDER BY process.created_at,
       process.id
LIMIT $4
`

func TestDaemonPollingOffersEquivalent(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	tools := createToolCallBatchForProcessTest(t, ctx, f, t.Name(), []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("shell-one", "run_command"),
		builtInProcessToolCallBatchItem("shell-two", "run_command"),
		builtInProcessToolCallBatchItem("upload", "upload_file"),
	})
	for _, id := range tools[:2] {
		startQueuedExpiryProcessForTool(t, ctx, f, id)
	}
	_, err := startProcessForTest(ctx, f.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ToolCallID: tools[2], RuntimeLockID: f.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: f.BindingID, Cwd: "/work",
		ExecutionSpec: processcmd.ForFileTransfer(processcmd.FileTransfer{
			Direction: processcmd.FileTransferUpload, LocalPath: "file.txt",
			Target: processcmd.FileTarget{Artifact: &processcmd.ArtifactTarget{}},
		}),
	})
	require.NoError(t, err)
	query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/processes.sql.go", "ListDaemonProcessOffers")
	for _, test := range []struct {
		name, mutation string
		limit          int
		expected       int
		wrongToken     bool
	}{
		{name: "offered_and_non_shell", limit: 100, expected: 3},
		{name: "limit", limit: 1, expected: 1},
		{name: "revoked_binding", limit: 100,
			mutation: "UPDATE agent_machine_bindings SET state='released' WHERE agent_id=$1"},
		{name: "revoked_grant", limit: 100,
			mutation: `DELETE FROM project_machine_grants
WHERE project_id=(SELECT project_id FROM agents WHERE id=$1)`},
		{name: "offline_runtime", limit: 100,
			mutation: `UPDATE daemon_runtimes SET state='ended',ended_at=statement_timestamp()
WHERE machine_id=(SELECT machine_id FROM agent_machine_bindings WHERE agent_id=$1)`},
		{name: "wrong_token", wrongToken: true, limit: 100},
		{name: "expired_queue", limit: 100,
			mutation: `UPDATE processes SET created_at=statement_timestamp()-interval '1 hour' WHERE agent_id=$1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := integrationdb.BeginTx(t, ctx, f.Store.pool)
			if test.mutation != "" {
				_, err := tx.Exec(ctx, test.mutation, f.AgentID)
				require.NoError(t, err)
			}
			token := f.TokenID
			if test.wrongToken {
				token = uuid.New()
			}
			read := func(sql string, args ...any) []map[string]any {
				rows, err := tx.Query(ctx, sql, args...)
				require.NoError(t, err)
				result, err := pgx.CollectRows(rows, pgx.RowToMap)
				require.NoError(t, err)
				return result
			}
			before := read(previousDaemonProcessOffers, f.OrgID, f.MachineID, 600, test.limit, f.RuntimeID, token)
			after := read(query, f.OrgID, f.MachineID, 600, f.RuntimeID, token, test.limit)
			require.Len(t, before, test.expected)
			require.Equal(t, before, after)
		})
	}
}

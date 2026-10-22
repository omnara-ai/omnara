//go:build integration

package executionstore_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestProcessExpiryCandidatesStayInflightBounded(t *testing.T) {
	store, _ := historyCostFixture(t)
	ctx := t.Context()
	user := mustCreateProjectDeveloperUser(t, ctx, store, "expiry-plans@example.com", "Expiry plans")
	f := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, t.Name(), time.Now().UTC())
	createTerminalProcessActionsForLifecycleTest(t, ctx, f, []terminalProcessActionTestInput{
		{Name: "read", ToolName: "read_process", Kind: executionstore.ProcessActionKindRead},
		{Name: "accepted", ToolName: "write_process", Kind: executionstore.ProcessActionKindWrite, Accepted: true},
	})
	expireDaemonRuntimeForTest(t, ctx, f)
	queued := newProcessDaemonFixtureInStore(t, ctx, store, user.ID, "queued-expiry", time.Now().UTC())
	process := startQueuedExpiryProcess(t, ctx, queued)
	ageQueuedExpiryProcess(t, ctx, queued, process.ID)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	_, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = force_custom_plan`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SELECT history_cost_turn($1,k,false) FROM generate_series(16,2063) k`, f.AgentID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
INSERT INTO machines(id,org_id,source_kind,display_name,provider,lifecycle_state,
 lifecycle_changed_at,created_at,updated_at)
SELECT md5('history-machine-'||n)::uuid,$1,'byo','History machine '||n,'byo','active',now(),now(),now()
FROM generate_series(1,128) n;
INSERT INTO project_machine_grants(org_id,project_id,machine_id,created_at,updated_at)
SELECT $1,md5('history-project-'||n)::uuid,md5('history-machine-'||n)::uuid,now(),now() FROM generate_series(1,128) n;
INSERT INTO agent_machine_bindings(org_id,project_id,agent_id,machine_id,binding_kind,created_at,updated_at)
SELECT $1,md5('history-project-'||n)::uuid,md5('history-agent-'||n)::uuid,md5('history-machine-'||n)::uuid,
 'explicit',now(),now() FROM generate_series(1,128) n;
INSERT INTO processes(org_id,project_id,agent_id,tool_call_id,runtime_lock_id,agent_machine_binding_id,machine_id,
 execution_spec,state,state_reason_code,state_changed_at,created_at,updated_at,last_activity_at)
SELECT binding.org_id,binding.project_id,call.agent_id,call.id,call.id,binding.id,binding.machine_id,
 '{"kind":"shell","shell":{"command":"true","shell_selector":"sh","io_mode":"pipe"}}'::jsonb,
 'failed','historical',now(),now()-interval '2 hours',now(),now()
FROM tool_calls call JOIN agent_machine_bindings binding ON binding.agent_id=call.agent_id
WHERE call.name='list_agents';
INSERT INTO process_actions(org_id,project_id,agent_id,process_id,tool_call_id,runtime_lock_id,action_kind,seq,state,
 created_at,updated_at)
SELECT org_id,project_id,agent_id,id,tool_call_id,runtime_lock_id,'read',1,'applied',created_at,updated_at
FROM processes WHERE state_reason_code='historical';
INSERT INTO daemon_runtimes(org_id,machine_id,daemon_token_id,daemon_instance_id,daemon_version,state,
 created_at,last_seen_at,lease_expires_at,updated_at,ended_at)
SELECT org_id,machine_id,daemon_token_id,md5('history-runtime-'||n)::uuid,daemon_version,'ended',
 now()-interval '3 hours',now()-interval '3 hours',now()-interval '2 hours',now(),now()-interval '2 hours'
FROM daemon_runtimes CROSS JOIN generate_series(1,2048) n WHERE id=$2;
ANALYZE machines,processes,process_actions,daemon_runtimes,tool_calls;`,
		pgx.QueryExecModeSimpleProtocol, f.OrgID, f.RuntimeID)
	require.NoError(t, err)
	queueTimeout := int32(executionstore.ProcessQueueTimeout / time.Second)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		_, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = "+mode)
		require.NoError(t, err)
		query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/process_tool_unreachable.sql.go",
			"ListProcessToolExpiryMachineCandidates")
		raw := integrationdb.ExplainPrepared(t, ctx, tx, query, int32(100), int32(0), queueTimeout)
		t.Logf("%s buffers=%d planning=%d", mode, integrationdb.PlanBuffers(t, raw), integrationdb.PlanningBuffers(t, raw))
		require.Less(t, integrationdb.PlanBuffers(t, raw), 250, "%s", raw)
		seen := map[string]bool{}
		integrationdb.ReadPlan(t, raw).Walk(func(node integrationdb.PlanNode) {
			if node.Loops > 0 {
				seen[node.Index] = true
			}
		})
		require.True(t, seen["processes_unreachable_candidate_idx"], "%s", raw)
		require.True(t, seen["process_actions_unreachable_candidate_idx"], "%s", raw)
		require.True(t, seen["daemon_runtimes_machine_recent_idx"], "%s", raw)
		rows, err := dbsqlc.New(tx).ListProcessToolExpiryMachineCandidates(ctx,
			dbsqlc.ListProcessToolExpiryMachineCandidatesParams{
				MachineUnreachableGraceSeconds: 0, QueueTimeoutSeconds: queueTimeout, LimitCount: 100,
			})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.ElementsMatch(t, []uuid.UUID{f.MachineID, queued.MachineID},
			[]uuid.UUID{rows[0].MachineID, rows[1].MachineID})
	}
}

//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

const (
	smallOnlineFleet           = 100
	largeOnlineFleet           = 1000
	lookupBufferBudget         = 64
	fleetGrowthBufferAllowance = 4
)

const previousMachineConnectionStates = `
SELECT machine.id AS machine_id,
       machine.org_id,
       CASE WHEN EXISTS (
         SELECT 1
         FROM online_daemon_runtimes runtime
         WHERE runtime.org_id = machine.org_id
           AND runtime.machine_id = machine.id
       ) THEN 'online'
       WHEN machine.asleep_since IS NOT NULL
         AND machine.lifecycle_state = 'active'
         AND machine.deleted_at IS NULL THEN 'asleep'
       ELSE 'offline' END AS connection_state
FROM machines machine`

const onlineFleetWithStaleLeaseStatistics = `
INSERT INTO orgs(id, name, created_at, updated_at)
SELECT md5('fleet-org-' || n)::uuid, 'fleet-org-' || n, statement_timestamp(), statement_timestamp()
FROM generate_series(1, %[1]d) n;
INSERT INTO machines(id, org_id, source_kind, display_name, provider, lifecycle_state, lifecycle_changed_at,
                     created_at, updated_at)
SELECT md5('fleet-machine-' || n)::uuid, md5('fleet-org-' || n)::uuid, 'byo', 'fleet-machine', 'byo', 'active',
       statement_timestamp(), statement_timestamp(), statement_timestamp()
FROM generate_series(1, %[1]d) n;
INSERT INTO machine_daemon_tokens(id, org_id, machine_id, name, token_hash, created_at)
SELECT md5('fleet-token-' || n)::uuid, md5('fleet-org-' || n)::uuid, md5('fleet-machine-' || n)::uuid,
       'daemon', 'fleet-token-' || n, statement_timestamp()
FROM generate_series(1, %[1]d) n;
INSERT INTO daemon_runtimes(id, org_id, machine_id, daemon_token_id, daemon_instance_id, daemon_version, state,
                            created_at, last_seen_at, lease_expires_at, updated_at)
SELECT md5('fleet-runtime-' || n)::uuid, md5('fleet-org-' || n)::uuid, md5('fleet-machine-' || n)::uuid,
       md5('fleet-token-' || n)::uuid, md5('fleet-instance-' || n)::uuid, '1.0.0', 'active',
       statement_timestamp() - interval '2 hours', statement_timestamp() - interval '2 hours',
       statement_timestamp() - interval '1 hour' + n * interval '1 second', statement_timestamp()
FROM generate_series(1, %[1]d) n;
INSERT INTO daemon_runtimes(org_id, machine_id, daemon_token_id, daemon_instance_id, daemon_version, state,
                            state_reason_code,
                            created_at, last_seen_at, lease_expires_at, ended_at, updated_at)
SELECT md5('fleet-org-' || n)::uuid, md5('fleet-machine-' || n)::uuid, md5('fleet-token-' || n)::uuid,
       gen_random_uuid(),
       '1.0.0', 'ended', 'daemon_runtime_replaced', statement_timestamp() - interval '3 days',
       statement_timestamp() - interval '3 days', statement_timestamp() - interval '3 days' + interval '2 minutes',
       statement_timestamp() - interval '3 days' + interval '1 minute', statement_timestamp()
FROM generate_series(1, %[1]d) n, generate_series(1, 20) history;
UPDATE machines
SET current_daemon_runtime_id = md5('fleet-runtime-' || n)::uuid
FROM generate_series(1, %[1]d) n
WHERE org_id = md5('fleet-org-' || n)::uuid AND id = md5('fleet-machine-' || n)::uuid;
ANALYZE machines, machine_daemon_tokens, daemon_runtimes, agent_machine_bindings, project_machine_grants;
UPDATE daemon_runtimes
SET last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '1 hour' + n * interval '1 second'
FROM generate_series(1, %[1]d) n
WHERE id = md5('fleet-runtime-' || n)::uuid;`

const machineConnectionCases = `
CREATE TEMP TABLE connection_cases(
  name, pool, lifecycle_state, asleep, runtime_state, runtime_reason, lease_live, token_revoked, runtime_current,
  expected
) ON COMMIT DROP AS VALUES
  ('online', false, 'active', false, 'active', NULL, true, false, true, 'online'),
  ('online_asleep', false, 'active', true, 'active', NULL, true, false, true, 'online'),
  ('expired_lease', false, 'active', false, 'active', NULL, false, false, true, 'offline'),
  ('expired_lease_asleep', false, 'active', true, 'active', NULL, false, false, true, 'asleep'),
  ('revoked_token', false, 'active', false, 'active', NULL, true, true, true, 'offline'),
  ('ended_lease_expired', false, 'active', false, 'ended', 'daemon_lease_expired', false, false, true, 'offline'),
  ('ended_asleep', false, 'active', true, 'ended', 'machine_asleep', false, false, true, 'asleep'),
  ('released_live_lease', false, 'active', false, 'ended', 'daemon_runtime_released', true, false, true, 'offline'),
  ('asleep_live_lease', false, 'active', true, 'ended', 'machine_asleep', true, false, true, 'asleep'),
  ('non_current_runtime', false, 'active', false, 'active', NULL, true, false, false, 'offline'),
  ('no_runtime', false, 'active', false, NULL, NULL, NULL, false, false, 'offline'),
  ('deleted', false, 'deleted', true, 'active', NULL, true, false, true, 'offline'),
  ('pool_deleting', true, 'deleting', true, 'active', NULL, true, false, true, 'offline'),
  ('pool_online', true, 'active', false, 'active', NULL, true, false, true, 'online');
INSERT INTO orgs(id, name, created_at, updated_at)
VALUES (md5('connection-org')::uuid, 'connection-org', statement_timestamp(), statement_timestamp());
INSERT INTO machine_pools(id, org_id, name, management_kind, provider, provider_auth_env_var, max_total_machines,
                           created_at, updated_at)
VALUES (md5('connection-pool')::uuid, md5('connection-org')::uuid, 'connection-pool', 'cluster', 'test.provider',
        'TEST_PROVIDER_TOKEN', 100, statement_timestamp(), statement_timestamp());
INSERT INTO machines(id, org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
                     lifecycle_changed_at,
                     provider_resource_id, cpu, memory_mb, provider_options, next_reconcile_after, asleep_since,
                     deleted_at,
                     created_at, updated_at)
SELECT md5('connection-machine-' || name)::uuid, md5('connection-org')::uuid,
       CASE WHEN pool THEN md5('connection-pool')::uuid END,
       CASE WHEN pool THEN 'pool' ELSE 'byo' END,
       name, CASE WHEN pool THEN 'test.provider' ELSE 'byo' END, lifecycle_state, statement_timestamp(),
       CASE WHEN pool THEN 'resource-' || name END,
       CASE WHEN pool THEN 1 END, CASE WHEN pool THEN 1024 END, CASE WHEN pool THEN '{}'::jsonb END,
       CASE WHEN lifecycle_state NOT IN ('active', 'deleted') THEN statement_timestamp() END,
       CASE WHEN asleep THEN statement_timestamp() - interval '1 hour' END,
       CASE WHEN lifecycle_state = 'deleted' THEN statement_timestamp() END,
       statement_timestamp(), statement_timestamp()
FROM connection_cases;
INSERT INTO machine_daemon_tokens(id, org_id, machine_id, name, token_hash, created_at, revoked_at)
SELECT md5('connection-token-' || name)::uuid, md5('connection-org')::uuid, md5('connection-machine-' || name)::uuid,
       'daemon', 'connection-token-' || name, statement_timestamp() - interval '3 hours',
       CASE WHEN token_revoked THEN statement_timestamp() END
FROM connection_cases;
INSERT INTO daemon_runtimes(id, org_id, machine_id, daemon_token_id, daemon_instance_id, daemon_version, state,
                            state_reason_code, created_at, last_seen_at, lease_expires_at, ended_at, updated_at)
SELECT md5('connection-runtime-' || name)::uuid, md5('connection-org')::uuid, md5('connection-machine-' || name)::uuid,
       md5('connection-token-' || name)::uuid, md5('connection-instance-' || name)::uuid, '1.0.0', runtime_state,
       runtime_reason, statement_timestamp() - interval '2 hours',
       CASE WHEN lease_live THEN statement_timestamp() ELSE statement_timestamp() - interval '2 hours' END,
       CASE WHEN lease_live THEN statement_timestamp() + interval '1 hour'
            ELSE statement_timestamp() - interval '1 hour' END,
       CASE WHEN runtime_state = 'ended' THEN statement_timestamp() END,
       statement_timestamp()
FROM connection_cases
WHERE runtime_state IS NOT NULL;
UPDATE machines machine
SET current_daemon_runtime_id = md5('connection-runtime-' || connection_case.name)::uuid
FROM connection_cases connection_case
WHERE machine.org_id = md5('connection-org')::uuid
  AND machine.id = md5('connection-machine-' || connection_case.name)::uuid
  AND connection_case.runtime_current;`

type machineConnectionLookup struct {
	file, name string
	args       []any
}

func TestMachineConnectionStatePlansAndResults(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	tools := createToolCallBatchForProcessTest(t, ctx, f, t.Name(), []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("started", "run_command"),
		builtInProcessToolCallBatchItem("inserted", "run_command"),
		builtInProcessToolCallBatchItem("action", "write_process"),
	})
	started := startQueuedExpiryProcessForTool(t, ctx, f, tools[0])
	_, accepted, err := acceptDaemonProcessForTest(ctx, f.Store, f.OrgID, f.MachineID, f.RuntimeID, started.ID)
	require.NoError(t, err)
	require.True(t, accepted)

	tx := integrationdb.BeginTx(t, ctx, f.Store.pool)
	var startedSpec, startedCwd, startedEnv, startedSecretEnv string
	var startedTimeout, startedInitialWait int32
	require.NoError(t, tx.QueryRow(ctx, `
SELECT execution_spec::text, cwd, env::text, secret_env::text, timeout_seconds, initial_wait_ms
FROM processes
WHERE id = $1`, started.ID,
	).Scan(&startedSpec, &startedCwd, &startedEnv, &startedSecretEnv, &startedTimeout, &startedInitialWait))
	var wake dbsqlc.GetMachineWakeStateParams
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT md5('connection-org')::uuid, md5('connection-machine-pool_online')::uuid, md5('connection-pool')::uuid`,
	).Scan(&wake.OrgID, &wake.MachineID, &wake.MachinePoolID))
	blocker := dbsqlc.GetProcessActionCreateBlockerParams{
		ProjectID: testProjectID, AgentID: f.AgentID, ProcessID: started.ID,
	}
	lookups := []machineConnectionLookup{
		{"execution_resources", "GetMachine", []any{f.OrgID, f.MachineID}},
		{"machine_daemon", "GetMachineWakeState", []any{wake.OrgID, wake.MachineID, wake.MachinePoolID}},
		{"agent_machine_bindings", "ListExecutableAgentMachineBindings", []any{testProjectID, f.AgentID}},
		{"processes", "InsertProcess", []any{
			startedSpec, startedCwd, startedEnv, startedSecretEnv, startedTimeout, startedInitialWait,
			tools[1], f.BindingID, testProjectID, f.AgentID, f.Lock.ID,
		}},
		{"process_actions", "GetProcessActionCreateBlocker", []any{blocker.ProjectID, blocker.AgentID, blocker.ProcessID}},
		{"process_actions", "InsertProcessAction", []any{
			tools[2], f.Lock.ID, "write", `{"data":""}`, testProjectID, f.AgentID, started.ID,
		}},
	}

	buffers := map[int]map[string]int{}
	for _, fleetSize := range []int{smallOnlineFleet, largeOnlineFleet} {
		_, err := tx.Exec(ctx, "SAVEPOINT fleet")
		require.NoError(t, err)
		_, err = tx.Exec(ctx, fmt.Sprintf(onlineFleetWithStaleLeaseStatistics, fleetSize))
		require.NoError(t, err)
		_, err = tx.Exec(ctx, machineConnectionCases)
		require.NoError(t, err)
		requireMachineConnectionStates(t, tx, fleetSize)
		requireOnlineQueryResults(t, tx, []uuid.UUID{f.MachineID, wake.MachineID}, wake, blocker)
		buffers[fleetSize] = lookupPlanBuffers(t, tx, lookups)
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT fleet")
		require.NoError(t, err)
	}
	require.Len(t, buffers[smallOnlineFleet], 2*len(lookups))
	for _, label := range slices.Sorted(maps.Keys(buffers[smallOnlineFleet])) {
		small, large := buffers[smallOnlineFleet][label], buffers[largeOnlineFleet][label]
		t.Logf("%s: %d buffers at %d online runtimes, %d at %d", label, small, smallOnlineFleet, large, largeOnlineFleet)
		require.LessOrEqual(t, small, lookupBufferBudget, label)
		require.LessOrEqual(t, large, lookupBufferBudget, label)
		require.LessOrEqual(t, large, small+fleetGrowthBufferAllowance, label)
	}
}

func requireMachineConnectionStates(t *testing.T, tx pgx.Tx, fleetSize int) {
	t.Helper()
	ctx := t.Context()
	rows, err := tx.Query(ctx, `
SELECT connection_case.name, connection_case.expected, state.connection_state
FROM connection_cases connection_case
JOIN machine_connection_states state ON state.org_id = md5('connection-org')::uuid
  AND state.machine_id = md5('connection-machine-' || connection_case.name)::uuid`)
	require.NoError(t, err)
	expected, actual := map[string]string{}, map[string]string{}
	var name, expectedState, actualState string
	_, err = pgx.ForEachRow(rows, []any{&name, &expectedState, &actualState}, func() error {
		expected[name], actual[name] = expectedState, actualState
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, rows.Err())
	require.Len(t, expected, 14)
	require.Equal(t, expected, actual)
	var online, differences int
	require.NoError(t, tx.QueryRow(ctx, `
WITH previous AS (`+previousMachineConnectionStates+`)
SELECT (SELECT count(*) FROM machine_connection_states WHERE connection_state = 'online'),
       (SELECT count(*) FROM ((TABLE previous EXCEPT ALL TABLE machine_connection_states)
         UNION ALL (TABLE machine_connection_states EXCEPT ALL TABLE previous)) difference)`,
	).Scan(&online, &differences))
	require.Greater(t, online, fleetSize)
	require.Zero(t, differences)
}

func requireOnlineQueryResults(
	t *testing.T,
	tx pgx.Tx,
	machines []uuid.UUID,
	wake dbsqlc.GetMachineWakeStateParams,
	blocker dbsqlc.GetProcessActionCreateBlockerParams,
) {
	t.Helper()
	ctx := t.Context()
	queries := dbsqlc.New(tx)
	for _, transition := range []struct{ state, runtimeEndReason string }{
		{"online", ""},
		{"asleep", "machine_asleep"},
		{"offline", "daemon_runtime_released"},
	} {
		_, err := tx.Exec(ctx, "SAVEPOINT connection")
		require.NoError(t, err)
		if transition.runtimeEndReason != "" {
			_, err = tx.Exec(ctx, `
UPDATE daemon_runtimes runtime
SET state = 'ended', state_reason_code = $2, ended_at = statement_timestamp(), updated_at = statement_timestamp()
FROM machines machine
WHERE machine.id = ANY($1)
  AND runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id`, machines, transition.runtimeEndReason)
			require.NoError(t, err)
		}
		if transition.state == "asleep" {
			_, err = tx.Exec(ctx, `UPDATE machines SET asleep_since = statement_timestamp() WHERE id = ANY($1)`, machines)
			require.NoError(t, err)
		}
		var states []string
		require.NoError(t, tx.QueryRow(ctx,
			`SELECT array_agg(connection_state) FROM machine_connection_states WHERE machine_id = ANY($1)`, machines,
		).Scan(&states))
		require.Equal(t, []string{transition.state, transition.state}, states)
		wakeState, err := queries.GetMachineWakeState(ctx, wake)
		require.NoError(t, err)
		require.Equal(t, transition.state == "online", wakeState.Online, transition.state)
		blockerState, err := queries.GetProcessActionCreateBlocker(ctx, blocker)
		require.NoError(t, err)
		require.Equal(t, transition.state == "online", blockerState.HasOnlineRuntime, transition.state)
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT connection")
		require.NoError(t, err)
	}
}

func lookupPlanBuffers(t *testing.T, tx pgx.Tx, lookups []machineConnectionLookup) map[string]int {
	t.Helper()
	ctx := t.Context()
	buffers := map[string]int{}
	for _, lookup := range lookups {
		query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/"+lookup.file+".sql.go", lookup.name)
		_, err := tx.Exec(ctx, "PREPARE lookup AS "+query)
		require.NoError(t, err)
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			label := lookup.name + " " + mode
			_, err := tx.Exec(ctx, "SAVEPOINT lookup; SET LOCAL plan_cache_mode = "+mode)
			require.NoError(t, err)
			var raw json.RawMessage
			require.NoError(t, tx.QueryRow(ctx,
				"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE lookup("+sqlArguments(lookup.args)+")",
			).Scan(&raw))
			plan := integrationdb.ReadPlan(t, raw)
			require.Equal(t, 1.0, plan.Rows, label)
			currentRuntimeProbes := 0
			plan.Walk(func(node integrationdb.PlanNode) {
				if node.Relation != "daemon_runtimes" {
					return
				}
				require.LessOrEqual(t, node.RowsVisited(), 1.0, "%s: %s", label, raw)
				if node.Type == "Index Scan" && node.Loops*node.Rows == 1 &&
					(node.Index == "daemon_runtimes_pkey" || node.Index == "daemon_runtimes_one_active_idx") {
					currentRuntimeProbes++
				}
			})
			require.Positive(t, currentRuntimeProbes, "%s: %s", label, raw)
			buffers[label] = integrationdb.PlanBuffers(t, raw)
			_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT lookup")
			require.NoError(t, err)
		}
		_, err = tx.Exec(ctx, "DEALLOCATE lookup")
		require.NoError(t, err)
	}
	return buffers
}

func sqlArguments(args []any) string {
	formatted := make([]string, len(args))
	for i, arg := range args {
		formatted[i] = "'" + strings.ReplaceAll(fmt.Sprint(arg), "'", "''") + "'"
	}
	return strings.Join(formatted, ",")
}

//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

const onlineFleet = 1000

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
       statement_timestamp() - interval '1 hour', statement_timestamp()
FROM generate_series(1, %[1]d) n;
UPDATE machines
SET current_daemon_runtime_id = md5('fleet-runtime-' || n)::uuid
FROM generate_series(1, %[1]d) n
WHERE org_id = md5('fleet-org-' || n)::uuid AND id = md5('fleet-machine-' || n)::uuid;
ANALYZE machines, machine_daemon_tokens, daemon_runtimes;
UPDATE daemon_runtimes
SET last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '1 hour'
FROM generate_series(1, %[1]d) n
WHERE id = md5('fleet-runtime-' || n)::uuid;`

const machineConnectionCases = `
CREATE TEMP TABLE connection_cases(
  name, pool, lifecycle_state, asleep, lease_live, token_revoked, runtime_current, expected
) AS VALUES
  ('online_asleep', false, 'active', true, true, false, true, 'online'),
  ('expired_lease_asleep', false, 'active', true, false, false, true, 'asleep'),
  ('revoked_token', false, 'active', false, true, true, true, 'offline'),
  ('non_current_runtime', false, 'active', false, true, false, false, 'offline'),
  ('deleted', false, 'deleted', true, true, false, true, 'offline'),
  ('pool_online', true, 'active', false, true, false, true, 'online');
INSERT INTO orgs(id, name, created_at, updated_at)
VALUES (md5('connection-org')::uuid, 'connection-org', statement_timestamp(), statement_timestamp());
INSERT INTO machine_pools(id, org_id, name, management_kind, provider, provider_auth_env_var, max_total_machines,
                           created_at, updated_at)
VALUES (md5('connection-pool')::uuid, md5('connection-org')::uuid, 'connection-pool', 'cluster', 'test.provider',
        'TEST_PROVIDER_TOKEN', 100, statement_timestamp(), statement_timestamp());
INSERT INTO machines(id, org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
                     lifecycle_changed_at, provider_resource_id, cpu, memory_mb, provider_options, asleep_since,
                     deleted_at, created_at, updated_at)
SELECT md5('connection-machine-' || name)::uuid, md5('connection-org')::uuid,
       CASE WHEN pool THEN md5('connection-pool')::uuid END,
       CASE WHEN pool THEN 'pool' ELSE 'byo' END,
       name, CASE WHEN pool THEN 'test.provider' ELSE 'byo' END, lifecycle_state, statement_timestamp(),
       CASE WHEN pool THEN 'resource-' || name END,
       CASE WHEN pool THEN 1 END, CASE WHEN pool THEN 1024 END, CASE WHEN pool THEN '{}'::jsonb END,
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
                            created_at, last_seen_at, lease_expires_at, updated_at)
SELECT md5('connection-runtime-' || name)::uuid, md5('connection-org')::uuid, md5('connection-machine-' || name)::uuid,
       md5('connection-token-' || name)::uuid, md5('connection-instance-' || name)::uuid, '1.0.0', 'active',
       statement_timestamp() - interval '2 hours',
       CASE WHEN lease_live THEN statement_timestamp() ELSE statement_timestamp() - interval '2 hours' END,
       CASE WHEN lease_live THEN statement_timestamp() + interval '1 hour'
            ELSE statement_timestamp() - interval '1 hour' END,
       statement_timestamp()
FROM connection_cases;
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
			startedSpec, startedCwd, startedEnv, startedSecretEnv, startedTimeout, startedInitialWait, "",
			tools[1], f.BindingID, testProjectID, f.AgentID, f.Lock.ID,
		}},
		{"process_actions", "GetProcessActionCreateBlocker", []any{blocker.ProjectID, blocker.AgentID, blocker.ProcessID}},
		{"process_actions", "InsertProcessAction", []any{
			tools[2], f.Lock.ID, "write", `{"data":""}`, testProjectID, f.AgentID, started.ID,
		}},
	}

	_, err = tx.Exec(ctx, fmt.Sprintf(onlineFleetWithStaleLeaseStatistics, onlineFleet))
	require.NoError(t, err)
	_, err = tx.Exec(ctx, machineConnectionCases)
	require.NoError(t, err)
	requireMachineConnectionStates(t, tx)
	requireSingleRowConnectionReads(t, tx, lookups)
	requireOnlineQueryResults(t, tx, []uuid.UUID{f.MachineID, wake.MachineID}, wake, blocker)
}

func requireMachineConnectionStates(t *testing.T, tx pgx.Tx) {
	t.Helper()
	var expected, actual []string
	require.NoError(t, tx.QueryRow(t.Context(), `
SELECT (SELECT array_agg(name || '=' || expected ORDER BY name) FROM connection_cases),
       (SELECT array_agg(connection_case.name || '=' || state.connection_state
                         ORDER BY connection_case.name, state.connection_state)
        FROM connection_cases connection_case
        JOIN machine_connection_states state ON state.org_id = md5('connection-org')::uuid
          AND state.machine_id = md5('connection-machine-' || connection_case.name)::uuid)`,
	).Scan(&expected, &actual))
	require.Equal(t, expected, actual)
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
	for _, online := range []bool{true, false} {
		if !online {
			_, err := tx.Exec(ctx, `
UPDATE daemon_runtimes runtime
SET state = 'ended', state_reason_code = 'machine_asleep', ended_at = statement_timestamp(),
    updated_at = statement_timestamp()
FROM machines machine
WHERE machine.id = ANY($1)
  AND runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id`, machines)
			require.NoError(t, err)
			_, err = tx.Exec(ctx, `UPDATE machines SET asleep_since = statement_timestamp() WHERE id = ANY($1)`, machines)
			require.NoError(t, err)
		}
		wakeState, err := queries.GetMachineWakeState(ctx, wake)
		require.NoError(t, err)
		require.Equal(t, online, wakeState.Online)
		blockerState, err := queries.GetProcessActionCreateBlocker(ctx, blocker)
		require.NoError(t, err)
		require.Equal(t, online, blockerState.HasOnlineRuntime)
	}
}

func requireSingleRowConnectionReads(t *testing.T, tx pgx.Tx, lookups []machineConnectionLookup) {
	t.Helper()
	ctx := t.Context()
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
				"EXPLAIN (ANALYZE, FORMAT JSON) EXECUTE lookup("+sqlArguments(lookup.args)+")",
			).Scan(&raw))
			integrationdb.ReadPlan(t, raw).Walk(func(node integrationdb.PlanNode) {
				if node.Relation == "daemon_runtimes" || node.Relation == "machine_daemon_tokens" {
					require.LessOrEqual(t, node.Loops*node.Rows, 1.0, "%s: %s", label, raw)
				}
			})
			_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT lookup")
			require.NoError(t, err)
		}
		_, err = tx.Exec(ctx, "DEALLOCATE lookup")
		require.NoError(t, err)
	}
}

func sqlArguments(args []any) string {
	formatted := make([]string, len(args))
	for i, arg := range args {
		formatted[i] = "'" + strings.ReplaceAll(fmt.Sprint(arg), "'", "''") + "'"
	}
	return strings.Join(formatted, ",")
}

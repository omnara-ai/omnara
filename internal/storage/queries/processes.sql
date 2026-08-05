-- name: GetProcessExecutionConfig :one
-- @sqlc-vet-disable machines-deleted-at
-- Binding attachment governs liveness; machine env still resolves during teardown.
SELECT binding.id AS binding_id,
       binding.org_id,
       binding.project_id,
       binding.machine_id,
       binding.cwd AS binding_cwd,
       binding.env_overlay AS binding_env_overlay,
       binding.secret_env_overlay AS binding_secret_env_overlay,
       machine.cwd AS machine_cwd,
       machine.env AS machine_env,
       machine.secret_env AS machine_secret_env
FROM agent_machine_bindings binding
JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.id = sqlc.arg(agent_machine_binding_id);

-- name: InsertProcess :one
WITH target_agent AS MATERIALIZED (
  SELECT agent.org_id, agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
),
live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
),
bound_machine AS MATERIALIZED (
  SELECT binding.machine_id
  FROM agent_machine_bindings binding
  WHERE binding.project_id = sqlc.arg(project_id)
    AND binding.agent_id = sqlc.arg(agent_id)
    AND binding.id = sqlc.arg(agent_machine_binding_id)
    AND binding.state = 'attached'
),
reachable_machine AS MATERIALIZED (
  SELECT bound.machine_id
  FROM bound_machine bound
  JOIN machine_connection_states connection ON connection.org_id = (SELECT org_id FROM target_agent)
    AND connection.machine_id = bound.machine_id
  WHERE connection.connection_state IN ('online', 'asleep')
)
INSERT INTO processes(org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, state, state_changed_at, created_at, updated_at)
SELECT agent.org_id, agent.project_id, agent.id, tool_call.id, runtime_lock.id, binding.id, binding.machine_id, NULL, sqlc.arg(io_mode), sqlc.arg(command), sqlc.arg(shell_selector), sqlc.arg(cwd), sqlc.arg(env)::jsonb, sqlc.arg(secret_env)::jsonb, sqlc.arg(timeout_seconds), sqlc.arg(initial_wait_ms), 'queued', statement_timestamp(), statement_timestamp(), statement_timestamp()
FROM target_agent agent
JOIN live_runtime runtime_lock ON runtime_lock.project_id = agent.project_id
  AND runtime_lock.agent_id = agent.id
JOIN tool_calls tool_call ON tool_call.agent_id = agent.id
  AND tool_call.id = sqlc.arg(tool_call_id)
JOIN agent_machine_bindings binding ON binding.project_id = agent.project_id
  AND binding.agent_id = agent.id
  AND binding.id = sqlc.arg(agent_machine_binding_id)
  AND binding.state = 'attached'
JOIN project_machine_grants pmgrant ON pmgrant.project_id = binding.project_id
  AND pmgrant.machine_id = binding.machine_id
JOIN reachable_machine ON true
ON CONFLICT (agent_id, tool_call_id) DO NOTHING
RETURNING id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at;

-- name: MachineReachableForProjectMachine :one
SELECT machine.id
FROM machines machine
JOIN project_machine_grants pmgrant ON pmgrant.org_id = machine.org_id
  AND pmgrant.machine_id = machine.id
  AND pmgrant.project_id = sqlc.arg(project_id)
JOIN machine_connection_states connection ON connection.org_id = machine.org_id
  AND connection.machine_id = machine.id
WHERE machine.id = sqlc.arg(machine_id)
  AND machine.deleted_at IS NULL
  AND connection.connection_state IN ('online', 'asleep')
LIMIT 1;

-- name: CountNonTerminalProcessesForAgent :one
SELECT count(*)::bigint
FROM processes
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND state IN ('queued', 'starting', 'running');

-- name: GetProcess :one
SELECT id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at
FROM processes
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

-- name: GetProcessForUpdate :one
SELECT id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at
FROM processes
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id)
FOR UPDATE;

-- name: LockProcessForActionCreation :one
SELECT id
FROM processes
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id)
FOR UPDATE;

-- name: AdvanceProcessDefaultOutputCursor :execrows
UPDATE processes
SET default_output_cursor = GREATEST(
      default_output_cursor,
      sqlc.arg(next_cursor)::bigint
    )
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

-- name: GetProcessByMachine :one
SELECT id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at
FROM processes
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND id = sqlc.arg(id);

-- name: ListProcessesForMachineReconciliation :many
SELECT process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at
FROM processes process
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND (
    process.state IN ('starting', 'running')
    OR EXISTS (
      SELECT 1
      FROM process_actions action
      WHERE action.project_id = process.project_id
        AND action.agent_id = process.agent_id
        AND action.process_id = process.id
        AND action.state IN ('queued', 'accepted')
    )
  );

-- name: GetProcessByToolCall :one
SELECT id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at
FROM processes
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND tool_call_id = sqlc.arg(tool_call_id);

-- name: CancelUnresolvedProcessesForAgentTurn :many
UPDATE processes process
SET state = CASE
      WHEN process.state = 'queued' THEN 'failed'
      ELSE 'unknown'
    END,
    state_reason_code = CASE
      WHEN process.state = 'queued' THEN 'agent_canceled_before_grant'
      ELSE 'agent_canceled_after_grant'
    END,
    state_reason_message = CASE
      WHEN process.state = 'queued' THEN 'agent was canceled before process execution was granted'
      ELSE 'agent was canceled after process execution was granted'
    END,
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
FROM tool_call_read_projection tool_call
WHERE process.project_id = sqlc.arg(project_id)
  AND process.agent_id = sqlc.arg(agent_id)
  AND process.state IN ('queued', 'starting', 'running')
  AND process.tool_call_id = tool_call.id
  AND tool_call.project_id = process.project_id
  AND tool_call.agent_id = process.agent_id
  AND tool_call.state = 'waiting'
  AND tool_call.turn_id = sqlc.arg(turn_id)
RETURNING process.id, process.org_id, process.machine_id, process.state;

-- name: MarkProcessStarted :one
UPDATE processes process
SET state = 'running',
    source_started_at = coalesce(source_started_at, sqlc.arg(source_started_at)::timestamptz),
    state_changed_at = CASE
      WHEN state = 'starting' THEN statement_timestamp()
      ELSE state_changed_at
    END,
    updated_at = CASE
      WHEN state = 'starting' OR source_started_at IS NULL THEN statement_timestamp()
      ELSE updated_at
    END
WHERE process.project_id = sqlc.arg(project_id)
  AND process.agent_id = sqlc.arg(agent_id)
  AND process.id = sqlc.arg(id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.state IN ('starting', 'running')
RETURNING process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at;

-- name: CompleteProcess :one
UPDATE processes
SET state = sqlc.arg(state),
    source_ended_at = sqlc.arg(source_ended_at)::timestamptz,
    exit_code = sqlc.narg(exit_code),
    exit_signal = sqlc.arg(exit_signal),
    state_reason_code = sqlc.narg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id)
  AND runtime_lock_id = sqlc.arg(runtime_lock_id)
  AND state IN ('starting', 'running')
RETURNING id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at;

-- name: CompleteDaemonObservedProcess :one
UPDATE processes process
SET state = sqlc.arg(state),
    source_started_at = coalesce(source_started_at, sqlc.narg(source_started_at)::timestamptz),
    source_ended_at = sqlc.narg(source_ended_at)::timestamptz,
    exit_code = sqlc.narg(exit_code),
    exit_signal = sqlc.arg(exit_signal),
    state_reason_code = sqlc.narg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE process.project_id = sqlc.arg(project_id)
  AND process.agent_id = sqlc.arg(agent_id)
  AND process.id = sqlc.arg(id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.state IN ('starting', 'running')
  AND sqlc.arg(state)::text IN ('exited', 'failed', 'killed', 'unknown')
  AND (sqlc.arg(state)::text <> 'exited' OR sqlc.narg(exit_code)::integer IS NOT NULL)
  AND (sqlc.arg(state)::text IN ('exited', 'failed') OR sqlc.narg(exit_code)::integer IS NULL)
  AND (sqlc.arg(state)::text <> 'failed' OR sqlc.narg(state_reason_code)::text IS NOT NULL)
  AND (sqlc.arg(state)::text <> 'unknown' OR sqlc.narg(state_reason_code)::text IS NOT NULL)
  AND (
    sqlc.narg(source_started_at)::timestamptz IS NULL
    OR process.source_started_at IS NULL
    OR process.source_started_at = sqlc.narg(source_started_at)::timestamptz
  )
  AND (
    sqlc.narg(source_ended_at)::timestamptz IS NULL
    OR coalesce(process.source_started_at, sqlc.narg(source_started_at)::timestamptz) IS NULL
    OR sqlc.narg(source_ended_at)::timestamptz >= coalesce(process.source_started_at, sqlc.narg(source_started_at)::timestamptz)
  )
  AND (
    sqlc.arg(state)::text NOT IN ('exited', 'killed')
    OR sqlc.narg(source_ended_at)::timestamptz IS NOT NULL
  )
  AND (
    sqlc.arg(state)::text <> 'failed'
    OR coalesce(process.source_started_at, sqlc.narg(source_started_at)::timestamptz) IS NULL
    OR sqlc.narg(source_ended_at)::timestamptz IS NOT NULL
  )
RETURNING process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at;

-- name: FailProcessBeforeExecution :one
UPDATE processes process
SET state = 'failed',
    state_reason_code = sqlc.narg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.id = sqlc.arg(id)
  AND process.state = 'starting'
  AND process.source_started_at IS NULL
RETURNING process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at;

-- name: ListDaemonProcessOffers :many
WITH runtime AS MATERIALIZED (
  SELECT runtime.org_id, runtime.machine_id
  FROM online_daemon_runtimes runtime
  WHERE runtime.id = sqlc.arg(daemon_runtime_id)::uuid
    AND runtime.org_id = sqlc.arg(org_id)
    AND runtime.machine_id = sqlc.arg(machine_id)
    AND runtime.daemon_token_id = sqlc.arg(daemon_token_id)::uuid
)
SELECT process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at
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
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.state = 'queued'
ORDER BY process.created_at, process.id
LIMIT sqlc.arg(limit_count);

-- name: LockDaemonProcessForAccept :one
SELECT process.id
FROM processes process
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.id = sqlc.arg(process_id)
FOR UPDATE;

-- name: AcceptDaemonProcess :one
WITH runtime AS MATERIALIZED (
  SELECT runtime.org_id, runtime.machine_id
  FROM online_daemon_runtimes runtime
  WHERE runtime.id = sqlc.arg(daemon_runtime_id)::uuid
    AND runtime.org_id = sqlc.arg(org_id)
    AND runtime.machine_id = sqlc.arg(machine_id)
    AND runtime.daemon_token_id = sqlc.arg(daemon_token_id)::uuid
)
UPDATE processes process
SET execution_granted_at = statement_timestamp(),
    state = 'starting',
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
FROM runtime
WHERE process.org_id = runtime.org_id
  AND process.machine_id = runtime.machine_id
  AND process.id = sqlc.arg(process_id)
  AND process.state = 'queued'
  AND EXISTS (
    SELECT 1
    FROM agent_machine_bindings binding
    JOIN project_machine_grants pmgrant ON pmgrant.project_id = binding.project_id
      AND pmgrant.machine_id = binding.machine_id
    WHERE binding.project_id = process.project_id
      AND binding.agent_id = process.agent_id
      AND binding.id = process.agent_machine_binding_id
      AND binding.machine_id = process.machine_id
      AND binding.state = 'attached'
  )
RETURNING process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at;

-- name: GetDaemonProcessForProjectReport :one
SELECT processes.id, processes.org_id, processes.project_id, processes.agent_id, processes.tool_call_id, processes.runtime_lock_id, processes.agent_machine_binding_id, processes.machine_id, processes.execution_granted_at, processes.io_mode, processes.command, processes.shell_selector, processes.cwd, processes.env, processes.secret_env, processes.timeout_seconds, processes.initial_wait_ms, processes.default_output_cursor, processes.state, processes.state_reason_code, processes.state_reason_message, processes.source_started_at, processes.source_ended_at, processes.state_changed_at, processes.exit_code, processes.exit_signal, processes.created_at, processes.updated_at
FROM processes
WHERE processes.project_id = sqlc.arg(project_id)
  AND processes.machine_id = sqlc.arg(machine_id)
  AND processes.id = sqlc.arg(id);

-- name: GetDaemonProcessForMachineReport :one
SELECT process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at
FROM processes process
JOIN reportable_daemon_runtimes runtime ON runtime.org_id = process.org_id
  AND runtime.machine_id = process.machine_id
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND runtime.id = sqlc.arg(daemon_runtime_id)
  AND runtime.daemon_token_id = sqlc.arg(daemon_token_id)
  AND process.id = sqlc.arg(id);

-- name: MarkActiveProcessUnknownByMachine :one
UPDATE processes
SET state = 'unknown',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND id = sqlc.arg(id)
  AND state IN ('starting', 'running')
RETURNING id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at;

-- name: LockAgentsForExecutionRevoked :many
SELECT agent.id
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND (
    (sqlc.narg(agent_id)::uuid IS NOT NULL AND agent.id = sqlc.narg(agent_id)::uuid)
    OR (
      (sqlc.narg(project_machine_grant_id)::uuid IS NOT NULL OR sqlc.narg(project_machine_pool_grant_id)::uuid IS NOT NULL)
      AND EXISTS (
        SELECT 1
        FROM agent_machine_bindings binding
        JOIN project_machine_grants pmgrant ON pmgrant.project_id = binding.project_id
          AND pmgrant.machine_id = binding.machine_id
        WHERE binding.project_id = agent.project_id
          AND binding.agent_id = agent.id
          AND binding.state = 'attached'
          AND (
            sqlc.narg(project_machine_grant_id)::uuid IS NULL
            OR pmgrant.id = sqlc.narg(project_machine_grant_id)::uuid
          )
          AND (
            sqlc.narg(project_machine_pool_grant_id)::uuid IS NULL
            OR (
              pmgrant.source_kind = 'pool'
              AND pmgrant.project_machine_pool_grant_id = sqlc.narg(project_machine_pool_grant_id)::uuid
            )
          )
      )
    )
  )
ORDER BY agent.id
FOR UPDATE;

-- name: ListProcessesForExecutionRevoked :many
SELECT process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at
FROM processes process
WHERE process.project_id = sqlc.arg(project_id)
  AND (
    process.state IN ('queued', 'starting', 'running')
    OR EXISTS (
      SELECT 1
      FROM process_actions action
      WHERE action.project_id = process.project_id
        AND action.agent_id = process.agent_id
        AND action.process_id = process.id
        AND action.state IN ('queued', 'accepted')
    )
  )
  AND (
    sqlc.narg(agent_id)::uuid IS NOT NULL
    OR sqlc.narg(project_machine_grant_id)::uuid IS NOT NULL
    OR sqlc.narg(project_machine_pool_grant_id)::uuid IS NOT NULL
  )
  AND (
    sqlc.narg(agent_id)::uuid IS NULL
    OR process.agent_id = sqlc.narg(agent_id)::uuid
  )
  AND (
    (
      sqlc.narg(project_machine_grant_id)::uuid IS NULL
      AND sqlc.narg(project_machine_pool_grant_id)::uuid IS NULL
    )
    OR EXISTS (
      SELECT 1
      FROM project_machine_grants pmgrant
      WHERE pmgrant.project_id = process.project_id
        AND pmgrant.machine_id = process.machine_id
        AND (
          sqlc.narg(project_machine_grant_id)::uuid IS NULL
          OR pmgrant.id = sqlc.narg(project_machine_grant_id)::uuid
        )
        AND (
          sqlc.narg(project_machine_pool_grant_id)::uuid IS NULL
          OR (
            pmgrant.source_kind = 'pool'
            AND pmgrant.project_machine_pool_grant_id = sqlc.narg(project_machine_pool_grant_id)::uuid
          )
        )
    )
  )
ORDER BY process.project_id, process.agent_id, process.created_at, process.id;

-- name: ListProcessesForMachineLifecycleTermination :many
SELECT id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id, agent_machine_binding_id, machine_id, execution_granted_at, io_mode, command, shell_selector, cwd, env, secret_env, timeout_seconds, initial_wait_ms, default_output_cursor, state, state_reason_code, state_reason_message, source_started_at, source_ended_at, state_changed_at, exit_code, exit_signal, created_at, updated_at
FROM processes process
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND (
    process.state IN ('starting', 'running')
    OR EXISTS (
      SELECT 1
      FROM process_actions action
      WHERE action.project_id = process.project_id
        AND action.agent_id = process.agent_id
        AND action.process_id = process.id
        AND action.state IN ('queued', 'accepted')
    )
  )
ORDER BY project_id, agent_id, created_at, id;

-- name: ListActiveProcessesForContext :many
SELECT id, state, machine_id, io_mode, command, shell_selector, cwd, source_started_at, created_at, updated_at, tool_call_id
FROM processes
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND state IN ('starting', 'running')
ORDER BY updated_at DESC, id
LIMIT 20;

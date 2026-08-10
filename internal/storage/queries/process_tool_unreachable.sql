-- name: ListMachineUnreachableMachineCandidates :many
WITH cutoff AS MATERIALIZED (
  SELECT transaction_timestamp() AS observed_at,
         transaction_timestamp()
           - (sqlc.arg(machine_unreachable_grace_seconds)::int * interval '1 second') AS unreachable_before
), process_work AS MATERIALIZED (
  SELECT process.org_id, process.machine_id, process.created_at AS work_at
  FROM processes process
  JOIN tool_calls tool_call ON tool_call.agent_id = process.agent_id
    AND tool_call.id = process.tool_call_id
  JOIN machines machine ON machine.org_id = process.org_id
    AND machine.id = process.machine_id
  LEFT JOIN LATERAL (
    SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz AS observed_at
    FROM daemon_runtimes runtime
    WHERE runtime.org_id = process.org_id
      AND runtime.machine_id = process.machine_id
    ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
    LIMIT 1
  ) latest_runtime ON true
  CROSS JOIN cutoff
  WHERE process.state IN ('queued', 'starting', 'running')
    AND process.created_at <= cutoff.unreachable_before
    AND coalesce(latest_runtime.observed_at, process.created_at) <= cutoff.unreachable_before
    AND tool_call.type = 'built_in'
    AND tool_call.state = 'waiting'
    AND machine.lifecycle_state = 'active'
    AND machine.deleted_at IS NULL
    AND (
      machine.wake_attempt_expires_at IS NULL
      OR machine.wake_attempt_expires_at <= cutoff.observed_at
    )
    AND NOT EXISTS (
      SELECT 1
      FROM online_daemon_runtimes online
      WHERE online.org_id = process.org_id
        AND online.machine_id = process.machine_id
    )
  ORDER BY process.created_at, process.id
  LIMIT sqlc.arg(limit_count)
), action_work AS MATERIALIZED (
  SELECT action.org_id, process.machine_id, action.created_at AS work_at
  FROM process_actions action
  JOIN processes process ON process.org_id = action.org_id
    AND process.project_id = action.project_id
    AND process.agent_id = action.agent_id
    AND process.id = action.process_id
  JOIN tool_calls tool_call ON tool_call.agent_id = action.agent_id
    AND tool_call.id = action.tool_call_id
  JOIN machines machine ON machine.org_id = action.org_id
    AND machine.id = process.machine_id
  LEFT JOIN LATERAL (
    SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz AS observed_at
    FROM daemon_runtimes runtime
    WHERE runtime.org_id = action.org_id
      AND runtime.machine_id = process.machine_id
    ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
    LIMIT 1
  ) latest_runtime ON true
  CROSS JOIN cutoff
  WHERE action.state IN ('queued', 'accepted')
    AND action.created_at <= cutoff.unreachable_before
    AND coalesce(latest_runtime.observed_at, action.created_at) <= cutoff.unreachable_before
    AND (
      action.state = 'accepted'
      OR process.state IN ('starting', 'running')
      OR (
        action.action_kind = 'read'
        AND process.state IN ('exited', 'failed', 'killed', 'unknown')
      )
    )
    AND tool_call.type = 'built_in'
    AND tool_call.state = 'waiting'
    AND machine.lifecycle_state = 'active'
    AND machine.deleted_at IS NULL
    AND (
      machine.wake_attempt_expires_at IS NULL
      OR machine.wake_attempt_expires_at <= cutoff.observed_at
    )
    AND NOT EXISTS (
      SELECT 1
      FROM online_daemon_runtimes online
      WHERE online.org_id = action.org_id
        AND online.machine_id = process.machine_id
    )
  ORDER BY action.created_at, action.id
  LIMIT sqlc.arg(limit_count)
), machine_work AS MATERIALIZED (
  SELECT work.org_id, work.machine_id, min(work.work_at)::timestamptz AS earliest_work_at
  FROM (
    SELECT org_id, machine_id, work_at FROM process_work
    UNION ALL
    SELECT org_id, machine_id, work_at FROM action_work
  ) work
  GROUP BY work.org_id, work.machine_id
), candidates AS (
  SELECT work.org_id,
         work.machine_id,
         greatest(
           work.earliest_work_at,
           coalesce(latest_runtime.observed_at, work.earliest_work_at)
         )::timestamptz AS unreachable_at
  FROM machine_work work
  LEFT JOIN LATERAL (
    SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz AS observed_at
    FROM daemon_runtimes runtime
    WHERE runtime.org_id = work.org_id
      AND runtime.machine_id = work.machine_id
    ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
    LIMIT 1
  ) latest_runtime ON true
)
SELECT candidates.org_id, candidates.machine_id, candidates.unreachable_at
FROM candidates
CROSS JOIN cutoff
WHERE candidates.unreachable_at <= cutoff.unreachable_before
ORDER BY candidates.unreachable_at, candidates.org_id, candidates.machine_id
LIMIT sqlc.arg(limit_count);

-- name: ListMachineUnreachableQueuedProcessToolCallsForMachine :many
SELECT process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at
FROM processes process
JOIN tool_calls tool_call ON tool_call.agent_id = process.agent_id
  AND tool_call.id = process.tool_call_id
JOIN machines machine ON machine.org_id = process.org_id
  AND machine.id = process.machine_id
LEFT JOIN LATERAL (
  SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz AS unreachable_at
  FROM daemon_runtimes runtime
  WHERE runtime.org_id = process.org_id
    AND runtime.machine_id = process.machine_id
  ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
  LIMIT 1
) latest_runtime ON true
LEFT JOIN online_daemon_runtimes online ON online.org_id = process.org_id
  AND online.machine_id = process.machine_id
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.state = 'queued'
  AND process.tool_call_id IS NOT NULL
  AND tool_call.type = 'built_in'
  AND tool_call.state = 'waiting'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND (
    machine.wake_attempt_expires_at IS NULL
    OR machine.wake_attempt_expires_at <= transaction_timestamp()
  )
  AND online.id IS NULL
  AND greatest(latest_runtime.unreachable_at, process.created_at) <= transaction_timestamp() - (sqlc.arg(machine_unreachable_grace_seconds)::int * interval '1 second')
ORDER BY process.created_at, process.id
LIMIT sqlc.arg(limit_count);

-- name: CheckMachineUnreachableForToolExpiry :one
SELECT machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND (
    machine.wake_attempt_expires_at IS NULL
    OR machine.wake_attempt_expires_at <= statement_timestamp()
  )
  AND greatest((
    SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz
    FROM daemon_runtimes runtime
    WHERE runtime.org_id = machine.org_id
      AND runtime.machine_id = machine.id
    ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
    LIMIT 1
  ), sqlc.arg(fallback_at)::timestamptz) <= statement_timestamp() - (sqlc.arg(machine_unreachable_grace_seconds)::int * interval '1 second')
  AND NOT EXISTS (
    SELECT 1
    FROM online_daemon_runtimes online
    WHERE online.org_id = machine.org_id
      AND online.machine_id = machine.id
  ) AS unreachable
FROM machines machine
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id);

-- name: ListQueuedProcessToolCallsForMachineDeletion :many
SELECT process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at
FROM processes process
JOIN tool_calls tool_call ON tool_call.agent_id = process.agent_id
  AND tool_call.id = process.tool_call_id
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.state = 'queued'
  AND process.tool_call_id IS NOT NULL
  AND tool_call.type = 'built_in'
  AND tool_call.state = 'waiting'
ORDER BY process.created_at, process.id
LIMIT sqlc.arg(limit_count);

-- name: MarkQueuedProcessFailedByMachine :one
UPDATE processes process
SET state = 'failed',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = '',
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE process.project_id = sqlc.arg(project_id)
  AND process.agent_id = sqlc.arg(agent_id)
  AND process.id = sqlc.arg(id)
  AND process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.state = 'queued'
  AND EXISTS (
    SELECT 1
    FROM tool_calls tool_call
    WHERE tool_call.agent_id = process.agent_id
      AND tool_call.id = process.tool_call_id
      AND tool_call.type = 'built_in'
      AND tool_call.state = 'waiting'
  )
RETURNING process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at;

-- name: ListMachineUnreachableAcceptedProcessToolCallsForMachine :many
SELECT process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at
FROM processes process
JOIN tool_calls tool_call ON tool_call.agent_id = process.agent_id
  AND tool_call.id = process.tool_call_id
JOIN machines machine ON machine.org_id = process.org_id
  AND machine.id = process.machine_id
LEFT JOIN LATERAL (
  SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz AS unreachable_at
  FROM daemon_runtimes runtime
  WHERE runtime.org_id = process.org_id
    AND runtime.machine_id = process.machine_id
  ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
  LIMIT 1
) latest_runtime ON true
LEFT JOIN online_daemon_runtimes online ON online.org_id = process.org_id
  AND online.machine_id = process.machine_id
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND process.state IN ('starting', 'running')
  AND process.tool_call_id IS NOT NULL
  AND tool_call.type = 'built_in'
  AND tool_call.state = 'waiting'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND (
    machine.wake_attempt_expires_at IS NULL
    OR machine.wake_attempt_expires_at <= transaction_timestamp()
  )
  AND online.id IS NULL
  AND greatest(latest_runtime.unreachable_at, process.created_at) <= transaction_timestamp() - (sqlc.arg(machine_unreachable_grace_seconds)::int * interval '1 second')
ORDER BY coalesce(latest_runtime.unreachable_at, process.created_at), process.created_at, process.id
LIMIT sqlc.arg(limit_count);

-- name: ListMachineUnreachableQueuedProcessActionToolCallsForMachine :many
SELECT action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message
FROM process_actions action
JOIN processes process ON process.project_id = action.project_id
  AND process.agent_id = action.agent_id
  AND process.id = action.process_id
JOIN tool_calls tool_call ON tool_call.agent_id = action.agent_id
  AND tool_call.id = action.tool_call_id
JOIN machines machine ON machine.org_id = process.org_id
  AND machine.id = process.machine_id
LEFT JOIN LATERAL (
  SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz AS unreachable_at
  FROM daemon_runtimes runtime
  WHERE runtime.org_id = process.org_id
    AND runtime.machine_id = process.machine_id
  ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
  LIMIT 1
) latest_runtime ON true
LEFT JOIN online_daemon_runtimes online ON online.org_id = process.org_id
  AND online.machine_id = process.machine_id
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND (
    process.state IN ('starting', 'running')
    OR (
      action.action_kind = 'read'
      AND process.state IN ('exited', 'failed', 'killed', 'unknown')
    )
  )
  AND action.state = 'queued'
  AND tool_call.type = 'built_in'
  AND tool_call.state = 'waiting'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND (
    machine.wake_attempt_expires_at IS NULL
    OR machine.wake_attempt_expires_at <= transaction_timestamp()
  )
  AND online.id IS NULL
  AND greatest(latest_runtime.unreachable_at, action.created_at) <= transaction_timestamp() - (sqlc.arg(machine_unreachable_grace_seconds)::int * interval '1 second')
ORDER BY coalesce(latest_runtime.unreachable_at, action.created_at), action.created_at, action.id
LIMIT sqlc.arg(limit_count);

-- name: ListMachineUnreachableAcceptedProcessActionToolCallsForMachine :many
SELECT action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message
FROM process_actions action
JOIN processes process ON process.project_id = action.project_id
  AND process.agent_id = action.agent_id
  AND process.id = action.process_id
JOIN tool_calls tool_call ON tool_call.agent_id = action.agent_id
  AND tool_call.id = action.tool_call_id
JOIN machines machine ON machine.org_id = process.org_id
  AND machine.id = process.machine_id
LEFT JOIN LATERAL (
  SELECT coalesce(runtime.ended_at, runtime.lease_expires_at)::timestamptz AS unreachable_at
  FROM daemon_runtimes runtime
  WHERE runtime.org_id = process.org_id
    AND runtime.machine_id = process.machine_id
  ORDER BY coalesce(runtime.ended_at, runtime.lease_expires_at) DESC, runtime.id DESC
  LIMIT 1
) latest_runtime ON true
LEFT JOIN online_daemon_runtimes online ON online.org_id = process.org_id
  AND online.machine_id = process.machine_id
WHERE process.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND action.state = 'accepted'
  AND tool_call.type = 'built_in'
  AND tool_call.state = 'waiting'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND (
    machine.wake_attempt_expires_at IS NULL
    OR machine.wake_attempt_expires_at <= transaction_timestamp()
  )
  AND online.id IS NULL
  AND greatest(latest_runtime.unreachable_at, action.created_at) <= transaction_timestamp() - (sqlc.arg(machine_unreachable_grace_seconds)::int * interval '1 second')
ORDER BY action.process_id, action.seq, action.id
LIMIT sqlc.arg(limit_count);

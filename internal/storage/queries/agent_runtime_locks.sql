-- name: DBNow :one
SELECT transaction_timestamp()::timestamptz;

-- name: LockAgentRuntimeLockForOwnedMutation :one
SELECT runtime_lock.id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.id = sqlc.arg(id)
FOR UPDATE OF runtime_lock;

-- name: GetAgentRuntimeLockForRelease :one
SELECT runtime_lock.id, runtime_lock.agent_id, runtime_lock.worker_process_id,
       runtime_lock.started_at, runtime_lock.renewed_at,
       runtime_lock.lease_expires_at, runtime_lock.cancel_requested_at
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.id = sqlc.arg(id);

-- name: ListExpiredAgentRuntimeLockCandidates :many
SELECT runtime_lock.id, agent.project_id, runtime_lock.agent_id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE runtime_lock.lease_expires_at <= statement_timestamp()
ORDER BY runtime_lock.lease_expires_at ASC, runtime_lock.id ASC
LIMIT sqlc.arg(batch_size)::int;

-- name: FailQueuedProcessesForRuntimeEnd :many
UPDATE processes process
SET state = 'failed',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = '',
    state_changed_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE process.project_id = sqlc.arg(project_id)
  AND process.agent_id = sqlc.arg(agent_id)
  AND process.runtime_lock_id = sqlc.arg(runtime_lock_id)::uuid
  AND process.state = 'queued'
RETURNING process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at, process.last_activity_at, process.execution_spec;

-- name: FailQueuedProcessActionsForRuntimeEnd :many
UPDATE process_actions action
SET state = 'failed',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = '',
    updated_at = statement_timestamp()
WHERE action.project_id = sqlc.arg(project_id)
  AND action.agent_id = sqlc.arg(agent_id)
  AND action.runtime_lock_id = sqlc.arg(runtime_lock_id)::uuid
  AND action.state = 'queued'
  AND EXISTS (
    SELECT 1
    FROM tool_calls tool_call
    WHERE tool_call.agent_id = action.agent_id
      AND tool_call.id = action.tool_call_id
      AND tool_call.type = 'built_in'
      AND tool_call.state = 'waiting'
  )
RETURNING action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message;

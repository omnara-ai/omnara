-- name: DBNow :one
SELECT transaction_timestamp()::timestamptz;

-- name: AcquireAgentRuntimeLock :one
WITH lease_clock AS MATERIALIZED (
  SELECT statement_timestamp() AS renewed_at
)
INSERT INTO agent_runtime_locks(agent_id, worker_process_id, started_at, renewed_at, lease_expires_at)
SELECT agent.id,
       sqlc.arg(worker_process_id)::uuid,
       lease_clock.renewed_at,
       lease_clock.renewed_at,
       lease_clock.renewed_at + (sqlc.arg(lease_duration_microseconds)::bigint * interval '1 microsecond')
FROM agents agent
CROSS JOIN lease_clock
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
  AND agent.state <> 'archived'
ON CONFLICT (agent_id) DO NOTHING
RETURNING id, agent_id, worker_process_id, started_at, renewed_at,
          lease_expires_at, cancel_requested_at;

-- name: LockAgentRuntimeLockForRenewal :one
SELECT runtime_lock.id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.id = sqlc.arg(id)
FOR UPDATE OF runtime_lock;

-- name: RenewAgentRuntimeLock :one
UPDATE agent_runtime_locks runtime_lock
SET renewed_at = greatest(runtime_lock.renewed_at, lease_clock.statement_at),
    lease_expires_at = greatest(
      runtime_lock.lease_expires_at,
      greatest(runtime_lock.renewed_at, lease_clock.statement_at) +
        (sqlc.arg(lease_duration_microseconds)::bigint * interval '1 microsecond')
    )
FROM (SELECT statement_timestamp() AS statement_at) AS lease_clock
CROSS JOIN agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = runtime_lock.agent_id
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.id = sqlc.arg(id)
RETURNING runtime_lock.id, runtime_lock.agent_id, runtime_lock.worker_process_id,
          runtime_lock.started_at, runtime_lock.renewed_at,
          runtime_lock.lease_expires_at, runtime_lock.cancel_requested_at;

-- name: RequestAgentRuntimeCancel :one
UPDATE agent_runtime_locks runtime_lock
SET cancel_requested_at = coalesce(runtime_lock.cancel_requested_at, greatest(runtime_lock.started_at, statement_timestamp()))
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = runtime_lock.agent_id
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.cancel_requested_at IS NULL
RETURNING runtime_lock.id, runtime_lock.agent_id, runtime_lock.worker_process_id,
          runtime_lock.started_at, runtime_lock.renewed_at,
          runtime_lock.lease_expires_at, runtime_lock.cancel_requested_at;

-- name: GetPendingAgentRuntimeCancel :one
SELECT runtime_lock.id, runtime_lock.agent_id, runtime_lock.worker_process_id,
       runtime_lock.started_at, runtime_lock.renewed_at,
       runtime_lock.lease_expires_at, runtime_lock.cancel_requested_at
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.cancel_requested_at IS NOT NULL;

-- name: LockAgentRuntimeLockForOwnedMutation :one
SELECT runtime_lock.id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.id = sqlc.arg(id)
FOR UPDATE OF runtime_lock;

-- name: AgentRuntimeLockIsActive :one
SELECT EXISTS (
  SELECT 1
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
    AND agent.state <> 'archived'
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
) AS active;

-- name: GetAgentRuntimeLockForRelease :one
SELECT runtime_lock.id, runtime_lock.agent_id, runtime_lock.worker_process_id,
       runtime_lock.started_at, runtime_lock.renewed_at,
       runtime_lock.lease_expires_at, runtime_lock.cancel_requested_at
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.id = sqlc.arg(id);

-- name: ReleaseAgentRuntimeLock :execrows
DELETE FROM agent_runtime_locks
USING agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = agent_runtime_locks.agent_id
  AND agent_runtime_locks.agent_id = sqlc.arg(agent_id)
  AND agent_runtime_locks.id = sqlc.arg(id);

-- name: TryLockAgentForRuntimeLockReap :one
SELECT id
FROM agents
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(agent_id)
FOR UPDATE SKIP LOCKED;

-- Candidate discovery intentionally does not lock runtime rows. Destructive
-- recovery must lock the canonical agent row before the runtime row.
-- name: ListExpiredAgentRuntimeLockCandidates :many
SELECT runtime_lock.id, agent.project_id, runtime_lock.agent_id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE runtime_lock.lease_expires_at <= statement_timestamp()
ORDER BY runtime_lock.lease_expires_at ASC, runtime_lock.id ASC
LIMIT sqlc.arg(batch_size)::int;

-- name: LockExpiredAgentRuntimeLockForReap :one
SELECT runtime_lock.id, agent.project_id, runtime_lock.agent_id
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND runtime_lock.agent_id = sqlc.arg(agent_id)
  AND runtime_lock.id = sqlc.arg(id)
  AND runtime_lock.lease_expires_at <= statement_timestamp()
FOR UPDATE OF runtime_lock SKIP LOCKED;

-- name: DeleteAgentRuntimeLockForReap :execrows
DELETE FROM agent_runtime_locks
USING agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = agent_runtime_locks.agent_id
  AND agent_runtime_locks.agent_id = sqlc.arg(agent_id)
  AND agent_runtime_locks.id = sqlc.arg(id);

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
RETURNING process.id, process.org_id, process.project_id, process.agent_id, process.tool_call_id, process.runtime_lock_id, process.agent_machine_binding_id, process.machine_id, process.execution_granted_at, process.io_mode, process.command, process.shell_selector, process.cwd, process.env, process.secret_env, process.timeout_seconds, process.initial_wait_ms, process.default_output_cursor, process.state, process.state_reason_code, process.state_reason_message, process.source_started_at, process.source_ended_at, process.state_changed_at, process.exit_code, process.exit_signal, process.created_at, process.updated_at, process.last_activity_at;

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

-- name: FailRuntimeToolCalls :many
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM tool_call_read_projection projection
WHERE call.agent_id = sqlc.arg(agent_id)
  AND call.runtime_lock_id = sqlc.arg(runtime_lock_id)::uuid
  AND call.state = 'running'
  AND call.type IN ('built_in', 'mcp')
  AND projection.project_id = sqlc.arg(project_id)
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id,
  projection.source_event_id, projection.model_call_context_id, call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  'failed'::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

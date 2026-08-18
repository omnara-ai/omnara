-- name: InsertProcessAction :one
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
),
target AS MATERIALIZED (
  SELECT process.id, process.org_id, process.project_id, process.agent_id
  FROM processes process
  JOIN live_runtime runtime_lock ON runtime_lock.project_id = process.project_id
    AND runtime_lock.agent_id = process.agent_id
  JOIN agent_machine_bindings binding ON binding.project_id = process.project_id
    AND binding.agent_id = process.agent_id
    AND binding.id = process.agent_machine_binding_id
    AND binding.machine_id = process.machine_id
    AND binding.state = 'attached'
  JOIN project_machine_grants pmgrant ON pmgrant.project_id = binding.project_id
    AND pmgrant.machine_id = binding.machine_id
  JOIN machine_connection_states connection ON connection.org_id = process.org_id
    AND connection.machine_id = process.machine_id
  WHERE process.project_id = sqlc.arg(project_id)
    AND process.agent_id = sqlc.arg(agent_id)
    AND process.id = sqlc.arg(process_id)
    AND (
      (
        connection.connection_state = 'online'
        AND process.state IN ('starting', 'running')
      )
      OR (
        connection.connection_state IN ('online', 'asleep')
        AND sqlc.arg(action_kind)::text = 'read'
        AND process.state IN ('exited', 'failed', 'killed', 'unknown')
        AND process.state_reason_code IS DISTINCT FROM 'machine_storage_exhausted'
      )
    )
    AND (
      sqlc.arg(action_kind)::text = 'read'
      OR NOT EXISTS (
        SELECT 1
        FROM process_actions terminate_action
        WHERE terminate_action.project_id = process.project_id
          AND terminate_action.agent_id = process.agent_id
          AND terminate_action.process_id = process.id
          AND terminate_action.action_kind = 'terminate'
          AND terminate_action.state IN ('queued', 'accepted', 'applied', 'unknown')
      )
    )
)
INSERT INTO process_actions(org_id, project_id, agent_id, process_id, tool_call_id, runtime_lock_id, action_kind, seq, payload, state, created_at, updated_at)
SELECT target.org_id, target.project_id, target.agent_id, target.id, sqlc.arg(tool_call_id), sqlc.arg(runtime_lock_id), sqlc.arg(action_kind)::text, coalesce((SELECT max(seq) FROM process_actions WHERE project_id = target.project_id AND agent_id = target.agent_id AND process_id = target.id), 0)::bigint + 1, sqlc.arg(payload), 'queued', statement_timestamp(), statement_timestamp()
FROM target
ON CONFLICT (agent_id, tool_call_id) DO NOTHING
RETURNING id, org_id, project_id, agent_id, process_id, tool_call_id, runtime_lock_id, action_kind, seq, payload, state, created_at, updated_at, state_reason_code, state_reason_message;

-- name: GetProcessActionCreateBlocker :one
SELECT process.state,
  EXISTS (
    SELECT 1
    FROM process_actions terminate_action
    WHERE terminate_action.project_id = process.project_id
      AND terminate_action.agent_id = process.agent_id
      AND terminate_action.process_id = process.id
      AND terminate_action.action_kind = 'terminate'
      AND terminate_action.state IN ('queued', 'accepted', 'applied', 'unknown')
  ) AS has_terminate_action,
  EXISTS (
    SELECT 1
    FROM online_daemon_runtimes online
    WHERE online.org_id = process.org_id
      AND online.machine_id = process.machine_id
  ) AS has_online_runtime
FROM processes process
WHERE process.project_id = sqlc.arg(project_id)
  AND process.agent_id = sqlc.arg(agent_id)
  AND process.id = sqlc.arg(process_id);

-- name: GetProcessActionByToolCall :one
SELECT id, org_id, project_id, agent_id, process_id, tool_call_id, runtime_lock_id, action_kind, seq, payload, state, created_at, updated_at, state_reason_code, state_reason_message
FROM process_actions
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND tool_call_id = sqlc.arg(tool_call_id);

-- name: CancelQueuedProcessActionsForAgentTurn :many
UPDATE process_actions action
SET state = 'failed',
    state_reason_code = 'agent_canceled_before_grant',
    state_reason_message = 'agent was canceled before process action execution was granted',
    updated_at = statement_timestamp()
FROM tool_call_read_projection tool_call
WHERE action.project_id = sqlc.arg(project_id)
  AND action.agent_id = sqlc.arg(agent_id)
  AND action.state = 'queued'
  AND action.tool_call_id = tool_call.id
  AND tool_call.project_id = action.project_id
  AND tool_call.agent_id = action.agent_id
  AND tool_call.turn_id = sqlc.arg(turn_id)
RETURNING action.id;

-- name: CancelAcceptedProcessActionsForAgentTurn :execrows
UPDATE process_actions action
SET state = CASE WHEN action.action_kind = 'read' THEN 'failed' ELSE 'unknown' END,
    state_reason_code = CASE
      WHEN action.action_kind = 'read' THEN 'agent_canceled'
      ELSE 'agent_canceled_after_grant'
    END,
    state_reason_message = CASE
      WHEN action.action_kind = 'read' THEN 'agent was canceled while process output was being read'
      ELSE 'agent was canceled after process action execution was granted'
    END,
    updated_at = statement_timestamp()
FROM tool_call_read_projection tool_call
WHERE action.project_id = sqlc.arg(project_id)
  AND action.agent_id = sqlc.arg(agent_id)
  AND action.state = 'accepted'
  AND action.tool_call_id = tool_call.id
  AND tool_call.project_id = action.project_id
  AND tool_call.agent_id = action.agent_id
  AND tool_call.turn_id = sqlc.arg(turn_id);

-- name: ResetAcceptedReadProcessActionsForMachine :execrows
UPDATE process_actions action
SET state = 'queued',
    updated_at = statement_timestamp()
FROM processes process
WHERE action.org_id = sqlc.arg(org_id)
  AND action.process_id = process.id
  AND action.project_id = process.project_id
  AND action.agent_id = process.agent_id
  AND process.machine_id = sqlc.arg(machine_id)
  AND action.action_kind = 'read'
  AND action.state = 'accepted';

-- name: GetProcessActionForReport :one
SELECT id, org_id, project_id, agent_id, process_id, tool_call_id, runtime_lock_id, action_kind, seq, payload, state, created_at, updated_at, state_reason_code, state_reason_message
FROM process_actions
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND process_id = sqlc.arg(process_id)
  AND id = sqlc.arg(id);

-- name: GetDaemonProcessActionForMachineReport :one
SELECT action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message
FROM process_actions action
JOIN processes process ON process.project_id = action.project_id
  AND process.agent_id = action.agent_id
  AND process.id = action.process_id
JOIN reportable_daemon_runtimes runtime ON runtime.org_id = process.org_id
  AND runtime.machine_id = process.machine_id
WHERE action.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND runtime.id = sqlc.arg(daemon_runtime_id)
  AND runtime.daemon_token_id = sqlc.arg(daemon_token_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.id = sqlc.arg(id);

-- name: ListProcessActionsAfterResolvedSequence :many
SELECT action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message
FROM process_actions action
JOIN processes process ON process.project_id = action.project_id
  AND process.agent_id = action.agent_id
  AND process.id = action.process_id
WHERE action.org_id = sqlc.arg(org_id)
  AND process.machine_id = sqlc.arg(machine_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.seq > sqlc.arg(resolved_action_seq)
ORDER BY action.seq, action.id;

-- name: AcceptedProcessActionAtOrBelowResolvedSequenceExists :one
SELECT EXISTS (
  SELECT 1
  FROM process_actions action
  JOIN processes process ON process.project_id = action.project_id
    AND process.agent_id = action.agent_id
    AND process.id = action.process_id
  WHERE action.org_id = sqlc.arg(org_id)
    AND process.machine_id = sqlc.arg(machine_id)
    AND action.process_id = sqlc.arg(process_id)
    AND action.seq <= sqlc.arg(resolved_action_seq)
    AND action.state = 'accepted'
);

-- name: EarlierNonTerminalProcessActionExists :one
SELECT EXISTS (
  SELECT 1
  FROM process_actions action
  JOIN process_actions prior ON prior.project_id = action.project_id
    AND prior.agent_id = action.agent_id
    AND prior.process_id = action.process_id
    AND prior.seq < action.seq
    AND prior.state IN ('queued', 'accepted')
  WHERE action.project_id = sqlc.arg(project_id)
    AND action.agent_id = sqlc.arg(agent_id)
    AND action.process_id = sqlc.arg(process_id)
    AND action.id = sqlc.arg(id)
    AND action.state = 'accepted'
);

-- name: ListDaemonProcessActionOffers :many
WITH runtime AS MATERIALIZED (
  SELECT daemon_runtimes.org_id, daemon_runtimes.machine_id
  FROM online_daemon_runtimes daemon_runtimes
  WHERE daemon_runtimes.id = sqlc.arg(daemon_runtime_id)::uuid
    AND daemon_runtimes.org_id = sqlc.arg(org_id)
    AND daemon_runtimes.machine_id = sqlc.arg(machine_id)
    AND daemon_runtimes.daemon_token_id = sqlc.arg(daemon_token_id)::uuid
),
daemon_process AS MATERIALIZED (
  SELECT process.project_id, process.agent_id, process.id, process.state
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
    AND process.state IN ('running', 'exited', 'failed', 'killed', 'unknown')
),
eligible_action AS MATERIALIZED (
  SELECT action.id
  FROM daemon_process process
  JOIN process_actions action ON action.project_id = process.project_id
    AND process.agent_id = action.agent_id
    AND process.id = action.process_id
  WHERE action.org_id = sqlc.arg(org_id)
    AND action.state = 'queued'
    AND (
      process.state = 'running'
      OR (
        action.action_kind = 'read'
        AND process.state IN ('exited', 'failed', 'killed', 'unknown')
      )
    )
    AND NOT EXISTS (
      SELECT 1
      FROM process_actions prior
      WHERE prior.project_id = action.project_id
        AND prior.agent_id = action.agent_id
        AND prior.process_id = action.process_id
        AND prior.seq < action.seq
        AND prior.state IN ('queued', 'accepted')
    )
    AND NOT EXISTS (
      SELECT 1
      FROM process_actions accepted
      WHERE accepted.project_id = action.project_id
        AND accepted.agent_id = action.agent_id
        AND accepted.process_id = action.process_id
        AND accepted.state = 'accepted'
    )
)
SELECT action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message
FROM process_actions action
JOIN eligible_action eligible ON eligible.id = action.id
ORDER BY action.process_id, action.seq, action.id
LIMIT sqlc.arg(limit_count);

-- name: LockDaemonProcessActionForAccept :one
SELECT action.id
FROM process_actions action
WHERE action.org_id = sqlc.arg(org_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.id = sqlc.arg(id)
FOR UPDATE;

-- name: AcceptDaemonProcessAction :one
WITH runtime AS MATERIALIZED (
  SELECT daemon_runtimes.org_id, daemon_runtimes.machine_id
  FROM online_daemon_runtimes daemon_runtimes
  WHERE daemon_runtimes.id = sqlc.arg(daemon_runtime_id)::uuid
    AND daemon_runtimes.org_id = sqlc.arg(org_id)
    AND daemon_runtimes.machine_id = sqlc.arg(machine_id)
    AND daemon_runtimes.daemon_token_id = sqlc.arg(daemon_token_id)::uuid
)
UPDATE process_actions action
SET state = 'accepted',
    updated_at = statement_timestamp()
FROM runtime
CROSS JOIN processes process
WHERE action.id = sqlc.arg(id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.project_id = process.project_id
  AND action.agent_id = process.agent_id
  AND action.process_id = process.id
  AND action.org_id = runtime.org_id
  AND process.machine_id = runtime.machine_id
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
  AND (
    process.state = 'running'
    OR (
      action.action_kind = 'read'
      AND process.state IN ('exited', 'failed', 'killed', 'unknown')
    )
  )
  AND action.state = 'queued'
  AND NOT EXISTS (
    SELECT 1
    FROM process_actions prior
    WHERE prior.project_id = action.project_id
      AND prior.agent_id = action.agent_id
      AND prior.process_id = action.process_id
      AND prior.seq < action.seq
      AND prior.state IN ('queued', 'accepted')
  )
  AND NOT EXISTS (
    SELECT 1
    FROM process_actions accepted
    WHERE accepted.project_id = action.project_id
      AND accepted.agent_id = action.agent_id
      AND accepted.process_id = action.process_id
      AND accepted.state = 'accepted'
  )
RETURNING action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message, process.state AS process_state, process.default_output_cursor;

-- name: MarkProcessActionApplied :one
UPDATE process_actions action
SET state = 'applied',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    updated_at = statement_timestamp()
WHERE action.project_id = sqlc.arg(project_id)
  AND action.agent_id = sqlc.arg(agent_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.id = sqlc.arg(id)
  AND action.state = 'accepted'
  AND NOT EXISTS (
    SELECT 1
    FROM process_actions prior
    WHERE prior.project_id = action.project_id
      AND prior.agent_id = action.agent_id
      AND prior.process_id = action.process_id
      AND prior.seq < action.seq
      AND prior.state IN ('queued', 'accepted')
  )
RETURNING action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message;

-- name: TouchProcessActivity :exec
UPDATE processes
SET last_activity_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(process_id);

-- name: MarkProcessActionFailed :one
UPDATE process_actions action
SET state = 'failed',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    updated_at = statement_timestamp()
WHERE action.project_id = sqlc.arg(project_id)
  AND action.agent_id = sqlc.arg(agent_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.id = sqlc.arg(id)
  AND action.state = 'accepted'
  AND NOT EXISTS (
    SELECT 1
    FROM process_actions prior
    WHERE prior.project_id = action.project_id
      AND prior.agent_id = action.agent_id
      AND prior.process_id = action.process_id
      AND prior.seq < action.seq
      AND prior.state IN ('queued', 'accepted')
  )
RETURNING action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message;

-- name: MarkProcessActionUnknown :one
UPDATE process_actions action
SET state = 'unknown',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    updated_at = statement_timestamp()
WHERE action.project_id = sqlc.arg(project_id)
  AND action.agent_id = sqlc.arg(agent_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.id = sqlc.arg(id)
  AND action.state = 'accepted'
  AND NOT EXISTS (
    SELECT 1
    FROM process_actions prior
    WHERE prior.project_id = action.project_id
      AND prior.agent_id = action.agent_id
      AND prior.process_id = action.process_id
      AND prior.seq < action.seq
      AND prior.state IN ('queued', 'accepted')
  )
RETURNING action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message;

-- name: ResolveAcceptedProcessActionsWithoutEvidence :many
UPDATE process_actions action
SET state = CASE WHEN action.action_kind = 'read' THEN 'failed' ELSE 'unknown' END,
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    updated_at = statement_timestamp()
WHERE action.org_id = sqlc.arg(org_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.state = 'accepted'
  AND EXISTS (
    SELECT 1
    FROM tool_calls tool_call
    WHERE tool_call.agent_id = action.agent_id
      AND tool_call.id = action.tool_call_id
      AND tool_call.type = 'built_in'
      AND tool_call.state = 'waiting'
  )
RETURNING action.id, action.org_id, action.project_id, action.agent_id, action.process_id, action.tool_call_id, action.runtime_lock_id, action.action_kind, action.seq, action.payload, action.state, action.created_at, action.updated_at, action.state_reason_code, action.state_reason_message;

-- name: MarkQueuedProcessActionsFailedForProcess :many
UPDATE process_actions action
SET state = 'failed',
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    updated_at = statement_timestamp()
WHERE action.org_id = sqlc.arg(org_id)
  AND action.process_id = sqlc.arg(process_id)
  AND (sqlc.narg(action_id)::uuid IS NULL OR action.id = sqlc.narg(action_id)::uuid)
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

-- name: MarkQueuedMutatingProcessActionsFailedForTerminalProcess :many
UPDATE process_actions action
SET state = 'failed',
    state_reason_code = 'process_terminal',
    state_reason_message = 'the process ended before this action was accepted',
    updated_at = statement_timestamp()
WHERE action.org_id = sqlc.arg(org_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.action_kind IN ('write', 'interrupt')
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

-- name: ResolveQueuedTerminateActionsForTerminalProcess :many
UPDATE process_actions action
SET state = CASE
      WHEN sqlc.arg(process_state)::text = 'unknown' THEN 'failed'
      ELSE 'applied'
    END,
    state_reason_code = CASE
      WHEN sqlc.arg(process_state)::text = 'unknown' THEN 'process_state_unknown'
      ELSE 'already_stopped'
    END,
    state_reason_message = CASE
      WHEN sqlc.arg(process_state)::text = 'unknown'
        THEN 'the process may still exist, so stopping it cannot be confirmed'
      ELSE ''
    END,
    updated_at = statement_timestamp()
WHERE action.org_id = sqlc.arg(org_id)
  AND action.process_id = sqlc.arg(process_id)
  AND action.action_kind = 'terminate'
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

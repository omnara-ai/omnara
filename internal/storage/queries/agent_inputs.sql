-- name: ClaimNextAgentWakeup :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.id AS agent_id, agent.project_id, agent.root_agent_id
  FROM agent_wakeups wake
  JOIN agents agent ON agent.id = wake.agent_id
  WHERE agent.state <> 'archived'
    AND wake.ready_at <= statement_timestamp()
  ORDER BY wake.ready_at ASC, wake.agent_id ASC
  FOR UPDATE OF agent SKIP LOCKED
  LIMIT 1
),
locked_wake AS MATERIALIZED (
  SELECT wake.agent_id, agent.project_id, agent.root_agent_id
  FROM agent_wakeups wake
  JOIN locked_agent agent ON agent.agent_id = wake.agent_id
  WHERE NOT EXISTS (
      SELECT 1
      FROM agent_runtime_locks runtime_lock
      WHERE runtime_lock.agent_id = wake.agent_id
    )
  FOR UPDATE OF wake
)
SELECT agent_id, project_id, root_agent_id
FROM locked_wake;

-- name: LockClaimableAgentWakeup :one
SELECT wake.agent_id
FROM agent_wakeups wake JOIN agents agent ON agent.id=wake.agent_id
WHERE wake.agent_id=sqlc.arg(agent_id) AND agent.state<>'archived'
  AND wake.ready_at<=statement_timestamp()
  AND NOT EXISTS(SELECT 1 FROM agent_runtime_locks runtime WHERE runtime.agent_id=wake.agent_id)
FOR UPDATE OF wake;

-- name: ListQueuedBacklogInputs :many
SELECT input.id, input.project_id, input.agent_id, input.state, input.input_rank, input.actor_id, input.input_kind, input.integration_target_id, coalesce(input.idempotency_scope, '') AS idempotency_scope, coalesce(input.input_idempotency_key, '') AS input_idempotency_key, input.queued_at, input.admitted_event_id, input.admitted_at, input.canceled_at, input.delivery_mode, coalesce(input.control_type, '') AS control_type, input.target_interaction_id, input.resolved_at, input.metadata, input.scheduled_for
FROM agent_inputs input
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.delivery_mode IN ('steering', 'queued')
  AND input.input_kind = 'content'
  -- Each state arm matches its own partial index.
  AND (
    (
      input.state = 'received'
      AND (
        sqlc.narg(cursor_id)::uuid IS NULL
        OR (
          sqlc.narg(cursor_scheduled_for)::timestamptz IS NULL
          AND (
            input.delivery_mode < sqlc.narg(cursor_delivery_mode)::text
            OR (
              input.delivery_mode = sqlc.narg(cursor_delivery_mode)::text
              AND (input.input_rank, input.queued_at, input.id) > (sqlc.narg(cursor_input_rank)::bigint, sqlc.narg(cursor_queued_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
            )
          )
        )
      )
    )
    OR (
      input.state = 'scheduled'
      AND (
        sqlc.narg(cursor_id)::uuid IS NULL
        OR sqlc.narg(cursor_scheduled_for)::timestamptz IS NULL
        OR (input.scheduled_for, input.id) > (sqlc.narg(cursor_scheduled_for)::timestamptz, sqlc.narg(cursor_id)::uuid)
      )
    )
  )
-- Waiting inputs come first in delivery order; scheduled inputs follow by due time.
ORDER BY
  (input.state = 'scheduled') ASC,
  CASE WHEN input.state = 'scheduled' THEN '' ELSE input.delivery_mode END DESC,
  CASE WHEN input.state = 'scheduled' THEN 0 ELSE input.input_rank END ASC,
  CASE WHEN input.state = 'scheduled' THEN input.scheduled_for ELSE input.queued_at END ASC,
  input.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- The cursor lets one pass walk past inputs whose release failed, so they
-- cannot hold back later due inputs.
-- name: ListDueScheduledAgentInputs :many
SELECT input.id, input.project_id, input.agent_id, input.scheduled_for
FROM agent_inputs input
JOIN agents agent ON agent.id = input.agent_id
WHERE input.state = 'scheduled'
  AND input.scheduled_for <= statement_timestamp()
  AND agent.state <> 'archived'
  AND (
    sqlc.narg(after_id)::uuid IS NULL
    OR (input.scheduled_for, input.id) > (sqlc.narg(after_scheduled_for)::timestamptz, sqlc.narg(after_id)::uuid)
  )
ORDER BY input.scheduled_for ASC, input.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- Wakeups exist only while no runtime lock is held, so updated_at marks when a
-- backdated ready_at became claimable.
-- name: SampleReadyAgentBacklog :one
SELECT count(*)::bigint AS ready_count,
  coalesce(EXTRACT(EPOCH FROM statement_timestamp() - min(greatest(wake.ready_at, wake.updated_at))), 0)::double precision
    AS oldest_ready_age_seconds
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.state <> 'archived'
  AND wake.ready_at <= statement_timestamp()
  AND NOT EXISTS (
    SELECT 1 FROM agent_runtime_locks runtime_lock WHERE runtime_lock.agent_id = wake.agent_id
  );

-- name: ListScheduledAgentInputsByActor :many
SELECT input.id, input.queued_at, input.scheduled_for
FROM agent_inputs input
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.state = 'scheduled'
  AND input.actor_id = sqlc.arg(actor_id)::uuid
ORDER BY input.scheduled_for ASC, input.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

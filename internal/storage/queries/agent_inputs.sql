-- name: ClaimNextAgentWakeup :one
-- The query walks wakeups in global ready order, but locks agents before wake
-- rows to match all other agent mutation paths. If the selected wake is no
-- longer claimable after the runtime-lock recheck, this returns no rows and the
-- worker retries on its next poll; no work is lost.
WITH locked_agent AS MATERIALIZED (
  SELECT agent.id AS agent_id, agent.project_id
  FROM agent_wakeups wake
  JOIN agents agent ON agent.id = wake.agent_id
  WHERE agent.state <> 'archived'
    AND wake.ready_at <= statement_timestamp()
    AND NOT EXISTS (
      SELECT 1
      FROM agent_runtime_locks runtime_lock
      WHERE runtime_lock.agent_id = wake.agent_id
    )
  ORDER BY wake.ready_at ASC, wake.agent_id ASC
  FOR UPDATE OF agent SKIP LOCKED
  LIMIT 1
),
locked_wake AS MATERIALIZED (
  SELECT wake.agent_id, agent.project_id
  FROM agent_wakeups wake
  JOIN locked_agent agent ON agent.agent_id = wake.agent_id
  WHERE NOT EXISTS (
      SELECT 1
      FROM agent_runtime_locks runtime_lock
      WHERE runtime_lock.agent_id = wake.agent_id
    )
  FOR UPDATE OF wake
)
SELECT agent_id, project_id
FROM locked_wake;

-- name: ListQueuedBacklogInputs :many
SELECT input.id, input.project_id, input.agent_id, input.state, input.input_rank, input.actor_id, input.input_kind, input.integration_target_id, coalesce(input.idempotency_scope, '') AS idempotency_scope, coalesce(input.input_idempotency_key, '') AS input_idempotency_key, input.queued_at, input.admitted_event_id, input.admitted_at, input.canceled_at, input.delivery_mode, coalesce(input.control_type, '') AS control_type, input.target_interaction_id, input.resolved_at, coalesce(input.rejected_reason, '') AS rejected_reason, input.metadata
FROM agent_inputs input
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.state = 'received'
  AND input.delivery_mode IN ('steering', 'queued')
  AND input.input_kind = 'content'
  AND (
    sqlc.narg(cursor_delivery_mode)::text IS NULL
    OR input.delivery_mode < sqlc.narg(cursor_delivery_mode)::text
    OR (
      input.delivery_mode = sqlc.narg(cursor_delivery_mode)::text
      AND (input.input_rank, input.queued_at, input.id) > (sqlc.narg(cursor_input_rank)::bigint, sqlc.narg(cursor_queued_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
    )
  )
ORDER BY input.delivery_mode DESC, input.input_rank ASC, input.queued_at ASC, input.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

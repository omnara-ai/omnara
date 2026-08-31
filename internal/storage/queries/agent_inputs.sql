-- name: MarkAgentWakeup :exec
INSERT INTO agent_wakeups(agent_id, ready_at, updated_at, metadata)
SELECT agent.id,
       statement_timestamp(), statement_timestamp(), sqlc.arg(metadata)
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
  AND agent.state <> 'archived'
ON CONFLICT (agent_id) DO UPDATE
SET ready_at = LEAST(agent_wakeups.ready_at, excluded.ready_at),
    updated_at = GREATEST(agent_wakeups.updated_at, excluded.updated_at),
    metadata = agent_wakeups.metadata || excluded.metadata;

-- name: ConsumeAgentWakeup :execrows
DELETE FROM agent_wakeups wake
USING agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
  AND wake.agent_id = agent.id;

-- name: ReconcileAgentWakeup :exec
WITH locked_agent AS MATERIALIZED (
  SELECT agent.id, agent.project_id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
    AND agent.state <> 'archived'
),
desired AS MATERIALIZED (
  SELECT agent_next_wakeup_ready_at(
    locked_agent.project_id,
    locked_agent.id
  ) AS ready_at
  FROM locked_agent
),
upserted AS (
  INSERT INTO agent_wakeups(agent_id, ready_at, updated_at, metadata)
  SELECT locked_agent.id,
         desired.ready_at,
         statement_timestamp(),
         sqlc.arg(metadata)
  FROM locked_agent
  JOIN desired ON desired.ready_at IS NOT NULL
  ON CONFLICT (agent_id) DO UPDATE
  SET ready_at = excluded.ready_at,
      updated_at = GREATEST(agent_wakeups.updated_at, excluded.updated_at),
      metadata = agent_wakeups.metadata || excluded.metadata
  RETURNING agent_id
)
DELETE FROM agent_wakeups wake
USING locked_agent
CROSS JOIN desired
WHERE wake.agent_id = locked_agent.id
  AND desired.ready_at IS NULL;

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

-- name: DeleteAgentWakeup :execrows
WITH locked AS (
  SELECT agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
DELETE FROM agent_wakeups wake
USING locked
WHERE wake.agent_id = locked.id;

-- name: ListSteeringAgentInputsForAdmission :many
SELECT input.id, input.project_id, input.agent_id, input.state, input.input_rank, input.actor_id, input.input_kind, input.integration_target_id, coalesce(input.idempotency_scope, '') AS idempotency_scope, coalesce(input.input_idempotency_key, '') AS input_idempotency_key, input.queued_at, input.admitted_event_id, input.admitted_at, input.canceled_at, input.delivery_mode, coalesce(input.control_type, '') AS control_type, input.target_interaction_id, input.resolved_at, coalesce(input.rejected_reason, '') AS rejected_reason, input.metadata
FROM agent_inputs input
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.state = 'received'
  AND input.delivery_mode = 'steering'
  AND input.input_kind = 'content'
  AND NOT agent_has_incomplete_tool_batch(sqlc.arg(project_id), sqlc.arg(agent_id))
ORDER BY input.input_rank ASC, input.queued_at ASC, input.id ASC
FOR UPDATE;

-- name: GetNextQueuedAgentInputForAdmission :one
SELECT input.id, input.project_id, input.agent_id, input.state, input.input_rank, input.actor_id, input.input_kind, input.integration_target_id, coalesce(input.idempotency_scope, '') AS idempotency_scope, coalesce(input.input_idempotency_key, '') AS input_idempotency_key, input.queued_at, input.admitted_event_id, input.admitted_at, input.canceled_at, input.delivery_mode, coalesce(input.control_type, '') AS control_type, input.target_interaction_id, input.resolved_at, coalesce(input.rejected_reason, '') AS rejected_reason, input.metadata
FROM agent_inputs input
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.state = 'received'
  AND input.delivery_mode = 'queued'
  AND input.input_kind = 'content'
ORDER BY input.input_rank ASC, input.queued_at ASC, input.id ASC
FOR UPDATE
LIMIT 1;

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

-- name: AdmitAgentInput :one
UPDATE agent_inputs input
SET state = 'resolved',
    admitted_event_id = sqlc.arg(admitted_event_id)::uuid,
    admitted_at = event.created_at,
    resolved_at = event.created_at
FROM agent_events event
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(id)
  AND input.state = 'received'
  AND event.agent_id = input.agent_id
  AND event.agent_input_id = input.id
  AND event.id = sqlc.arg(admitted_event_id)
RETURNING input.admitted_at, input.resolved_at;

-- name: CancelQueuedBacklogInput :execrows
UPDATE agent_inputs
SET state = 'canceled',
    canceled_at = statement_timestamp()
WHERE agent_inputs.project_id = sqlc.arg(project_id)
  AND agent_inputs.agent_id = sqlc.arg(agent_id)
  AND agent_inputs.id = sqlc.arg(id)
  AND agent_inputs.state = 'received'
  AND agent_inputs.delivery_mode = 'queued'
  AND agent_inputs.input_kind = 'content';

-- name: CancelQueuedBacklogInputsForAgent :execrows
UPDATE agent_inputs
SET state = 'canceled',
    canceled_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND state = 'received'
  AND delivery_mode = 'queued'
  AND input_kind = 'content';

-- name: QueuedBacklogMoveIsValid :one
SELECT CASE WHEN EXISTS (
  SELECT 1
  FROM agent_inputs input
  WHERE input.project_id = sqlc.arg(project_id)
    AND input.agent_id = sqlc.arg(agent_id)
    AND input.id = sqlc.arg(id)
    AND input.state = 'received'
    AND input.delivery_mode = 'queued'
    AND input.input_kind = 'content'
) AND (
  NOT sqlc.arg(requires_anchor)::boolean
  OR EXISTS (
    SELECT 1
    FROM agent_inputs anchor
    WHERE anchor.project_id = sqlc.arg(project_id)
      AND anchor.agent_id = sqlc.arg(agent_id)
      AND anchor.id = sqlc.arg(anchor_id)
      AND anchor.state = 'received'
      AND anchor.delivery_mode = 'queued'
      AND anchor.input_kind = 'content'
  )
) THEN true ELSE false END AS valid;

-- name: MoveQueuedBacklogInputToFront :execrows
WITH first_neighbor AS (
  SELECT input_rank
  FROM agent_inputs
  WHERE project_id = sqlc.arg(project_id)
    AND agent_id = sqlc.arg(agent_id)
    AND state = 'received'
    AND delivery_mode = 'queued'
    AND input_kind = 'content'
    AND id <> sqlc.arg(id)
  ORDER BY input_rank ASC, queued_at ASC, id ASC
  LIMIT 1
),
new_rank AS (
  SELECT CASE
    WHEN (SELECT input_rank FROM first_neighbor) IS NULL THEN sqlc.arg(rank_stride)::bigint
    WHEN (SELECT input_rank FROM first_neighbor) > 1 THEN
      (SELECT input_rank / 2 FROM first_neighbor)
    ELSE NULL::bigint
  END AS input_rank
)
UPDATE agent_inputs input
SET input_rank = new_rank.input_rank
FROM new_rank
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(id)
  AND input.state = 'received'
  AND input.delivery_mode = 'queued'
  AND input.input_kind = 'content'
  AND new_rank.input_rank IS NOT NULL;

-- name: MoveQueuedBacklogInputToBack :execrows
WITH last_neighbor AS (
  SELECT input_rank
  FROM agent_inputs
  WHERE project_id = sqlc.arg(project_id)
    AND agent_id = sqlc.arg(agent_id)
    AND state = 'received'
    AND delivery_mode = 'queued'
    AND input_kind = 'content'
    AND id <> sqlc.arg(id)
  ORDER BY input_rank DESC, queued_at DESC, id DESC
  LIMIT 1
),
new_rank AS (
  SELECT CASE
    WHEN (SELECT input_rank FROM last_neighbor) IS NULL THEN sqlc.arg(rank_stride)::bigint
    ELSE (SELECT input_rank + sqlc.arg(rank_stride)::bigint FROM last_neighbor)
  END AS input_rank
)
UPDATE agent_inputs input
SET input_rank = new_rank.input_rank
FROM new_rank
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(id)
  AND input.state = 'received'
  AND input.delivery_mode = 'queued'
  AND input.input_kind = 'content'
  AND new_rank.input_rank IS NOT NULL;

-- name: MoveQueuedBacklogInputBefore :execrows
WITH target AS (
  SELECT anchor.id, anchor.input_rank, anchor.queued_at
  FROM agent_inputs anchor
  WHERE anchor.project_id = sqlc.arg(project_id)
    AND anchor.agent_id = sqlc.arg(agent_id)
    AND anchor.state = 'received'
    AND anchor.delivery_mode = 'queued'
    AND anchor.input_kind = 'content'
    AND anchor.id = sqlc.arg(anchor_id)
),
previous_neighbor AS (
  SELECT candidate.input_rank
  FROM agent_inputs candidate
  CROSS JOIN target
  WHERE candidate.project_id = sqlc.arg(project_id)
    AND candidate.agent_id = sqlc.arg(agent_id)
    AND candidate.state = 'received'
    AND candidate.delivery_mode = 'queued'
    AND candidate.input_kind = 'content'
    AND candidate.id <> sqlc.arg(id)
    AND (candidate.input_rank, candidate.queued_at, candidate.id) < (target.input_rank, target.queued_at, target.id)
  ORDER BY candidate.input_rank DESC, candidate.queued_at DESC, candidate.id DESC
  LIMIT 1
),
new_rank AS (
  SELECT CASE
    WHEN (SELECT id FROM target) IS NULL THEN NULL
    WHEN (SELECT input_rank FROM previous_neighbor) IS NULL AND
         (SELECT input_rank FROM target) > 1 THEN
      (SELECT input_rank / 2 FROM target)
    WHEN (SELECT input_rank FROM target) - (SELECT input_rank FROM previous_neighbor) > 1 THEN
      (SELECT input_rank FROM previous_neighbor) +
      ((SELECT input_rank FROM target) - (SELECT input_rank FROM previous_neighbor)) / 2
    ELSE NULL::bigint
  END AS input_rank
)
UPDATE agent_inputs input
SET input_rank = new_rank.input_rank
FROM new_rank
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(id)
  AND input.state = 'received'
  AND input.delivery_mode = 'queued'
  AND input.input_kind = 'content'
  AND new_rank.input_rank IS NOT NULL;

-- name: MoveQueuedBacklogInputAfter :execrows
WITH target AS (
  SELECT anchor.id, anchor.input_rank, anchor.queued_at
  FROM agent_inputs anchor
  WHERE anchor.project_id = sqlc.arg(project_id)
    AND anchor.agent_id = sqlc.arg(agent_id)
    AND anchor.state = 'received'
    AND anchor.delivery_mode = 'queued'
    AND anchor.input_kind = 'content'
    AND anchor.id = sqlc.arg(anchor_id)
),
next_neighbor AS (
  SELECT candidate.input_rank
  FROM agent_inputs candidate
  CROSS JOIN target
  WHERE candidate.project_id = sqlc.arg(project_id)
    AND candidate.agent_id = sqlc.arg(agent_id)
    AND candidate.state = 'received'
    AND candidate.delivery_mode = 'queued'
    AND candidate.input_kind = 'content'
    AND candidate.id <> sqlc.arg(id)
    AND (candidate.input_rank, candidate.queued_at, candidate.id) > (target.input_rank, target.queued_at, target.id)
  ORDER BY candidate.input_rank ASC, candidate.queued_at ASC, candidate.id ASC
  LIMIT 1
),
new_rank AS (
  SELECT CASE
    WHEN (SELECT id FROM target) IS NULL THEN NULL
    WHEN (SELECT input_rank FROM next_neighbor) IS NULL THEN
      (SELECT input_rank + sqlc.arg(rank_stride)::bigint FROM target)
    WHEN (SELECT input_rank FROM next_neighbor) - (SELECT input_rank FROM target) > 1 THEN
      (SELECT input_rank FROM target) +
      ((SELECT input_rank FROM next_neighbor) - (SELECT input_rank FROM target)) / 2
    ELSE NULL::bigint
  END AS input_rank
)
UPDATE agent_inputs input
SET input_rank = new_rank.input_rank
FROM new_rank
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(id)
  AND input.state = 'received'
  AND input.delivery_mode = 'queued'
  AND input.input_kind = 'content'
  AND new_rank.input_rank IS NOT NULL;

-- name: PromoteQueuedInputToSteering :one
WITH promoted AS (
  UPDATE agent_inputs input
  SET delivery_mode = 'steering',
      input_rank = coalesce(
        (
          SELECT max(existing.input_rank) + sqlc.arg(rank_stride)::bigint
          FROM agent_inputs existing
          WHERE existing.project_id = input.project_id
            AND existing.agent_id = input.agent_id
            AND existing.delivery_mode = 'steering'
            AND existing.state = 'received'
            AND existing.input_kind = 'content'
        ),
        sqlc.arg(rank_stride)::bigint
      )
  WHERE input.project_id = sqlc.arg(project_id)
    AND input.agent_id = sqlc.arg(agent_id)
    AND input.id = sqlc.arg(id)
    AND input.state = 'received'
    AND input.delivery_mode = 'queued'
    AND input.input_kind = 'content'
  RETURNING TRUE
)
SELECT EXISTS (SELECT 1 FROM promoted) AS changed,
       EXISTS (SELECT 1 FROM promoted)
       OR EXISTS (
         SELECT 1
         FROM agent_inputs input
         WHERE input.project_id = sqlc.arg(project_id)
           AND input.agent_id = sqlc.arg(agent_id)
           AND input.id = sqlc.arg(id)
           AND input.input_kind = 'content'
           AND (
             (input.state = 'received' AND input.delivery_mode = 'steering')
             OR (input.state = 'resolved' AND input.admitted_event_id IS NOT NULL)
           )
       ) AS effective;

-- name: DemoteSteeringInputToQueued :execrows
UPDATE agent_inputs input
SET delivery_mode = 'queued',
    input_rank = coalesce(
      (
        SELECT max(existing.input_rank) + sqlc.arg(rank_stride)::bigint
        FROM agent_inputs existing
        WHERE existing.project_id = input.project_id
          AND existing.agent_id = input.agent_id
          AND existing.delivery_mode = 'queued'
          AND existing.state = 'received'
          AND existing.input_kind = 'content'
      ),
      sqlc.arg(rank_stride)::bigint
    )
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(id)
  AND input.state = 'received'
  AND input.delivery_mode = 'steering'
  AND input.input_kind = 'content';

-- name: RebalanceQueuedBacklogRanks :execrows
WITH ordered AS (
  SELECT id, row_number() OVER (ORDER BY input_rank ASC, queued_at ASC, id ASC) AS row_number
  FROM agent_inputs
  WHERE project_id = sqlc.arg(project_id)
    AND agent_id = sqlc.arg(agent_id)
    AND state = 'received'
    AND delivery_mode = 'queued'
    AND input_kind = 'content'
)
UPDATE agent_inputs input
SET input_rank = ordered.row_number * sqlc.arg(rank_stride)::bigint
FROM ordered
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = ordered.id;

-- name: CancelSteeringAgentInputsForAgent :execrows
UPDATE agent_inputs
SET state = 'canceled',
    canceled_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND state = 'received'
  AND delivery_mode = 'steering'
  AND input_kind = 'content';

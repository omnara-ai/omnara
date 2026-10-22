-- name: ClaimNextAgentWakeup :one
WITH candidate AS MATERIALIZED (
  SELECT agent.id,agent.project_id,agent.root_agent_id,agent.org_id,agent.state,agent.current_config_id,agent.next_event_sequence,locked_wake.agent_id
  FROM agent_wakeups wake
  CROSS JOIN LATERAL (
    SELECT a.id, a.project_id, a.root_agent_id, a.org_id, a.state,
           a.current_config_id, a.next_event_sequence
    FROM agents a
    WHERE a.id=wake.agent_id AND a.state<>'archived'
    FOR UPDATE OF a SKIP LOCKED
  ) agent
  CROSS JOIN LATERAL (
    SELECT w.agent_id
    FROM agent_wakeups w
    WHERE w.agent_id=agent.id AND w.ready_at<=statement_timestamp()
      AND NOT EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.agent_id=w.agent_id)
    FOR UPDATE OF w SKIP LOCKED
  ) locked_wake
  WHERE wake.ready_at<=statement_timestamp()
  ORDER BY wake.ready_at, wake.agent_id
  LIMIT 1
)
SELECT c.agent_id,c.project_id,c.root_agent_id,c.org_id,c.state,
       h.current_turn_id,coalesce(h.stop_sequence,0)::bigint AS stop_sequence,
       coalesce(h.max_normal_input_sequence,0)::bigint AS max_normal_input_sequence,
       coalesce(h.max_context_input_sequence,0)::bigint AS max_context_input_sequence,
       h.normal_context_id,h.compaction_context_id,h.pending_tool_output_id,h.pending_output_limit_id,
       h.pending_config_input_id,h.pending_checkpoint_id,
       coalesce(h.incomplete_tools,false)::boolean AS incomplete_tools,h.logical_ready_at,
       (h.agent_id IS NOT NULL)::boolean AS head_exists,
       c.current_config_id,(c.next_event_sequence-1)::bigint AS watermark,
       EXISTS(SELECT 1 FROM model_call_contexts m WHERE m.agent_id=c.agent_id AND m.state='started')::boolean AS started,
       statement_timestamp()::timestamptz AS database_now
FROM candidate c LEFT JOIN LATERAL (
 SELECT head.agent_id,head.current_turn_id,head.stop_sequence,head.max_normal_input_sequence,head.max_context_input_sequence,head.normal_context_id,head.compaction_context_id,head.pending_tool_output_id,head.pending_output_limit_id,head.pending_config_input_id,head.pending_checkpoint_id,head.incomplete_tools,head.logical_ready_at FROM agent_execution_state head WHERE head.agent_id=c.agent_id FOR UPDATE OF head
) h ON true;

-- name: LockClaimableAgentWakeup :one
SELECT wake.agent_id
FROM agent_wakeups wake JOIN agents agent ON agent.id=wake.agent_id
WHERE wake.agent_id=sqlc.arg(agent_id) AND agent.state<>'archived'
  AND wake.ready_at<=statement_timestamp()
  AND NOT EXISTS(SELECT 1 FROM agent_runtime_locks runtime WHERE runtime.agent_id=wake.agent_id)
FOR UPDATE OF wake;

-- name: ListQueuedBacklogInputs :many
SELECT input.id, input.project_id, input.agent_id, input.state, input.input_rank, input.actor_id, input.input_kind, input.integration_target_id, coalesce(input.idempotency_scope, '') AS idempotency_scope, coalesce(input.input_idempotency_key, '') AS input_idempotency_key, input.queued_at, input.admitted_event_id, input.admitted_at, input.canceled_at, input.delivery_mode, coalesce(input.control_type, '') AS control_type, input.target_interaction_id, input.resolved_at, input.metadata
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

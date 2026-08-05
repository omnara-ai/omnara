-- name: GetEventByProjectAgentIdempotencyKey :one
SELECT event.id, event.agent_id, event.turn_id, event.is_opening_event, event.sequence, event.event_kind, event.created_at, coalesce(event.idempotency_key, '') AS idempotency_key
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = sqlc.arg(project_id) AND event.agent_id = sqlc.arg(agent_id) AND event.idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: AllocateEventSequence :one
SELECT project_id, next_event_sequence
FROM agents AS agents
WHERE id = $1
FOR UPDATE;

-- name: LatestAgentEvent :one
SELECT event.id, event.agent_id, event.turn_id, event.is_opening_event, event.sequence, event.event_kind, event.created_at, coalesce(event.idempotency_key, '') AS idempotency_key
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1 AND event.agent_id = $2
ORDER BY sequence DESC
LIMIT 1;

-- name: AdvanceEventSequence :exec
UPDATE agents
SET next_event_sequence = next_event_sequence + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id);

-- name: MaxEventSequence :one
SELECT coalesce(max(event.sequence), 0)::bigint
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1 AND event.agent_id = $2;

-- name: ListCompactionSourceEvents :many
SELECT event.id,
       event.sequence,
       event.turn_id,
       event.is_opening_event,
       event.event_kind,
       coalesce(input.input_kind, '') AS input_kind,
       event.created_at,
       coalesce(tool_call.name, '') AS tool_name,
       coalesce(tool_call.provider_call_id, '') AS provider_call_id,
       coalesce(tool_result.outcome, '') AS tool_outcome,
       (
         CASE
           WHEN event.event_kind = 'agent_input' AND input.input_kind = 'config_change' AND event.sequence > 1 THEN
             jsonb_build_array(jsonb_build_object('type', 'text', 'text', 'Agent configuration changed. The current system prompt, model, and tool policy are reflected in this model call.'))
           ELSE
             coalesce(block_projection.content_parts, '[]'::jsonb)
         END
       )::jsonb AS content_parts
FROM agent_events event
JOIN agents scoped_agent
  ON scoped_agent.project_id = sqlc.arg(project_id)
 AND scoped_agent.id = event.agent_id
LEFT JOIN agent_inputs input
  ON input.agent_id = event.agent_id
 AND input.id = event.agent_input_id
LEFT JOIN tool_call_results tool_result
  ON tool_result.agent_id = event.agent_id
 AND tool_result.id = event.tool_call_result_id
LEFT JOIN tool_calls tool_call
  ON tool_call.agent_id = tool_result.agent_id
 AND tool_call.id = tool_result.tool_call_id
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    CASE
      WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', coalesce(block.text_content, ''))
      WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
      WHEN block.block_kind = 'artifact' THEN jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id)
      WHEN block.block_kind = 'reasoning' THEN jsonb_build_object('type', 'reasoning', 'text', coalesce(block.text_content, ''))
      WHEN block.block_kind = 'error' THEN jsonb_build_object('type', 'error', 'text', coalesce(block.text_content, ''))
      WHEN block.block_kind = 'tool_call' THEN
        jsonb_build_object(
          'type', 'tool_call',
          'tool_call_id', block.tool_call_id,
          'tool_type', tool_block_call.type,
          'name', tool_block_call.name,
          'input', tool_block_call.input
        )
      ELSE NULL
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact', 'reasoning', 'tool_call', 'error')), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  LEFT JOIN tool_calls tool_block_call
    ON tool_block_call.agent_id = block.agent_id
   AND tool_block_call.id = block.tool_call_id
  WHERE block.agent_id = event.agent_id
    AND (
      block.owner_agent_input_id = event.agent_input_id
      OR block.owner_model_output_id = event.model_output_id
      OR block.owner_tool_call_result_id = event.tool_call_result_id
    )
) block_projection ON true
WHERE event.agent_id = sqlc.arg(agent_id)
  AND event.sequence > sqlc.arg(after_sequence)
ORDER BY event.sequence
LIMIT sqlc.arg(page_limit);

-- name: ListCompactionAtomicGroups :many
WITH atomic_groups AS (
  SELECT 'tool_call_result'::text AS group_kind,
         tool_call.source_event_sequence AS start_sequence,
         CASE
           WHEN bool_or(result_event.sequence IS NULL)
             THEN sqlc.arg(input_event_sequence)::bigint + 1
           ELSE max(result_event.sequence)
         END::bigint AS end_sequence
  FROM tool_call_read_projection tool_call
  LEFT JOIN tool_call_results result
    ON result.agent_id = tool_call.agent_id
   AND result.tool_call_id = tool_call.id
  LEFT JOIN agent_events result_event
    ON result_event.agent_id = result.agent_id
   AND result_event.tool_call_result_id = result.id
  WHERE tool_call.project_id = sqlc.arg(project_id)
    AND tool_call.agent_id = sqlc.arg(agent_id)
    AND tool_call.source_event_sequence <= sqlc.arg(input_event_sequence)
    AND (result_event.sequence IS NULL OR result_event.sequence > sqlc.arg(last_checkpoint_end))
  GROUP BY tool_call.source_event_sequence
  HAVING CASE
           WHEN bool_or(result_event.sequence IS NULL)
             THEN sqlc.arg(input_event_sequence)::bigint + 1
           ELSE max(result_event.sequence)
         END > tool_call.source_event_sequence

  UNION ALL

  SELECT 'turn_opening'::text AS group_kind,
         min(opening_event.sequence)::bigint AS start_sequence,
         max(opening_event.sequence)::bigint AS end_sequence
  FROM agent_events opening_event
  JOIN agents opening_agent
    ON opening_agent.project_id = sqlc.arg(project_id)
   AND opening_agent.id = opening_event.agent_id
  WHERE opening_event.agent_id = sqlc.arg(agent_id)
    AND opening_event.is_opening_event
    AND opening_event.sequence <= sqlc.arg(input_event_sequence)
  GROUP BY opening_event.turn_id
  HAVING count(*) > 1
     AND max(opening_event.sequence) > sqlc.arg(last_checkpoint_end)
)
SELECT group_kind, start_sequence, end_sequence
FROM atomic_groups
ORDER BY start_sequence, end_sequence, group_kind;

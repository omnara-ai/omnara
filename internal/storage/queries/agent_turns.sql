-- name: NextTurnSequence :one
SELECT (coalesce(max(turn.turn_sequence), 0) + 1)::bigint
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND turn.agent_id = sqlc.arg(agent_id);

-- name: ListAgentTurnsForRead :many
SELECT turn.id,
       agent.project_id,
       turn.agent_id,
       turn.turn_sequence,
       turn.latest_event_id,
       turn.latest_semantic_event_id,
       (
         SELECT count(*)::bigint
         FROM agent_events event
         WHERE event.agent_id = turn.agent_id
           AND event.turn_id = turn.id
       ) AS event_count
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND turn.agent_id = sqlc.arg(agent_id)
  AND (
    sqlc.arg(before_turn_sequence)::bigint <= 0
    OR turn.turn_sequence < sqlc.arg(before_turn_sequence)::bigint
  )
ORDER BY turn.turn_sequence DESC
LIMIT sqlc.arg(page_limit);

-- name: AgentTurnExistsInProject :one
SELECT EXISTS (
  SELECT 1
  FROM agent_turns turn
  JOIN agents agent ON agent.id = turn.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND turn.agent_id = sqlc.arg(agent_id)
    AND turn.id = sqlc.arg(id)
)::bool;

-- name: ListAgentEventsForRead :many
SELECT projection.id, projection.org_id, projection.project_id, projection.agent_id,
       projection.turn_id, projection.turn_sequence, projection.is_opening_event,
       projection.sequence, projection.event_kind, projection.input_kind,
       projection.actor_id, projection.idempotency_scope, projection.input_idempotency_key,
       projection.agent_input_id,
       projection.control_type, projection.target_interaction_id, projection.agent_config_id,
       projection.tool_call_id, projection.tool_outcome, projection.model_call_context_id,
       projection.model_stop_reason, projection.context_checkpoint_id,
       projection.summarized_through_event_sequence, projection.checkpoint_summary,
       projection.content_blocks, projection.created_at
FROM agent_event_read_projection projection
WHERE projection.project_id = sqlc.arg(project_id)
  AND projection.agent_id = sqlc.arg(agent_id)
  AND projection.sequence > sqlc.arg(after_sequence)
ORDER BY projection.sequence ASC
LIMIT sqlc.arg(page_limit);

-- name: ListAgentEventsBeforeForRead :many
SELECT projection.id, projection.org_id, projection.project_id, projection.agent_id,
       projection.turn_id, projection.turn_sequence, projection.is_opening_event,
       projection.sequence, projection.event_kind, projection.input_kind,
       projection.actor_id, projection.idempotency_scope, projection.input_idempotency_key,
       projection.agent_input_id,
       projection.control_type, projection.target_interaction_id, projection.agent_config_id,
       projection.tool_call_id, projection.tool_outcome, projection.model_call_context_id,
       projection.model_stop_reason, projection.context_checkpoint_id,
       projection.summarized_through_event_sequence, projection.checkpoint_summary,
       projection.content_blocks, projection.created_at
FROM agent_event_read_projection projection
WHERE projection.project_id = sqlc.arg(project_id)
  AND projection.agent_id = sqlc.arg(agent_id)
  AND (
    sqlc.arg(before_sequence)::bigint <= 0
    OR projection.sequence < sqlc.arg(before_sequence)::bigint
  )
ORDER BY projection.sequence DESC
LIMIT sqlc.arg(page_limit);

-- name: ListTurnEventsForRead :many
SELECT projection.id, projection.org_id, projection.project_id, projection.agent_id,
       projection.turn_id, projection.turn_sequence, projection.is_opening_event,
       projection.sequence, projection.event_kind, projection.input_kind,
       projection.actor_id, projection.idempotency_scope, projection.input_idempotency_key,
       projection.agent_input_id,
       projection.control_type, projection.target_interaction_id, projection.agent_config_id,
       projection.tool_call_id, projection.tool_outcome, projection.model_call_context_id,
       projection.model_stop_reason, projection.context_checkpoint_id,
       projection.summarized_through_event_sequence, projection.checkpoint_summary,
       projection.content_blocks, projection.created_at
FROM agent_event_read_projection projection
WHERE projection.project_id = sqlc.arg(project_id)
  AND projection.agent_id = sqlc.arg(agent_id)
  AND projection.turn_id = sqlc.arg(turn_id)
  AND (
    sqlc.arg(before_sequence)::bigint <= 0
    OR projection.sequence < sqlc.arg(before_sequence)::bigint
  )
ORDER BY projection.sequence DESC
LIMIT sqlc.arg(page_limit);

-- name: ListTurnBoundaryEventsForRead :many
SELECT projection.id, projection.org_id, projection.project_id, projection.agent_id,
       projection.turn_id, projection.turn_sequence, projection.is_opening_event,
       projection.sequence, projection.event_kind, projection.input_kind,
       projection.actor_id, projection.idempotency_scope, projection.input_idempotency_key,
       projection.agent_input_id,
       projection.control_type, projection.target_interaction_id, projection.agent_config_id,
       projection.tool_call_id, projection.tool_outcome, projection.model_call_context_id,
       projection.model_stop_reason, projection.context_checkpoint_id,
       projection.summarized_through_event_sequence, projection.checkpoint_summary,
       projection.content_blocks, projection.created_at
FROM agent_event_read_projection projection
JOIN agent_turns turn
  ON turn.agent_id = projection.agent_id
 AND turn.id = projection.turn_id
WHERE projection.project_id = sqlc.arg(project_id)
  AND projection.agent_id = sqlc.arg(agent_id)
  AND projection.turn_id = ANY(sqlc.arg(turn_ids)::uuid[])
  AND (
    projection.is_opening_event
    OR projection.id = turn.latest_event_id
    OR projection.id = turn.latest_semantic_event_id
  )
ORDER BY projection.turn_sequence DESC, projection.sequence ASC;

-- name: InsertAgentTurn :one
WITH inserted AS (
  INSERT INTO agent_turns(id, agent_id, turn_sequence, latest_event_id, latest_semantic_event_id)
  SELECT sqlc.arg(id), sqlc.arg(agent_id),
         sqlc.arg(turn_sequence), event.id, sqlc.arg(latest_semantic_event_id)
  FROM agent_events event
  JOIN agents agent ON agent.id = event.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND event.agent_id = sqlc.arg(agent_id)
    AND event.turn_id = sqlc.arg(id)
    AND event.id = sqlc.arg(latest_event_id)
  RETURNING id, agent_id, turn_sequence, latest_event_id, latest_semantic_event_id
)
SELECT inserted.id, agent.project_id, inserted.agent_id, inserted.turn_sequence,
       inserted.latest_event_id, inserted.latest_semantic_event_id
FROM inserted
JOIN agents agent ON agent.id = inserted.agent_id;

-- name: UpdateAgentTurnLatestEvent :execrows
UPDATE agent_turns
SET latest_event_id = sqlc.arg(latest_event_id),
    latest_semantic_event_id = coalesce(sqlc.narg(latest_semantic_event_id), latest_semantic_event_id)
FROM agent_events event
WHERE agent_turns.agent_id = sqlc.arg(agent_id)
  AND agent_turns.id = sqlc.arg(id)
  AND event.agent_id = agent_turns.agent_id
  AND event.turn_id = agent_turns.id
  AND event.id = sqlc.arg(latest_event_id)
  AND EXISTS (
    SELECT 1
    FROM agents agent
    WHERE agent.id = agent_turns.agent_id
      AND agent.project_id = sqlc.arg(project_id)
  );

-- name: CurrentContinuableAgentTurn :one
WITH latest_turn AS MATERIALIZED (
  SELECT turn.id, agent.project_id, turn.agent_id, turn.turn_sequence, turn.latest_event_id,
         turn.latest_semantic_event_id
  FROM agent_turns turn
  JOIN agents agent ON agent.id = turn.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND turn.agent_id = sqlc.arg(agent_id)
  ORDER BY turn.turn_sequence DESC
  LIMIT 1
)
SELECT turn.id, turn.project_id, turn.agent_id, turn.turn_sequence, turn.latest_event_id,
       turn.latest_semantic_event_id
FROM latest_turn turn
WHERE EXISTS (
    SELECT 1
    FROM agent_continuable_model_contexts(turn.project_id, turn.agent_id)
      AS context(turn_id, model_call_context_id, input_event_sequence, has_later_semantic_event)
    WHERE context.turn_id = turn.id
  )
  OR agent_has_incomplete_tool_batch(turn.project_id, turn.agent_id)
  OR EXISTS (
    SELECT 1
    FROM agent_next_model_work(turn.project_id, turn.agent_id) frontier
    WHERE frontier.turn_id = turn.id
  );

-- name: ListContextEvents :many
SELECT event.id,
       event.agent_input_id,
       event.sequence,
       event.created_at,
       event.event_kind,
       event.model_output_id,
       output.model_call_context_id,
       revision.model_provider_config_id,
       coalesce(revision.provider_model_slug, '') AS requested_provider_model_slug,
       coalesce(context.api_format, '') AS api_format,
       coalesce(context.api_variant, '') AS api_variant,
       output.provider_replay,
       CASE
         WHEN event.event_kind = 'agent_input' AND input.input_kind = 'config_change' AND event.sequence > 1 THEN
           jsonb_build_array(jsonb_build_object('type', 'text', 'text', 'Agent configuration changed. The current system prompt, model, and tool policy are reflected in this model call.'))
         ELSE
           coalesce(jsonb_agg(
             CASE
               WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', coalesce(block.text_content, ''))
               WHEN block.block_kind = 'artifact' THEN jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id)
               WHEN block.block_kind = 'reasoning' THEN jsonb_build_object('type', 'reasoning', 'text', coalesce(block.text_content, ''))
               WHEN block.block_kind = 'tool_call' THEN jsonb_build_object('type', 'tool_call', 'tool_call_id', block.tool_call_id)
               WHEN block.block_kind = 'error' THEN jsonb_build_object('type', 'error', 'text', coalesce(block.text_content, ''))
               ELSE NULL
             END
             ORDER BY block.ordinal
           ) FILTER (
             WHERE block.id IS NOT NULL
               AND block.block_kind IN ('text', 'artifact', 'reasoning', 'tool_call', 'error')
           ), '[]'::jsonb)::jsonb
       END AS content_parts
FROM agent_events event
JOIN agents scoped_agent
  ON scoped_agent.id = event.agent_id
LEFT JOIN agent_inputs input
  ON input.agent_id = event.agent_id
 AND input.id = event.agent_input_id
LEFT JOIN model_outputs output
  ON output.agent_id = event.agent_id
 AND output.id = event.model_output_id
LEFT JOIN model_call_contexts context
  ON context.agent_id = output.agent_id
 AND context.id = output.model_call_context_id
LEFT JOIN configured_model_revisions revision
  ON revision.org_id = context.org_id
 AND revision.id = context.configured_model_revision_id
LEFT JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND (
   block.owner_agent_input_id = event.agent_input_id
   OR block.owner_model_output_id = event.model_output_id
 )
 AND block.block_kind IN ('text', 'artifact', 'reasoning', 'tool_call', 'error')
WHERE scoped_agent.project_id = sqlc.arg(project_id)
  AND event.agent_id = sqlc.arg(agent_id)
  AND event.sequence > sqlc.arg(after_sequence)
  AND event.sequence <= sqlc.arg(watermark)
  AND (
    (event.event_kind = 'agent_input' AND input.input_kind = 'config_change' AND event.sequence > 1)
    OR event.event_kind = 'model_output'
    OR (
      block.id IS NOT NULL
      AND event.event_kind = 'agent_input'
      AND input.input_kind = 'content'
    )
  )
GROUP BY event.id, event.sequence, event.created_at, event.event_kind,
  event.model_output_id, output.model_call_context_id, revision.model_provider_config_id,
  revision.provider_model_slug, context.api_format, context.api_variant,
  output.provider_replay, input.input_kind
ORDER BY event.sequence ASC
LIMIT sqlc.arg(page_limit);

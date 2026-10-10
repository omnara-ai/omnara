-- name: GetToolCall :one
WITH selected_call AS MATERIALIZED (
  SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id, tc.source_event_id, tc.source_event_sequence,
         tc.model_call_context_id, tc.model_output_id, tc.provider_call_id, tc.name, tc.input,
         tc.type, tc.state, tc.runtime_lock_id, tc.created_at
  FROM tool_call_read_projection tc
  WHERE tc.project_id = sqlc.arg(project_id)
    AND tc.agent_id = sqlc.arg(agent_id)
    AND tc.id = sqlc.arg(id)
)
SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id,
  tc.source_event_id, tc.model_call_context_id, tc.provider_call_id,
  tc.name, tc.input, tc.type,
  tc.state, coalesce(result.outcome, '') AS outcome,
  tc.runtime_lock_id,
  coalesce(result_blocks.content_parts, '[]'::jsonb)::jsonb AS result_content_parts,
  tc.created_at,
  result.completed_at
FROM selected_call tc
LEFT JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    CASE
      WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
      WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
      WHEN block.block_kind = 'artifact' THEN
        jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id::text) ||
        CASE
          WHEN block.exclude_from_model_context THEN jsonb_build_object('exclude_from_model_context', true)
          ELSE '{}'::jsonb
        END
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact')), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  WHERE block.agent_id = result.agent_id
    AND block.owner_tool_call_result_id = result.id
) result_blocks ON true;

-- name: GetToolCallByProviderCall :one
WITH selected_call AS MATERIALIZED (
  SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id, tc.source_event_id, tc.source_event_sequence,
         tc.model_call_context_id, tc.model_output_id, tc.provider_call_id, tc.name, tc.input,
         tc.type, tc.state, tc.runtime_lock_id, tc.created_at
  FROM tool_call_read_projection tc
  WHERE tc.project_id = sqlc.arg(project_id)
    AND tc.agent_id = sqlc.arg(agent_id)
    AND tc.model_call_context_id = sqlc.arg(model_call_context_id)
    AND tc.provider_call_id = sqlc.arg(provider_call_id)
)
SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id,
  tc.source_event_id, tc.model_call_context_id, tc.provider_call_id,
  tc.name, tc.input, tc.type,
  tc.state, coalesce(result.outcome, '') AS outcome,
  tc.runtime_lock_id,
  coalesce(result_blocks.content_parts, '[]'::jsonb)::jsonb AS result_content_parts,
  tc.created_at,
  result.completed_at
FROM selected_call tc
LEFT JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    CASE
      WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
      WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
      WHEN block.block_kind = 'artifact' THEN
        jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id::text) ||
        CASE
          WHEN block.exclude_from_model_context THEN jsonb_build_object('exclude_from_model_context', true)
          ELSE '{}'::jsonb
        END
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact')), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  WHERE block.agent_id = result.agent_id
    AND block.owner_tool_call_result_id = result.id
) result_blocks ON true;

-- name: ListToolCallsForAgent :many
SELECT call.id, call.project_id, call.agent_id,
  call.turn_id, call.source_event_id,
  call.model_call_context_id, call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  coalesce(result.outcome, '') AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at, result.completed_at
FROM tool_call_read_projection call
LEFT JOIN tool_call_results result ON result.agent_id = call.agent_id
  AND result.tool_call_id = call.id
WHERE call.project_id = sqlc.arg(project_id)
  AND call.agent_id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND (sqlc.arg(state)::text = '' OR call.state = sqlc.arg(state))
  AND (sqlc.arg(type)::text = '' OR call.type = sqlc.arg(type))
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (call.created_at, call.id) > (
      sqlc.narg(cursor_created_at)::timestamptz,
      sqlc.narg(cursor_id)::uuid
    )
  )
ORDER BY call.created_at ASC, call.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: NextRunnableToolCallForModelOutput :one
SELECT call.id, call.project_id, call.agent_id,
  call.turn_id, call.source_event_id,
  call.model_call_context_id,
  call.provider_call_id, call.name, call.input,
  call.type, call.state,
  ''::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at, NULL::timestamptz AS completed_at
FROM tool_call_read_projection call
LEFT JOIN content_blocks call_block ON call_block.agent_id = call.agent_id
  AND call_block.tool_call_id = call.id
  AND call_block.block_kind = 'tool_call'
WHERE call.project_id = sqlc.arg(project_id)
  AND call.agent_id = sqlc.arg(agent_id)
  AND call.model_output_id = sqlc.arg(model_output_id)
  AND (
    COALESCE(cardinality(sqlc.arg(excluded_tool_call_ids)::uuid[]), 0) = 0
    OR NOT (call.id = ANY(sqlc.arg(excluded_tool_call_ids)::uuid[]))
  )
  AND (
    call.state = 'awaiting_authorization'
    OR (call.state = 'ready' AND call.type IN ('built_in', 'mcp'))
  )
ORDER BY coalesce(call_block.ordinal, 2147483647), call.created_at, call.id
LIMIT 1;

-- name: ListCompletedToolCallsAtWatermark :many
WITH selected_calls AS MATERIALIZED (
  SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id, tc.source_event_id, tc.source_event_sequence,
         tc.model_call_context_id, tc.model_output_id, tc.provider_call_id, tc.name, tc.input,
         tc.type, tc.state, tc.runtime_lock_id, tc.created_at
  FROM tool_call_read_projection tc
  WHERE tc.project_id = sqlc.arg(project_id)
    AND tc.agent_id = sqlc.arg(agent_id)
    AND tc.state = 'completed'
    AND tc.source_event_sequence > sqlc.arg(after_event_sequence)
    AND tc.source_event_sequence <= sqlc.arg(max_event_sequence)
)
SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id,
  tc.source_event_id, tc.source_event_sequence,
  tc.model_call_context_id, result.id AS tool_call_result_id,
  tool_result_event.id AS tool_result_event_id,
  tool_result_event.sequence AS tool_result_event_sequence,
  tc.provider_call_id, tc.name, tc.input, tc.type, tc.state, result.outcome,
  tc.runtime_lock_id,
  coalesce(result_blocks.content_parts, '[]'::jsonb)::jsonb AS result_content_parts,
  tc.created_at, result.completed_at
FROM selected_calls tc
JOIN content_blocks call_block
  ON call_block.agent_id = tc.agent_id
 AND call_block.tool_call_id = tc.id
 AND call_block.block_kind = 'tool_call'
JOIN tool_call_results result
  ON result.agent_id = tc.agent_id
 AND result.tool_call_id = tc.id
JOIN agent_events tool_result_event
  ON tool_result_event.agent_id = result.agent_id
 AND tool_result_event.tool_call_result_id = result.id
 AND tool_result_event.event_kind = 'tool_result'
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    CASE
      WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
      WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
      WHEN block.block_kind = 'artifact' THEN
        jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id::text)
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (
    WHERE block.id IS NOT NULL
      AND block.block_kind IN ('text', 'structured_data', 'artifact')
      AND NOT block.exclude_from_model_context
  ), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  WHERE block.agent_id = result.agent_id
    AND block.owner_tool_call_result_id = result.id
) result_blocks ON true
WHERE tool_result_event.sequence > sqlc.arg(after_event_sequence)
  AND tool_result_event.sequence <= sqlc.arg(max_event_sequence)
ORDER BY tc.source_event_sequence, call_block.ordinal, tc.created_at, tc.id;

-- name: GetToolCall :one
WITH selected_call AS MATERIALIZED (
  SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id, tc.source_event_id, tc.source_event_sequence,
         tc.model_call_context_id, tc.model_output_id, tc.provider_call_id, tc.name, tc.input,
         tc.type, tc.state, tc.runtime_lock_id, tc.created_at, tc.caller, tc.parent_tool_call_id
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
  result.completed_at,
  tc.caller,
  tc.parent_tool_call_id
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

-- name: GetToolCallSummary :one
SELECT call.id, call.agent_id, source_event.turn_id, call.provider_call_id,
  call.name, call.input, call.type, call.state,
  coalesce(result.outcome, '') AS outcome, call.created_at, result.completed_at
FROM tool_calls call
JOIN agent_events source_event ON source_event.agent_id = call.agent_id
  AND source_event.model_output_id = call.model_output_id
  AND source_event.event_kind = 'model_output'
LEFT JOIN tool_call_results result ON result.agent_id = call.agent_id
  AND result.tool_call_id = call.id
WHERE call.project_id = sqlc.arg(project_id)
  AND call.agent_id = sqlc.arg(agent_id)
  AND call.id = sqlc.arg(id);

-- name: GetToolCallStatus :one
SELECT type, state
FROM tool_calls
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

-- name: GetCustomToolCallInput :one
SELECT tool_call.name, tool_call.input
FROM tool_calls tool_call
WHERE tool_call.project_id = sqlc.arg(project_id)
  AND tool_call.agent_id = sqlc.arg(agent_id)
  AND tool_call.id = sqlc.arg(id)
  AND tool_call.type = 'custom';

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
    AND tc.caller = 'model'
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
SELECT call.id, call.agent_id, call.turn_id, call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  coalesce(result.outcome, '') AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at, result.completed_at,
  call.caller, call.parent_tool_call_id
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

-- ListToolCallsForParent lists the program calls a run_command call's process
-- made; it seeks on tool_calls_program_key_idx. Rows match ListToolCallsForAgent.
-- name: ListToolCallsForParent :many
SELECT call.id, call.project_id, call.agent_id,
  call.turn_id, call.source_event_id,
  call.model_call_context_id, call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  coalesce(result.outcome, '') AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at, result.completed_at,
  call.caller, call.parent_tool_call_id
FROM tool_call_read_projection call
LEFT JOIN tool_call_results result ON result.agent_id = call.agent_id
  AND result.tool_call_id = call.id
WHERE call.project_id = sqlc.arg(project_id)
  AND call.agent_id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND (sqlc.arg(state)::text = '' OR call.state = sqlc.arg(state))
  AND (sqlc.arg(type)::text = '' OR call.type = sqlc.arg(type))
  AND call.caller = 'program'
  AND call.parent_tool_call_id = sqlc.arg(parent_tool_call_id)::uuid
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (call.created_at, call.id) > (
      sqlc.narg(cursor_created_at)::timestamptz,
      sqlc.narg(cursor_id)::uuid
    )
  )
ORDER BY call.created_at ASC, call.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListUnfinishedToolCallsForProject :many
WITH page AS (
  SELECT tool_call.agent_id, tool_call.id
  FROM tool_calls tool_call
  WHERE tool_call.project_id = sqlc.arg(project_id)
    AND tool_call.state <> 'completed'
    AND (sqlc.arg(state)::text = '' OR tool_call.state = sqlc.arg(state))
    AND (sqlc.arg(type)::text = '' OR tool_call.type = sqlc.arg(type))
    AND (sqlc.narg(names)::text[] IS NULL OR tool_call.name = ANY(sqlc.narg(names)::text[]))
    AND (
      SELECT agent.state = 'active'
        AND (sqlc.narg(agent_profile_id)::uuid IS NULL OR agent.agent_profile_id = sqlc.narg(agent_profile_id)::uuid)
      FROM agents agent
      WHERE agent.id = tool_call.agent_id
    )
    -- Keep the cursor indexable when PostgreSQL uses a generic prepared plan.
    AND (tool_call.created_at, tool_call.id) > (
      coalesce(sqlc.narg(cursor_created_at)::timestamptz, '-infinity'::timestamptz),
      coalesce(sqlc.narg(cursor_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
    )
  ORDER BY tool_call.created_at ASC, tool_call.id ASC
  LIMIT sqlc.arg(row_limit)::bigint
)
SELECT call.id, call.agent_id, call.turn_id, call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  ''::text AS outcome,
  call.created_at, NULL::timestamptz AS completed_at
FROM page
JOIN tool_call_read_projection call ON call.agent_id = page.agent_id
  AND call.id = page.id
ORDER BY call.created_at ASC, call.id ASC;

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
  AND call.model_output_id = sqlc.arg(model_output_id)::uuid
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
    AND tc.caller = 'model'
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
 AND NOT call_block.exclude_from_model_context
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

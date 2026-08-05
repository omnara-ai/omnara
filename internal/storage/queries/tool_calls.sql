-- name: InsertToolCall :one
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
source_event AS MATERIALIZED (
  SELECT event.id, event.turn_id, event.model_output_id, model_output.created_at
  FROM agent_events event
  JOIN agents agent ON agent.id = event.agent_id
  JOIN model_outputs model_output ON model_output.agent_id = event.agent_id
    AND model_output.id = event.model_output_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND event.agent_id = sqlc.arg(agent_id)
    AND event.id = sqlc.arg(source_event_id)
    AND event.event_kind = 'model_output'
    AND event.model_output_id IS NOT NULL
    AND model_output.model_call_context_id = sqlc.arg(model_call_context_id)
),
candidate AS MATERIALIZED (
  SELECT coalesce(sqlc.narg(tool_call_id)::uuid, uuidv7()) AS id,
         mcc.project_id,
         mcc.agent_id,
         mcc.id AS model_call_context_id,
         source_event.id AS source_event_id,
         source_event.turn_id,
         source_event.model_output_id,
         sqlc.arg(provider_call_id)::text AS provider_call_id,
         sqlc.arg(name)::text AS name,
         sqlc.arg(input)::jsonb AS input,
         sqlc.arg(type)::text AS type,
         source_event.created_at
  FROM model_call_contexts mcc
  JOIN live_runtime runtime_lock ON runtime_lock.project_id = mcc.project_id
    AND runtime_lock.agent_id = mcc.agent_id
    AND runtime_lock.id = mcc.runtime_lock_id
  JOIN source_event ON true
  WHERE mcc.project_id = sqlc.arg(project_id)
    AND mcc.agent_id = sqlc.arg(agent_id)
    AND mcc.id = sqlc.arg(model_call_context_id)
    AND mcc.runtime_lock_id = sqlc.arg(runtime_lock_id)
),
inserted_call AS (
  INSERT INTO tool_calls(
    id, agent_id, model_output_id,
    provider_call_id, name, input,
    type, state, created_at
  )
  SELECT id, agent_id, model_output_id,
         provider_call_id, name, input,
         type, 'awaiting_authorization', created_at
  FROM candidate
  RETURNING id, agent_id,
            model_output_id, provider_call_id, name,
            input, type, state,
            runtime_lock_id, created_at
)
SELECT call.id, candidate.project_id, call.agent_id,
  candidate.turn_id, candidate.source_event_id,
  candidate.model_call_context_id, call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  ''::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at, NULL::timestamptz AS completed_at
FROM inserted_call call
JOIN candidate ON candidate.id = call.id;

-- name: GetToolCall :one
SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id,
  tc.source_event_id, tc.model_call_context_id, tc.provider_call_id,
  tc.name, tc.input, tc.type,
  tc.state, coalesce(result.outcome, '') AS outcome,
  tc.runtime_lock_id,
  coalesce(result_blocks.content_parts, '[]'::jsonb)::jsonb AS result_content_parts,
  tc.created_at,
  result.completed_at
FROM tool_call_read_projection tc
LEFT JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    CASE
      WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
      WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
      WHEN block.block_kind = 'artifact' THEN jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id::text)
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact')), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  WHERE block.agent_id = result.agent_id
    AND block.owner_tool_call_result_id = result.id
) result_blocks ON true
WHERE tc.project_id = sqlc.arg(project_id) AND tc.agent_id = sqlc.arg(agent_id) AND tc.id = sqlc.arg(id);

-- name: GetToolCallByProviderCall :one
SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id,
  tc.source_event_id, tc.model_call_context_id, tc.provider_call_id,
  tc.name, tc.input, tc.type,
  tc.state, coalesce(result.outcome, '') AS outcome,
  tc.runtime_lock_id,
  coalesce(result_blocks.content_parts, '[]'::jsonb)::jsonb AS result_content_parts,
  tc.created_at,
  result.completed_at
FROM tool_call_read_projection tc
LEFT JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    CASE
      WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
      WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
      WHEN block.block_kind = 'artifact' THEN jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id::text)
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact')), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  WHERE block.agent_id = result.agent_id
    AND block.owner_tool_call_result_id = result.id
) result_blocks ON true
WHERE tc.project_id = sqlc.arg(project_id) AND tc.agent_id = sqlc.arg(agent_id) AND tc.model_call_context_id = sqlc.arg(model_call_context_id) AND tc.provider_call_id = sqlc.arg(provider_call_id);

-- name: ListToolCallsForModelContext :many
SELECT tc.id, tc.project_id, tc.agent_id,
  tc.turn_id, tc.source_event_id, tc.model_call_context_id,
  tc.provider_call_id, tc.name, tc.input, tc.type, tc.state,
  coalesce(result.outcome, '') AS outcome, tc.runtime_lock_id,
  coalesce(result_blocks.content_parts, '[]'::jsonb)::jsonb AS result_content_parts,
  tc.created_at, result.completed_at
FROM tool_call_read_projection tc
LEFT JOIN tool_call_results result
  ON result.agent_id = tc.agent_id
 AND result.tool_call_id = tc.id
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
  ), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  WHERE block.agent_id = result.agent_id
    AND block.owner_tool_call_result_id = result.id
) result_blocks ON true
WHERE tc.project_id = sqlc.arg(project_id)
  AND tc.agent_id = sqlc.arg(agent_id)
  AND tc.model_call_context_id = sqlc.arg(model_call_context_id)
ORDER BY tc.created_at, tc.id;

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
  AND call.agent_id = sqlc.arg(agent_id)
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

-- name: StartToolCall :execrows
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
UPDATE tool_calls call
SET state = CASE
      WHEN sqlc.arg(retain_runtime_ownership)::boolean THEN 'running'
      ELSE 'waiting'
    END,
    runtime_lock_id = CASE
      WHEN sqlc.arg(retain_runtime_ownership)::boolean THEN runtime_lock.id
      ELSE NULL
    END
FROM live_runtime runtime_lock
WHERE call.agent_id = sqlc.arg(agent_id)
  AND call.id = sqlc.arg(id)
  AND call.state = 'ready'
  AND call.runtime_lock_id IS NULL
  AND call.type IN ('built_in', 'mcp')
  AND (
    sqlc.arg(retain_runtime_ownership)::boolean
    OR call.type = 'built_in'
  )
  AND runtime_lock.agent_id = call.agent_id
  AND runtime_lock.id = sqlc.arg(runtime_lock_id);

-- name: GetToolCallDispatchState :one
SELECT state, runtime_lock_id
FROM tool_call_read_projection
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

-- name: ReleaseToolCallRuntimeOwnership :execrows
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
UPDATE tool_calls call
SET state = 'waiting',
    runtime_lock_id = NULL
FROM live_runtime runtime_lock
WHERE call.agent_id = runtime_lock.agent_id
  AND call.id = sqlc.arg(id)
  AND call.state = 'running'
  AND call.runtime_lock_id = runtime_lock.id
  AND EXISTS (
    SELECT 1
    FROM agent_interactions interaction
    WHERE interaction.agent_id = call.agent_id
      AND interaction.tool_call_id = call.id
      AND interaction.interaction_kind = 'question'
      AND interaction.state = 'open'
  );

-- name: RequeueRuntimeToolCall :execrows
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
UPDATE tool_calls call
SET state = 'ready',
    runtime_lock_id = NULL
FROM live_runtime runtime_lock
WHERE call.agent_id = runtime_lock.agent_id
  AND call.id = sqlc.arg(id)
  AND call.state = 'running'
  AND call.runtime_lock_id = runtime_lock.id;

-- name: MarkToolCallAwaitingPermission :one
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
UPDATE tool_calls call
SET state = 'awaiting_permission'
FROM live_runtime runtime_lock
WHERE call.agent_id = runtime_lock.agent_id
  AND call.id = sqlc.arg(id)
  AND call.state = 'awaiting_authorization'
  AND runtime_lock.id = sqlc.arg(runtime_lock_id)
RETURNING call.id;

-- name: MarkToolCallReady :one
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
UPDATE tool_calls call
SET state = 'ready'
FROM live_runtime runtime_lock
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = runtime_lock.agent_id
  AND call.id = sqlc.arg(id)
  AND call.state = 'awaiting_authorization'
  AND runtime_lock.id = sqlc.arg(runtime_lock_id)
  AND projection.project_id = runtime_lock.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  ''::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at, NULL::timestamptz AS completed_at;

-- name: MarkToolCallReadyFromInteraction :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
),
permission_interaction AS MATERIALIZED (
  SELECT agent.project_id, interaction.agent_id, interaction.tool_call_id
  FROM agent_interactions interaction
  JOIN locked_agent agent ON agent.id = interaction.agent_id
  WHERE interaction.agent_id = sqlc.arg(agent_id)
    AND interaction.id = sqlc.arg(interaction_id)
    AND interaction.interaction_kind = 'permission'
    AND interaction.state = 'resolved'
)
UPDATE tool_calls call
SET state = 'ready'
FROM permission_interaction interaction
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = interaction.agent_id
  AND call.id = interaction.tool_call_id
  AND call.state = 'awaiting_permission'
  AND projection.project_id = interaction.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  ''::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at, NULL::timestamptz AS completed_at;

-- name: CompleteToolCallFromPermissionInteraction :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
),
permission_interaction AS MATERIALIZED (
  SELECT agent.project_id, interaction.agent_id, interaction.tool_call_id
  FROM agent_interactions interaction
  JOIN locked_agent agent ON agent.id = interaction.agent_id
  WHERE interaction.agent_id = sqlc.arg(agent_id)
    AND interaction.id = sqlc.arg(interaction_id)
    AND interaction.interaction_kind = 'permission'
    AND interaction.state IN ('resolved', 'canceled')
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM permission_interaction interaction
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = interaction.agent_id
  AND call.id = interaction.tool_call_id
  AND call.state = 'awaiting_permission'
  AND projection.project_id = interaction.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteToolCall :one
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM live_runtime runtime_lock
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = sqlc.arg(agent_id)
  AND call.id = sqlc.arg(id)
  AND call.state <> 'completed'
  AND (
    (call.type IN ('built_in', 'mcp') AND call.state = 'ready')
    OR (
      call.state IN ('awaiting_authorization', 'awaiting_permission')
      AND sqlc.arg(outcome)::text IN ('failed', 'denied', 'canceled')
    )
  )
  AND runtime_lock.agent_id = call.agent_id
  AND runtime_lock.id = sqlc.arg(runtime_lock_id)
  AND projection.project_id = runtime_lock.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteRuntimeToolCall :one
WITH live_runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agents agent ON agent.id = runtime_lock.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND runtime_lock.agent_id = sqlc.arg(agent_id)
    AND runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM live_runtime runtime_lock
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = runtime_lock.agent_id
  AND call.id = sqlc.arg(id)
  AND call.state = 'running'
  AND call.runtime_lock_id = runtime_lock.id
  AND call.type IN ('built_in', 'mcp')
  AND projection.project_id = runtime_lock.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteMachineUnreachableToolCall :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.id = sqlc.arg(id)
  AND call.state = 'waiting'
  AND call.type = 'built_in'
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteCustomToolCall :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  JOIN tool_calls tool_call ON tool_call.agent_id = agent.id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND tool_call.agent_id = sqlc.arg(agent_id)
    AND tool_call.id = sqlc.arg(id)
  FOR UPDATE OF agent
)
UPDATE tool_calls call
SET state = 'completed'
FROM locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.id = sqlc.arg(id)
  AND call.state = 'ready'
  AND call.runtime_lock_id IS NULL
  AND call.type = 'custom'
  AND sqlc.arg(outcome)::text IN ('succeeded', 'failed')
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteToolCallFromProcess :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM processes tp
CROSS JOIN locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.id = sqlc.arg(id)
  AND call.state = 'waiting'
  AND call.type = 'built_in'
  AND tp.project_id = agent.project_id
  AND tp.agent_id = call.agent_id
  AND tp.id = sqlc.arg(process_id)
  AND tp.tool_call_id = call.id
  AND tp.state IN ('exited', 'failed', 'killed', 'unknown')
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteToolCallFromQuestionInteraction :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.state IN ('running', 'waiting')
  AND call.type = 'built_in'
  AND EXISTS (
    SELECT 1
    FROM agent_interactions question
    WHERE question.agent_id = call.agent_id
      AND question.tool_call_id = call.id
      AND question.id = sqlc.arg(interaction_id)
      AND question.interaction_kind = 'question'
      AND question.state IN ('resolved', 'canceled')
  )
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteToolCallFromStartedProcess :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM processes process
CROSS JOIN locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.id = sqlc.arg(id)
  AND call.state = 'waiting'
  AND call.type = 'built_in'
  AND process.project_id = agent.project_id
  AND process.agent_id = call.agent_id
  AND process.id = sqlc.arg(process_id)
  AND process.tool_call_id = call.id
  AND process.state = 'running'
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: CompleteToolCallFromProcessAction :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM process_actions action
CROSS JOIN locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.id = action.tool_call_id
  AND call.id = sqlc.arg(tool_call_id)
  AND call.state = 'waiting'
  AND call.type = 'built_in'
  AND action.project_id = agent.project_id
  AND action.agent_id = call.agent_id
  AND action.process_id = sqlc.arg(process_id)
  AND action.id = sqlc.arg(process_action_id)
  AND action.state IN ('applied', 'failed', 'unknown')
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

-- name: ListCompletedToolCallsAtWatermark :many
SELECT tc.id, tc.project_id, tc.agent_id, tc.turn_id,
  tc.source_event_id, tc.source_event_sequence,
  tc.model_call_context_id, result.id AS tool_call_result_id,
  tool_result_event.id AS tool_result_event_id,
  tool_result_event.sequence AS tool_result_event_sequence,
  tc.provider_call_id, tc.name, tc.input, tc.type, tc.state, result.outcome,
  tc.runtime_lock_id,
  coalesce(result_blocks.content_parts, '[]'::jsonb)::jsonb AS result_content_parts,
  tc.created_at, result.completed_at
FROM tool_call_read_projection tc
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
  ), '[]'::jsonb) AS content_parts
  FROM content_blocks block
  WHERE block.agent_id = result.agent_id
    AND block.owner_tool_call_result_id = result.id
) result_blocks ON true
WHERE tc.project_id = sqlc.arg(project_id)
  AND tc.agent_id = sqlc.arg(agent_id)
  AND tc.state = 'completed'
  AND tc.source_event_sequence > sqlc.arg(after_event_sequence)
  AND tool_result_event.sequence > sqlc.arg(after_event_sequence)
  AND tc.source_event_sequence <= sqlc.arg(max_event_sequence)
  AND tool_result_event.sequence <= sqlc.arg(max_event_sequence)
ORDER BY tc.source_event_sequence, call_block.ordinal, tc.created_at, tc.id;

-- name: CancelNonTerminalToolCallsForAgent :many
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.state <> 'completed'
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
  AND projection.turn_id = sqlc.arg(turn_id)
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  'canceled'::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

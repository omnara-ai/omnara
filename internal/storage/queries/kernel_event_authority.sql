-- name: InsertModelOutputAuthority :one
WITH inserted AS (
  INSERT INTO model_outputs(
    agent_id, model_call_context_id,
    served_provider_model_slug, stop_reason, provider_replay, created_at
  )
  SELECT agent.id, context.id,
    sqlc.arg(served_provider_model_slug), sqlc.arg(stop_reason),
    sqlc.narg(provider_replay)::jsonb, statement_timestamp()
  FROM agents agent
  JOIN model_call_contexts context ON context.project_id = agent.project_id
    AND context.agent_id = agent.id
    AND context.id = sqlc.arg(model_call_context_id)::uuid
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  ON CONFLICT (agent_id, model_call_context_id) DO NOTHING
  RETURNING id, agent_id, model_call_context_id,
    served_provider_model_slug, stop_reason, provider_replay, created_at
)
SELECT inserted.id, agent.project_id, inserted.agent_id,
  (
    SELECT context_turn.turn_id
    FROM model_call_context_turns context_turn
    WHERE context_turn.project_id = agent.project_id
      AND context_turn.agent_id = inserted.agent_id
      AND context_turn.model_call_context_id = inserted.model_call_context_id
  ) AS turn_id,
  inserted.model_call_context_id,
  inserted.served_provider_model_slug, inserted.stop_reason,
  context.provider_response_id,
  inserted.provider_replay,
  context.input_tokens_total, context.uncached_input_tokens,
  context.cache_read_input_tokens, context.cache_write_input_tokens,
  context.output_tokens_total, context.reasoning_output_tokens,
  inserted.created_at
FROM inserted
JOIN agents agent ON agent.id = inserted.agent_id
JOIN model_call_contexts context ON context.project_id = agent.project_id
  AND context.agent_id = inserted.agent_id
  AND context.id = inserted.model_call_context_id;

-- name: GetModelOutputByModelContext :one
SELECT output.id, agent.project_id, output.agent_id,
  (
    SELECT context_turn.turn_id
    FROM model_call_context_turns context_turn
    WHERE context_turn.project_id = agent.project_id
      AND context_turn.agent_id = output.agent_id
      AND context_turn.model_call_context_id = output.model_call_context_id
  ) AS turn_id,
  output.model_call_context_id,
  output.served_provider_model_slug,
  output.stop_reason, context.provider_response_id,
  output.provider_replay,
  context.input_tokens_total, context.uncached_input_tokens,
  context.cache_read_input_tokens, context.cache_write_input_tokens,
  context.output_tokens_total, context.reasoning_output_tokens,
  output.created_at
FROM model_outputs output
JOIN agents agent ON agent.id = output.agent_id
JOIN model_call_contexts context ON context.project_id = agent.project_id
  AND context.agent_id = output.agent_id
  AND context.id = output.model_call_context_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND output.agent_id = sqlc.arg(agent_id)
  AND output.model_call_context_id = sqlc.arg(model_call_context_id);

-- name: InsertControlAgentInput :one
WITH generated AS (
  SELECT uuidv7() AS id
)
INSERT INTO agent_inputs(
  id, project_id, agent_id, state,
  actor_id, input_kind, delivery_mode, control_type,
  idempotency_scope, input_idempotency_key, queued_at, metadata
)
SELECT generated.id, agent.project_id, agent.id, 'received',
       sqlc.arg(actor_id), 'control', 'immediate',
       sqlc.arg(control_type), sqlc.arg(idempotency_scope), sqlc.arg(input_idempotency_key),
       statement_timestamp(), sqlc.arg(metadata)
FROM agents agent
JOIN generated ON true
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
ON CONFLICT DO NOTHING
RETURNING id, project_id, agent_id,
  state, input_rank, actor_id, input_kind,
  coalesce(idempotency_scope, '') AS idempotency_scope,
  coalesce(input_idempotency_key, '') AS input_idempotency_key,
  queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode,
  coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at,
  coalesce(rejected_reason, '') AS rejected_reason, metadata;

-- name: ResolveControlAgentInput :exec
UPDATE agent_inputs
SET state = 'resolved',
    admitted_event_id = sqlc.arg(event_id),
    admitted_at = statement_timestamp(),
    resolved_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id)
  AND input_kind = 'control'
  AND control_type = sqlc.arg(control_type)
  AND state = 'received';

-- name: InsertConfigChangeAgentInput :one
WITH generated AS (
  SELECT uuidv7() AS id
),
inserted AS (
INSERT INTO agent_inputs(
  id, project_id, agent_id, state,
  actor_id, input_kind, delivery_mode,
  agent_config_id, idempotency_scope, input_idempotency_key, queued_at, metadata
)
SELECT generated.id, agent.project_id, agent.id, 'received',
       sqlc.narg(actor_id)::uuid, 'config_change', 'immediate',
       sqlc.arg(agent_config_id),
       sqlc.arg(idempotency_scope), sqlc.arg(input_idempotency_key),
       statement_timestamp(), sqlc.arg(metadata)
FROM agents agent
JOIN generated ON true
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
ON CONFLICT DO NOTHING
RETURNING id, project_id, agent_id,
  state, input_rank, actor_id, input_kind,
  coalesce(idempotency_scope, '') AS idempotency_scope,
  coalesce(input_idempotency_key, '') AS input_idempotency_key,
  queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode,
  coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at,
  coalesce(rejected_reason, '') AS rejected_reason, metadata
)
SELECT id, project_id, agent_id,
  state, input_rank, actor_id, input_kind,
  idempotency_scope,
  input_idempotency_key,
  queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode,
  control_type, target_interaction_id, agent_config_id, resolved_at,
  rejected_reason, metadata
FROM inserted
UNION ALL
SELECT id, project_id, agent_id,
  state, input_rank, actor_id, input_kind,
  coalesce(idempotency_scope, '') AS idempotency_scope,
  coalesce(input_idempotency_key, '') AS input_idempotency_key,
  queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode,
  coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at,
  coalesce(rejected_reason, '') AS rejected_reason, metadata
FROM agent_inputs
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND idempotency_scope = sqlc.arg(idempotency_scope)
  AND input_idempotency_key = sqlc.arg(input_idempotency_key)
LIMIT 1;

-- name: ResolveConfigChangeAgentInput :one
WITH causal_event AS MATERIALIZED (
  SELECT input.agent_config_id, event.created_at
  FROM agent_inputs input
  JOIN agent_events event ON event.agent_id = input.agent_id
    AND event.agent_input_id = input.id
  WHERE input.project_id = sqlc.arg(project_id)
    AND input.agent_id = sqlc.arg(agent_id)
    AND input.id = sqlc.arg(id)
    AND input.input_kind = 'config_change'
    AND input.state = 'received'
    AND event.id = sqlc.arg(event_id)
    AND event.event_kind = 'agent_input'
),
resolved AS (
  UPDATE agent_inputs input
  SET state = 'resolved',
      admitted_event_id = sqlc.arg(event_id),
      admitted_at = causal_event.created_at,
      resolved_at = causal_event.created_at
  FROM causal_event
  WHERE input.project_id = sqlc.arg(project_id)
    AND input.agent_id = sqlc.arg(agent_id)
    AND input.id = sqlc.arg(id)
    AND input.state = 'received'
  RETURNING input.admitted_at, input.resolved_at, input.agent_config_id
),
updated_agent AS (
  UPDATE agents agent
  SET current_config_id = resolved.agent_config_id,
      updated_at = resolved.resolved_at
  FROM resolved
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  RETURNING agent.id
)
SELECT resolved.admitted_at, resolved.resolved_at
FROM resolved
JOIN updated_agent ON true;

-- name: InsertToolCallResultAuthority :one
WITH inserted AS (
  INSERT INTO tool_call_results(
    agent_id, tool_call_id, outcome, completed_at
  )
  SELECT tool_call.agent_id, tool_call.id,
    sqlc.arg(outcome), statement_timestamp()
  FROM tool_call_read_projection tool_call
  WHERE tool_call.project_id = sqlc.arg(project_id)
    AND tool_call.agent_id = sqlc.arg(agent_id)
    AND tool_call.id = sqlc.arg(tool_call_id)
    AND tool_call.state = 'completed'
  ON CONFLICT (agent_id, tool_call_id) DO NOTHING
  RETURNING id, agent_id, tool_call_id, outcome, completed_at
)
SELECT inserted.id, agent.project_id, inserted.agent_id,
       source_event.turn_id, inserted.tool_call_id, inserted.outcome,
       inserted.completed_at
FROM inserted
JOIN agents agent ON agent.id = inserted.agent_id
JOIN tool_calls tool_call ON tool_call.agent_id = inserted.agent_id
  AND tool_call.id = inserted.tool_call_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output';

-- name: GetToolCallResultByToolCall :one
SELECT result.id, agent.project_id, result.agent_id,
  source_event.turn_id AS turn_id,
  result.tool_call_id, result.outcome, result.completed_at
FROM tool_call_results result
JOIN agents agent ON agent.id = result.agent_id
JOIN tool_calls tool_call ON tool_call.agent_id = result.agent_id
  AND tool_call.id = result.tool_call_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output'
WHERE agent.project_id = sqlc.arg(project_id)
  AND result.agent_id = sqlc.arg(agent_id)
  AND result.tool_call_id = sqlc.arg(tool_call_id);

-- name: ToolCallResultHasTypedEvent :one
SELECT EXISTS (
  SELECT 1
  FROM agent_events event
  JOIN agents agent ON agent.id = event.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND event.agent_id = sqlc.arg(agent_id)
    AND event.tool_call_result_id = sqlc.arg(tool_call_result_id)
    AND event.event_kind = 'tool_result'
)::boolean;

-- name: ListToolCallResultContentBlocks :many
SELECT block.id, agent.project_id, block.agent_id, block.owner_kind,
       block.owner_agent_input_id, block.owner_model_output_id,
       block.owner_tool_call_result_id, block.ordinal, block.block_kind,
       coalesce(block.text_content, '') AS text_content, block.structured_data,
       block.artifact_id, block.tool_call_id, block.metadata, block.created_at
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND block.agent_id = sqlc.arg(agent_id)
  AND block.owner_tool_call_result_id = sqlc.arg(tool_call_result_id)
ORDER BY block.ordinal;

-- name: InsertContentBlock :one
WITH content_owner AS (
  SELECT input.queued_at AS created_at
  FROM agent_inputs input
  WHERE input.project_id = sqlc.arg(project_id)
    AND input.agent_id = sqlc.arg(agent_id)
    AND input.id = sqlc.narg(owner_agent_input_id)::uuid

  UNION ALL

  SELECT output.created_at
  FROM model_outputs output
  JOIN agents agent ON agent.id = output.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND output.agent_id = sqlc.arg(agent_id)
    AND output.id = sqlc.narg(owner_model_output_id)::uuid

  UNION ALL

  SELECT result.completed_at
  FROM tool_call_results result
  JOIN agents agent ON agent.id = result.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND result.agent_id = sqlc.arg(agent_id)
    AND result.id = sqlc.narg(owner_tool_call_result_id)::uuid
),
inserted AS (
  INSERT INTO content_blocks(
    agent_id, owner_kind, owner_agent_input_id,
    owner_model_output_id, owner_tool_call_result_id, ordinal, block_kind,
    text_content, structured_data, artifact_id, tool_call_id, metadata, created_at
  )
  SELECT
    sqlc.arg(agent_id), sqlc.arg(owner_kind),
    sqlc.narg(owner_agent_input_id)::uuid, sqlc.narg(owner_model_output_id)::uuid,
    sqlc.narg(owner_tool_call_result_id)::uuid, sqlc.arg(ordinal), sqlc.arg(block_kind),
    sqlc.narg(text_content), sqlc.narg(structured_data)::jsonb, sqlc.narg(artifact_id)::uuid, sqlc.narg(tool_call_id)::uuid,
    sqlc.arg(metadata)::jsonb, content_owner.created_at
  FROM content_owner
  RETURNING id, agent_id, owner_kind, owner_agent_input_id,
            owner_model_output_id, owner_tool_call_result_id, ordinal, block_kind,
            coalesce(text_content, '') AS text_content, structured_data,
            artifact_id, tool_call_id, created_at
)
SELECT inserted.id, agent.project_id, inserted.agent_id, inserted.owner_kind,
       inserted.owner_agent_input_id, inserted.owner_model_output_id,
       inserted.owner_tool_call_result_id, inserted.ordinal, inserted.block_kind,
       inserted.text_content, inserted.structured_data, inserted.artifact_id,
       inserted.tool_call_id, inserted.created_at
FROM inserted
JOIN agents agent ON agent.id = inserted.agent_id;

-- name: ListContentBlocksForModelOutput :many
SELECT block.id, agent.project_id, block.agent_id, block.owner_kind,
       block.owner_agent_input_id, block.owner_model_output_id,
       block.owner_tool_call_result_id, block.ordinal, block.block_kind,
       coalesce(block.text_content, '') AS text_content, block.structured_data,
       block.artifact_id, block.tool_call_id, block.created_at
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND block.agent_id = sqlc.arg(agent_id)
  AND block.owner_model_output_id = sqlc.arg(model_output_id)
ORDER BY block.ordinal ASC, block.id ASC;

-- name: ListContentBlocksForAgentInput :many
SELECT block.id, agent.project_id, block.agent_id, block.owner_kind,
       block.owner_agent_input_id, block.owner_model_output_id,
       block.owner_tool_call_result_id, block.ordinal, block.block_kind,
       coalesce(block.text_content, '') AS text_content, block.structured_data,
       block.artifact_id, block.tool_call_id, block.metadata, block.created_at
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND block.agent_id = sqlc.arg(agent_id)
  AND block.owner_agent_input_id = sqlc.arg(agent_input_id)
ORDER BY block.ordinal ASC, block.id ASC;

-- name: GetToolCallProviderCallID :one
SELECT provider_call_id
FROM tool_call_read_projection
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

-- name: InsertTypedAgentEvent :one
WITH event_time AS (
  SELECT statement_timestamp() AS created_at
  FROM agent_inputs input
  WHERE input.project_id = sqlc.arg(project_id)
    AND input.agent_id = sqlc.arg(agent_id)
    AND input.id = sqlc.narg(agent_input_id)::uuid

  UNION ALL

  SELECT output.created_at
  FROM model_outputs output
  JOIN agents agent ON agent.id = output.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND output.agent_id = sqlc.arg(agent_id)
    AND output.id = sqlc.narg(model_output_id)::uuid

  UNION ALL

  SELECT result.completed_at
  FROM tool_call_results result
  JOIN agents agent ON agent.id = result.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND result.agent_id = sqlc.arg(agent_id)
    AND result.id = sqlc.narg(tool_call_result_id)::uuid

  UNION ALL

  SELECT checkpoint.created_at
  FROM context_checkpoints checkpoint
  JOIN agents agent ON agent.id = checkpoint.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND checkpoint.agent_id = sqlc.arg(agent_id)
    AND checkpoint.id = sqlc.narg(context_checkpoint_id)::uuid
)
INSERT INTO agent_events(
  id, agent_id, turn_id, sequence, event_kind,
  idempotency_key, agent_input_id, model_output_id,
  tool_call_result_id, context_checkpoint_id, is_opening_event, created_at
)
SELECT
  coalesce(sqlc.narg(id)::uuid, uuidv7()),
  sqlc.arg(agent_id), sqlc.arg(turn_id), sqlc.arg(sequence),
  sqlc.arg(event_kind),
  sqlc.narg(idempotency_key),
  sqlc.narg(agent_input_id)::uuid, sqlc.narg(model_output_id)::uuid,
  sqlc.narg(tool_call_result_id)::uuid, sqlc.narg(context_checkpoint_id)::uuid,
  sqlc.arg(is_opening_event), event_time.created_at
FROM event_time
ON CONFLICT (agent_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING id, agent_id, turn_id, is_opening_event, sequence, event_kind, created_at, coalesce(idempotency_key, '') AS idempotency_key, agent_input_id, model_output_id, tool_call_result_id, context_checkpoint_id;

-- name: GetTypedAgentEventByIdempotency :one
SELECT event.id, event.agent_id, event.turn_id, event.is_opening_event,
       event.sequence, event.event_kind, event.created_at,
       coalesce(event.idempotency_key, '') AS idempotency_key,
       event.agent_input_id, event.model_output_id, event.tool_call_result_id,
       event.context_checkpoint_id
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND event.agent_id = sqlc.arg(agent_id)
  AND event.idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: GetTypedAgentEventByModelOutput :one
SELECT event.id, event.agent_id, event.turn_id, event.is_opening_event,
       event.sequence, event.event_kind, event.created_at,
       coalesce(event.idempotency_key, '') AS idempotency_key,
       event.agent_input_id, event.model_output_id, event.tool_call_result_id,
       event.context_checkpoint_id
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND event.agent_id = sqlc.arg(agent_id)
  AND event.model_output_id = sqlc.arg(model_output_id);

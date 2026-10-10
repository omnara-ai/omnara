-- name: GetModelOutputByModelContext :one
SELECT output.id, agent.project_id, output.agent_id,
  context.turn_id,
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

-- name: ListContentBlocksForAgentInputs :many
SELECT block.id, agent.project_id, block.agent_id, block.owner_kind,
       block.owner_agent_input_id, block.owner_model_output_id,
       block.owner_tool_call_result_id, block.ordinal, block.block_kind,
       coalesce(block.text_content, '') AS text_content, block.structured_data,
       block.artifact_id, block.tool_call_id, block.metadata, block.created_at
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND block.agent_id = sqlc.arg(agent_id)
  AND block.owner_agent_input_id = ANY(sqlc.arg(agent_input_ids)::uuid[])
ORDER BY block.owner_agent_input_id ASC, block.ordinal ASC, block.id ASC;

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

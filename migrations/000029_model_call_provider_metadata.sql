-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN provider_metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_provider_metadata_object
    CHECK (jsonb_typeof(provider_metadata) = 'object');

CREATE OR REPLACE VIEW agent_event_read_projection AS
SELECT event.id,
       agent.org_id,
       agent.project_id,
       event.agent_id,
       event.turn_id,
       turn.turn_sequence,
       event.is_opening_event,
       event.sequence,
       event.event_kind,
       input.input_kind,
       input.actor_id,
       input.idempotency_scope,
       input.input_idempotency_key,
       event.agent_input_id,
       input.control_type,
       input.target_interaction_id,
       input.agent_config_id,
       tool_result.tool_call_id,
       tool_result.outcome AS tool_outcome,
       model_output.model_call_context_id,
       model_output.stop_reason AS model_stop_reason,
       event.context_checkpoint_id,
       checkpoint.summarized_through_event_sequence,
       checkpoint.summary AS checkpoint_summary,
       block_projection.content_blocks::jsonb AS content_blocks,
       event.created_at,
       model_call.input_tokens_total,
       model_call.uncached_input_tokens,
       model_call.cache_read_input_tokens,
       model_call.cache_write_input_tokens,
       model_call.output_tokens_total,
       model_call.reasoning_output_tokens,
       model_call.provider_metadata
FROM agent_events event
JOIN agents agent
  ON agent.id = event.agent_id
JOIN agent_turns turn
  ON turn.agent_id = event.agent_id
 AND turn.id = event.turn_id
LEFT JOIN agent_inputs input
  ON input.agent_id = event.agent_id
 AND input.id = event.agent_input_id
LEFT JOIN tool_call_results tool_result
  ON tool_result.agent_id = event.agent_id
 AND tool_result.id = event.tool_call_result_id
LEFT JOIN model_outputs model_output
  ON model_output.agent_id = event.agent_id
 AND model_output.id = event.model_output_id
LEFT JOIN context_checkpoints checkpoint
  ON checkpoint.agent_id = event.agent_id
 AND checkpoint.id = event.context_checkpoint_id
LEFT JOIN model_call_contexts model_call
  ON model_call.agent_id = model_output.agent_id
 AND model_call.id = model_output.model_call_context_id
CROSS JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    (
      CASE
        WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
        WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
        WHEN block.block_kind = 'artifact' THEN
          jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id) ||
          CASE
            WHEN block.exclude_from_model_context THEN jsonb_build_object('exclude_from_model_context', true)
            ELSE '{}'::jsonb
          END
        WHEN block.block_kind = 'reasoning' THEN jsonb_build_object('type', 'reasoning', 'text', block.text_content)
        WHEN block.block_kind = 'error' THEN jsonb_build_object('type', 'error', 'text', block.text_content)
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
    ) || CASE
      WHEN block.metadata = '{}'::jsonb THEN '{}'::jsonb
      ELSE jsonb_build_object('metadata', block.metadata)
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact', 'reasoning', 'tool_call', 'error')), '[]'::jsonb) AS content_blocks
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
) block_projection;

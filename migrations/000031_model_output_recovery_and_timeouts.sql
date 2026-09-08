-- +goose Up

ALTER TABLE configured_model_revisions
    ALTER COLUMN max_output_tokens DROP NOT NULL,
    ADD CONSTRAINT configured_model_revisions_minimum_context
    CHECK (context_window_tokens >= 2),
    ADD CONSTRAINT configured_model_revisions_default_output_within_context
    CHECK (default_max_output_tokens IS NULL OR default_max_output_tokens < context_window_tokens);

-- Outputs recorded without explicit continuation intent never schedule recovery.
ALTER TABLE model_outputs
    ADD COLUMN continue_after_truncation boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT model_outputs_continuation_requires_truncation
    CHECK (NOT continue_after_truncation OR (stop_reason = 'max_tokens' AND provider_replay IS NULL));

CREATE INDEX model_outputs_truncation_continuation_idx
    ON model_outputs (agent_id) WHERE continue_after_truncation;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent_next_model_work(p_project_id uuid, p_agent_id uuid)
RETURNS TABLE (
    work_kind text,
    model_call_context_id uuid,
    model_output_id uuid,
    turn_id uuid,
    input_ids uuid[],
    opening_event_sequence bigint,
    ready_at timestamptz
)
LANGUAGE sql
STABLE
AS $$
WITH unstarted AS (
    SELECT opening.turn_id,
           max(opening.opening_event_sequence)::bigint AS opening_watermark,
           min(event.created_at) AS ready_at
    FROM agent_latest_unstarted_model_call_opening_inputs(p_project_id, p_agent_id)
      AS opening(turn_id, opening_event_sequence)
    JOIN agent_events event ON event.agent_id = p_agent_id
      AND event.sequence = opening.opening_event_sequence
    GROUP BY opening.turn_id
),
continuable AS MATERIALIZED (
    SELECT context.turn_id,
           context.model_call_context_id,
           context.input_event_sequence,
           context.has_later_semantic_event
    FROM agent_continuable_model_contexts(p_project_id, p_agent_id)
      AS context(
        turn_id,
        model_call_context_id,
        input_event_sequence,
        has_later_semantic_event
      )
),
retry AS (
    SELECT context.turn_id,
           context.model_call_context_id,
           context.input_event_sequence AS opening_watermark,
           retry_context.retry_at AS ready_at
    FROM continuable context
    JOIN model_call_contexts retry_context
      ON retry_context.project_id = p_project_id
     AND retry_context.agent_id = p_agent_id
     AND retry_context.id = context.model_call_context_id
     AND retry_context.state = 'failed'
     AND retry_context.recovery_kind = 'retry'
     AND retry_context.retry_at IS NOT NULL
    WHERE NOT context.has_later_semantic_event
    LIMIT 1
),
later_semantic AS (
    SELECT context.turn_id,
           context.input_event_sequence AS opening_watermark,
           semantic_event.created_at AS ready_at
    FROM continuable context
    JOIN agent_turns turn ON turn.agent_id = p_agent_id
      AND turn.id = context.turn_id
    JOIN agent_events semantic_event ON semantic_event.agent_id = turn.agent_id
      AND semantic_event.id = turn.latest_semantic_event_id
    WHERE context.has_later_semantic_event
    LIMIT 1
),
config_change AS (
    SELECT frontier.turn_id,
           frontier.config_event_sequence AS opening_watermark,
           frontier.ready_at
    FROM agent_unconsumed_config_change_frontiers(p_project_id, p_agent_id)
      AS frontier(turn_id, config_event_sequence, ready_at)
    LIMIT 1
),
completed_tools AS (
    SELECT frontier.turn_id,
           frontier.model_call_context_id,
           frontier.model_output_id,
           source_context.input_event_sequence AS opening_watermark,
           frontier.ready_at
    FROM agent_model_result_frontiers(p_project_id, p_agent_id)
      AS frontier(
        turn_id,
        model_call_context_id,
        model_output_id,
        ready_at,
        frontier_order_created_at,
        frontier_order_tool_call_id
      )
    JOIN model_call_contexts source_context
      ON source_context.project_id = p_project_id
     AND source_context.agent_id = p_agent_id
     AND source_context.id = frontier.model_call_context_id
    ORDER BY frontier.ready_at,
             frontier.frontier_order_created_at,
             frontier.frontier_order_tool_call_id
    LIMIT 1
),
truncated_output AS (
    SELECT source_event.turn_id,
           output.model_call_context_id,
           output.id AS model_output_id,
           source_context.input_event_sequence AS opening_watermark,
           source_event.created_at AS ready_at
    FROM model_outputs output
    JOIN model_call_contexts source_context
      ON source_context.project_id = p_project_id
     AND source_context.agent_id = output.agent_id
     AND source_context.id = output.model_call_context_id
     AND source_context.operation_kind = 'normal'
     AND source_context.state = 'succeeded'
    JOIN agent_events source_event ON source_event.agent_id = output.agent_id
      AND source_event.model_output_id = output.id
      AND source_event.event_kind = 'model_output'
    WHERE output.agent_id = p_agent_id
      AND output.continue_after_truncation
      AND source_event.turn_id = agent_latest_turn_id(p_project_id, p_agent_id)
      AND NOT EXISTS (
          SELECT 1 FROM agent_stop_events stop_event
          WHERE stop_event.project_id = p_project_id
            AND stop_event.agent_id = p_agent_id
            AND stop_event.sequence > source_event.sequence
      )
      AND NOT EXISTS (
          SELECT 1 FROM model_call_contexts later_context
          WHERE later_context.project_id = p_project_id
            AND later_context.agent_id = p_agent_id
            AND later_context.operation_kind = 'normal'
            AND later_context.input_event_sequence >= source_event.sequence
      )
    ORDER BY source_event.sequence
    LIMIT 1
),
checkpoint AS (
    SELECT frontier.turn_id,
           frontier.checkpoint_event_sequence AS opening_watermark,
           frontier.ready_at
    FROM agent_unconsumed_context_checkpoint_frontiers(p_project_id, p_agent_id)
      AS frontier(
        turn_id,
        checkpoint_event_sequence,
        ready_at
    )
    LIMIT 1
),
tool_batch AS MATERIALIZED (
    SELECT agent_has_incomplete_tool_batch(p_project_id, p_agent_id) AS incomplete
),
candidates AS (
    SELECT 'start'::text AS work_kind,
           NULL::uuid AS model_call_context_id,
           NULL::uuid AS model_output_id,
           turn_id,
           opening_watermark,
           ready_at,
           1 AS work_order,
           1::bigint AS source_order
    FROM unstarted
    UNION ALL
    SELECT 'resume', model_call_context_id, NULL::uuid, turn_id,
           opening_watermark, ready_at, 2, 1
    FROM retry
    UNION ALL
    SELECT 'start', NULL::uuid, NULL::uuid, turn_id,
           opening_watermark, ready_at, 2, 2
    FROM later_semantic
    UNION ALL
    SELECT 'start', NULL::uuid, NULL::uuid, turn_id,
           opening_watermark, ready_at, 2, 3
    FROM config_change
    UNION ALL
    SELECT 'continue', model_call_context_id, model_output_id, turn_id,
           opening_watermark, ready_at, 3, 1
    FROM completed_tools
    UNION ALL
    SELECT 'continue', model_call_context_id, model_output_id, turn_id,
           opening_watermark, ready_at, 3, 2
    FROM truncated_output
    UNION ALL
    SELECT 'start', NULL::uuid, NULL::uuid, turn_id,
           opening_watermark, ready_at, 3, 3
    FROM checkpoint
),
available AS (
    SELECT candidate.work_kind,
           candidate.model_call_context_id,
           candidate.model_output_id,
           candidate.turn_id,
           candidate.opening_watermark,
           candidate.ready_at,
           candidate.work_order,
           candidate.source_order
    FROM candidates candidate
    CROSS JOIN tool_batch
    WHERE NOT tool_batch.incomplete
)
SELECT candidate.work_kind,
       candidate.model_call_context_id,
       candidate.model_output_id,
       candidate.turn_id,
       array_agg(opening.input_id ORDER BY opening.event_sequence)::uuid[],
       min(opening.event_sequence)::bigint,
       candidate.ready_at
FROM available candidate
CROSS JOIN LATERAL agent_model_call_opening_content_inputs(
    p_project_id,
    p_agent_id,
    candidate.turn_id,
    candidate.opening_watermark
) AS opening(input_id, event_sequence)
GROUP BY candidate.work_kind,
         candidate.model_call_context_id,
         candidate.model_output_id,
         candidate.turn_id,
         candidate.ready_at,
         candidate.work_order,
         candidate.source_order
ORDER BY candidate.work_order,
         candidate.source_order,
         min(opening.event_sequence),
         candidate.turn_id
LIMIT 1
$$;
-- +goose StatementEnd

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
       model_call.provider_metadata,
       coalesce(model_output.continue_after_truncation, false)::boolean AS continue_after_truncation
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

ALTER TABLE model_provider_configs
    ALTER COLUMN request_timeout_ms SET DEFAULT 3600000,
    ADD COLUMN idle_timeout_ms integer NOT NULL DEFAULT 300000
        CHECK (idle_timeout_ms > 0);

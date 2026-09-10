-- +goose Up

ALTER TABLE configured_model_revisions
    ALTER COLUMN max_output_tokens DROP NOT NULL,
    ADD CONSTRAINT configured_model_revisions_minimum_context
    CHECK (context_window_tokens >= 2),
    ADD CONSTRAINT configured_model_revisions_default_output_within_context
    CHECK (default_max_output_tokens IS NULL OR default_max_output_tokens < context_window_tokens);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_tool_call_result()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Deferred checks must observe the final row, including calls created and
    -- rejected together with their result in one transaction.
    IF EXISTS (
        SELECT 1 FROM tool_calls call
        WHERE call.agent_id = NEW.agent_id AND call.id = NEW.id
          AND (call.state = 'completed') IS DISTINCT FROM EXISTS (
              SELECT 1 FROM tool_call_results result
              WHERE result.agent_id = call.agent_id AND result.tool_call_id = call.id
          )
    ) THEN
        RAISE EXCEPTION 'tool call % completion state must match result existence', NEW.id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

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
      AND output.stop_reason = 'max_tokens'
      AND NOT EXISTS (
          SELECT 1 FROM tool_calls call
          WHERE call.agent_id = output.agent_id
            AND call.model_output_id = output.id
      )
      AND source_event.turn_id = agent_latest_turn_id(p_project_id, p_agent_id)
      AND NOT EXISTS (
          SELECT 1 FROM agent_stop_events stop_event
          WHERE stop_event.project_id = p_project_id
            AND stop_event.agent_id = p_agent_id
            AND stop_event.sequence > source_event.sequence
      )
      AND source_event.sequence > (
          SELECT max(later_context.input_event_sequence)
          FROM model_call_contexts later_context
          WHERE later_context.project_id = p_project_id
            AND later_context.agent_id = p_agent_id
            AND later_context.operation_kind = 'normal'
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

ALTER TABLE model_provider_configs
    ALTER COLUMN request_timeout_ms SET DEFAULT 3600000,
    ADD COLUMN idle_timeout_ms integer NOT NULL DEFAULT 300000
        CHECK (idle_timeout_ms > 0);

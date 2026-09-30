-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN parent_normal_model_call_context_id uuid,
    ADD COLUMN replaces_checkpoint_id uuid,
    ADD COLUMN source_excerpt_bytes integer,
    ADD COLUMN recovery_max_output_tokens integer,
    ADD COLUMN recovery_checkpoint_retained_bytes integer,
    ADD COLUMN optional_input_target_tokens integer,
    ADD COLUMN optional_compaction_outcome text,
    ADD COLUMN request_input_fingerprint text,
    ADD COLUMN request_input_item_count integer;

-- Existing compactions have exactly one blocked normal operation at their frontier.
-- Backfill before strengthening the immutable identity and parent constraints.
ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard;
UPDATE model_call_contexts child
SET parent_normal_model_call_context_id = (
    SELECT parent.id FROM model_call_contexts parent
    WHERE parent.agent_id = child.agent_id
      AND parent.input_event_sequence = child.input_event_sequence
      AND parent.operation_kind = 'normal'
      AND parent.state = 'failed' AND parent.recovery_kind = 'compact'
      AND parent.created_at <= child.created_at
    ORDER BY parent.attempt_number DESC LIMIT 1
)
WHERE child.operation_kind = 'compaction';
ALTER TABLE model_call_contexts ENABLE TRIGGER model_call_contexts_transition_guard;

-- +goose StatementBegin
DO $$
DECLARE existing_name text;
BEGIN
    SELECT conname INTO STRICT existing_name FROM pg_constraint
    WHERE conrelid = 'model_call_contexts'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%reduce_compaction_source%';
    EXECUTE format('ALTER TABLE model_call_contexts DROP CONSTRAINT %I', existing_name);
    SELECT conname INTO STRICT existing_name FROM pg_constraint
    WHERE conrelid = 'model_call_contexts'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%retry_at%';
    EXECUTE format('ALTER TABLE model_call_contexts DROP CONSTRAINT %I', existing_name);
END;
$$;
-- +goose StatementEnd

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_recovery CHECK (
        recovery_kind IS NULL OR (
            state = 'failed' AND (
                (operation_kind = 'normal' AND recovery_kind IN ('retry', 'restore_output', 'compact', 'compact_optional')) OR
                (operation_kind = 'compaction' AND recovery_kind IN ('retry', 'reduce_compaction_source', 'resume_normal'))
            )
        )
    ),
    ADD CONSTRAINT model_call_contexts_retry_time CHECK (
        (coalesce(recovery_kind IN ('retry', 'restore_output'), false)) = (retry_at IS NOT NULL)
    ),
    ADD CONSTRAINT model_call_contexts_restore_output CHECK (
        recovery_kind IS DISTINCT FROM 'restore_output' OR (
            operation_kind = 'normal' AND error_kind = 'transient' AND api_format <> '' AND api_variant <> ''
            AND recovery_max_output_tokens IS NULL
            AND recovery_checkpoint_retained_bytes IS NULL
        )
    ),
    ADD CONSTRAINT model_call_contexts_parent FOREIGN KEY (agent_id, parent_normal_model_call_context_id)
        REFERENCES model_call_contexts(agent_id, id),
    ADD CONSTRAINT model_call_contexts_parent_operation CHECK (
        (operation_kind = 'compaction') = (parent_normal_model_call_context_id IS NOT NULL)
    ),
    ADD CONSTRAINT model_call_contexts_excerpt CHECK (
        source_excerpt_bytes IS NULL OR (operation_kind = 'compaction' AND source_excerpt_bytes > 0)
    ),
    ADD CONSTRAINT model_call_contexts_replaced_checkpoint FOREIGN KEY (agent_id, replaces_checkpoint_id)
        REFERENCES context_checkpoints(agent_id, id),
    ADD CONSTRAINT model_call_contexts_replaced_checkpoint_operation CHECK (
        replaces_checkpoint_id IS NULL OR operation_kind = 'compaction'
    ),
    ADD CONSTRAINT model_call_contexts_recovery_output CHECK (
        recovery_max_output_tokens IS NULL OR (
            operation_kind = 'normal' AND state = 'failed' AND recovery_kind IS NOT DISTINCT FROM 'retry'
            AND recovery_max_output_tokens > 0
        )
    ),
    ADD CONSTRAINT model_call_contexts_recovery_checkpoint_projection CHECK (
        recovery_checkpoint_retained_bytes IS NULL OR (
            operation_kind = 'normal' AND state = 'failed' AND recovery_kind IS NOT DISTINCT FROM 'retry'
            AND error_kind IN ('context_window', 'payload_too_large')
            AND api_format <> '' AND api_variant <> '' AND recovery_checkpoint_retained_bytes >= 0)
    ),
    ADD CONSTRAINT model_call_contexts_optional_input_target CHECK (
        (recovery_kind IS NOT DISTINCT FROM 'compact_optional') = (optional_input_target_tokens IS NOT NULL)
        AND (optional_input_target_tokens IS NULL OR optional_input_target_tokens > 0)
    ),
    ADD CONSTRAINT model_call_contexts_optional_outcome CHECK (
        optional_compaction_outcome IS NULL OR (recovery_kind IS NOT DISTINCT FROM 'resume_normal'
            AND optional_compaction_outcome IN ('interrupted', 'ineffective'))
    ),
    ADD CONSTRAINT model_call_contexts_request_input_identity CHECK (
        num_nonnulls(request_input_fingerprint, request_input_item_count) = 0
        OR (
            num_nonnulls(request_input_fingerprint, request_input_item_count) = 2
            AND operation_kind = 'normal' AND state = 'succeeded'
            AND request_input_item_count > 0
            AND request_input_fingerprint ~ '^[0-9a-fA-F]{64}$'
        )
    );

ALTER TABLE context_checkpoints
    DROP CONSTRAINT context_checkpoints_agent_id_summarized_through_event_seque_key;

DROP INDEX model_call_contexts_compaction_identity_idx;
CREATE UNIQUE INDEX model_call_contexts_compaction_identity_idx ON model_call_contexts(
    project_id, agent_id, parent_normal_model_call_context_id,
    source_event_sequence_end, coalesce(source_excerpt_bytes, 0), attempt_number
) WHERE operation_kind = 'compaction';

CREATE OR REPLACE VIEW model_call_context_turns AS
SELECT context.project_id, context.agent_id, context.id AS model_call_context_id,
       context.input_event_sequence, context.operation_kind, context.attempt_number,
       context.source_event_sequence_end, context.state, context.recovery_kind,
       context_turn.turn_id, context.parent_normal_model_call_context_id,
       context.source_excerpt_bytes
FROM model_call_contexts context
JOIN LATERAL (
    SELECT agent_turn_id_at_event_sequence(context.agent_id, context.input_event_sequence) AS turn_id
) context_turn ON context_turn.turn_id IS NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent_continuable_model_contexts(p_project_id uuid, p_agent_id uuid)
RETURNS TABLE (
    turn_id uuid,
    model_call_context_id uuid,
    input_event_sequence bigint,
    has_later_semantic_event boolean
)
LANGUAGE sql
STABLE
AS $$
WITH latest_turn AS MATERIALIZED (
    SELECT agent_latest_turn_id(p_project_id, p_agent_id) AS turn_id
)
SELECT context.turn_id,
       context.model_call_context_id,
       context.input_event_sequence,
       model_call_context_has_later_semantic_event(
           context.project_id,
           context.agent_id,
           context.model_call_context_id
       )
FROM model_call_context_turns context
JOIN latest_turn ON latest_turn.turn_id = context.turn_id
WHERE context.project_id = p_project_id
  AND context.agent_id = p_agent_id
  AND (
      context.state = 'started'
      OR (
          context.state = 'failed'
          AND context.recovery_kind IS NOT NULL
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM model_call_contexts newer
      WHERE newer.project_id = context.project_id
        AND newer.agent_id = context.agent_id
        AND newer.operation_kind = context.operation_kind
        AND newer.input_event_sequence = context.input_event_sequence
        AND newer.parent_normal_model_call_context_id IS NOT DISTINCT FROM context.parent_normal_model_call_context_id
        AND (
            (
                newer.source_event_sequence_end IS NOT DISTINCT FROM context.source_event_sequence_end
                AND newer.source_excerpt_bytes IS NOT DISTINCT FROM context.source_excerpt_bytes
                AND newer.attempt_number > context.attempt_number
            )
            OR (
                context.operation_kind = 'compaction'
                AND (newer.source_event_sequence_end < context.source_event_sequence_end
                    OR (newer.source_event_sequence_end = context.source_event_sequence_end
                        AND newer.source_excerpt_bytes IS NOT NULL
                        AND (context.source_excerpt_bytes IS NULL OR newer.source_excerpt_bytes < context.source_excerpt_bytes)))
            )
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM model_call_context_turns later_normal
      WHERE later_normal.project_id = context.project_id
        AND later_normal.agent_id = context.agent_id
        AND later_normal.turn_id = context.turn_id
        AND later_normal.operation_kind = 'normal'
        AND (later_normal.input_event_sequence > context.input_event_sequence
            OR (context.operation_kind = 'compaction'
                AND later_normal.input_event_sequence = context.input_event_sequence
                AND later_normal.attempt_number > (SELECT parent.attempt_number FROM model_call_contexts parent
                    WHERE parent.id = context.parent_normal_model_call_context_id)))
  )
  AND (
      context.operation_kind = 'compaction'
      OR (
          context.operation_kind = 'normal'
          AND NOT EXISTS (
              SELECT 1
              FROM model_call_contexts dependency
              WHERE dependency.project_id = context.project_id
                AND dependency.agent_id = context.agent_id
                AND dependency.operation_kind = 'compaction'
                AND dependency.parent_normal_model_call_context_id = context.model_call_context_id
          )
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM agent_stop_events stop_event
      WHERE stop_event.project_id = context.project_id
        AND stop_event.agent_id = context.agent_id
        AND stop_event.sequence > context.input_event_sequence
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION model_call_productive_frontier(p_context_id uuid)
RETURNS bigint LANGUAGE sql STABLE AS $$
SELECT coalesce(max(event.sequence), 0)::bigint
FROM model_call_contexts current
JOIN agent_events event ON event.agent_id = current.agent_id AND event.sequence <= current.input_event_sequence
LEFT JOIN model_outputs output ON output.agent_id = event.agent_id AND output.id = event.model_output_id
LEFT JOIN model_call_contexts producer ON producer.agent_id = output.agent_id AND producer.id = output.model_call_context_id
WHERE current.id = p_context_id
  AND (event.event_kind IN ('agent_input', 'tool_result')
    OR (event.event_kind = 'model_output' AND producer.operation_kind = 'normal' AND producer.state = 'succeeded'
      AND (output.stop_reason NOT IN ('max_tokens', 'error')
        OR EXISTS (SELECT 1 FROM tool_calls call WHERE call.agent_id = output.agent_id AND call.model_output_id = output.id))))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_model_call_context_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    projection_checkpoint_sequence bigint;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'model_call_contexts are immutable'
            USING ERRCODE = '25006';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'started' THEN
            RAISE EXCEPTION 'model_call_contexts must be inserted in started state'
                USING ERRCODE = '23514';
        END IF;
        IF NEW.operation_kind = 'compaction' AND NOT EXISTS (
            SELECT 1 FROM model_call_contexts parent
            WHERE parent.id = NEW.parent_normal_model_call_context_id
              AND parent.agent_id = NEW.agent_id AND parent.project_id = NEW.project_id
              AND parent.operation_kind = 'normal' AND parent.state = 'failed'
              AND parent.recovery_kind IN ('compact', 'compact_optional')
              AND parent.input_event_sequence = NEW.input_event_sequence
              AND parent.agent_config_id = NEW.agent_config_id
        ) THEN
            RAISE EXCEPTION 'compaction requires its blocked normal parent' USING ERRCODE = '23514';
        END IF;
        IF NEW.replaces_checkpoint_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM context_checkpoints prior
            WHERE prior.agent_id = NEW.agent_id AND prior.id = NEW.replaces_checkpoint_id
              AND prior.summarized_through_event_sequence = NEW.source_event_sequence_end
              AND prior.id = (
                  SELECT event.context_checkpoint_id FROM agent_events event
                  WHERE event.agent_id = NEW.agent_id AND event.event_kind = 'context_checkpoint'
                    AND event.sequence <= NEW.input_event_sequence
                  ORDER BY event.sequence DESC LIMIT 1
              )
        ) THEN
            RAISE EXCEPTION 'compaction must replace its latest applicable checkpoint at the same coverage'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.org_id IS DISTINCT FROM NEW.org_id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.agent_id IS DISTINCT FROM NEW.agent_id
       OR OLD.operation_kind IS DISTINCT FROM NEW.operation_kind
       OR OLD.attempt_number IS DISTINCT FROM NEW.attempt_number
       OR OLD.agent_config_id IS DISTINCT FROM NEW.agent_config_id
       OR OLD.configured_model_revision_id IS DISTINCT FROM NEW.configured_model_revision_id
       OR OLD.input_event_sequence IS DISTINCT FROM NEW.input_event_sequence
       OR OLD.source_event_sequence_end IS DISTINCT FROM NEW.source_event_sequence_end
       OR OLD.parent_normal_model_call_context_id IS DISTINCT FROM NEW.parent_normal_model_call_context_id
       OR OLD.source_excerpt_bytes IS DISTINCT FROM NEW.source_excerpt_bytes
       OR OLD.replaces_checkpoint_id IS DISTINCT FROM NEW.replaces_checkpoint_id
       OR OLD.runtime_lock_id IS DISTINCT FROM NEW.runtime_lock_id
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'model_call_context identity and runtime ownership are immutable'
            USING ERRCODE = '25006';
    END IF;

    IF OLD.state IN ('succeeded', 'failed', 'canceled') THEN
        RAISE EXCEPTION 'terminal model_call_contexts are immutable'
            USING ERRCODE = '25006';
    END IF;

    IF OLD.state <> 'started' OR NEW.state NOT IN ('succeeded', 'failed', 'canceled') THEN
        RAISE EXCEPTION 'invalid model_call_context transition % -> %', OLD.state, NEW.state
            USING ERRCODE = '23514';
    END IF;

    IF NEW.recovery_kind = 'resume_normal' AND NOT EXISTS (
        SELECT 1 FROM model_call_contexts parent
        WHERE parent.id = NEW.parent_normal_model_call_context_id
          AND (parent.recovery_kind = 'compact_optional' OR NEW.replaces_checkpoint_id IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'only optional or checkpoint replacement compaction can resume its normal parent'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.recovery_kind = 'resume_normal' AND EXISTS (
        SELECT 1 FROM model_call_contexts parent
        WHERE parent.id = NEW.parent_normal_model_call_context_id
          AND ((parent.recovery_kind = 'compact_optional') <> (NEW.optional_compaction_outcome IS NOT NULL))
    ) THEN
        RAISE EXCEPTION 'optional compaction outcomes require an optional parent'
            USING ERRCODE = '23514', CONSTRAINT = 'model_call_contexts_optional_outcome';
    END IF;
    IF NEW.recovery_checkpoint_retained_bytes IS NOT NULL THEN
        SELECT max(event.sequence) INTO projection_checkpoint_sequence
        FROM agent_events event
        WHERE event.agent_id = NEW.agent_id AND event.event_kind = 'context_checkpoint'
          AND event.sequence <= NEW.input_event_sequence;
        IF NOT EXISTS (
            SELECT 1 FROM context_checkpoints checkpoint
            JOIN agent_events event ON event.agent_id = checkpoint.agent_id
              AND event.context_checkpoint_id = checkpoint.id AND event.event_kind = 'context_checkpoint'
            WHERE checkpoint.agent_id = NEW.agent_id AND event.sequence = projection_checkpoint_sequence
              AND NEW.recovery_checkpoint_retained_bytes < octet_length(checkpoint.summary)
              AND checkpoint.summarized_through_event_sequence < (
                  SELECT min(opening.event_sequence) FROM agent_model_call_opening_content_inputs(
                      NEW.project_id, NEW.agent_id,
                      agent_turn_id_at_event_sequence(NEW.agent_id, NEW.input_event_sequence),
                      NEW.input_event_sequence
                  ) opening
              )
        ) THEN
            RAISE EXCEPTION 'recovery projection must reduce the latest applicable checkpoint'
                USING ERRCODE = '23514';
        END IF;
        IF EXISTS (
            SELECT 1 FROM model_call_contexts prior
            WHERE prior.agent_id = NEW.agent_id AND prior.id <> NEW.id
              AND prior.configured_model_revision_id = NEW.configured_model_revision_id
              AND prior.agent_config_id = NEW.agent_config_id
              AND prior.input_event_sequence >= projection_checkpoint_sequence
              AND NOT EXISTS (
                  SELECT 1 FROM agent_events event
                  WHERE event.agent_id = NEW.agent_id AND event.event_kind = 'context_checkpoint'
                    AND event.sequence > projection_checkpoint_sequence
                    AND event.sequence <= prior.input_event_sequence
              )
              AND prior.recovery_checkpoint_retained_bytes <= NEW.recovery_checkpoint_retained_bytes
        ) THEN
            RAISE EXCEPTION 'recovery checkpoint retained bytes must decrease' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF NEW.recovery_kind = 'restore_output' AND EXISTS (
        SELECT 1 FROM model_call_contexts prior
        WHERE prior.agent_id = NEW.agent_id AND prior.id <> NEW.id
          AND prior.agent_config_id = NEW.agent_config_id
          AND prior.configured_model_revision_id = NEW.configured_model_revision_id
          AND prior.recovery_kind = 'restore_output'
          AND prior.input_event_sequence >= model_call_productive_frontier(NEW.id)
    ) THEN
        RAISE EXCEPTION 'output allowance can only be restored once per productive frontier'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.recovery_max_output_tokens IS NOT NULL AND EXISTS (
        SELECT 1 FROM model_call_contexts prior
        WHERE prior.agent_id = NEW.agent_id AND prior.operation_kind = 'normal'
          AND prior.input_event_sequence = NEW.input_event_sequence
          AND prior.configured_model_revision_id = NEW.configured_model_revision_id
          AND prior.id <> NEW.id AND prior.recovery_max_output_tokens <= NEW.recovery_max_output_tokens
    ) THEN
        RAISE EXCEPTION 'recovery output allowance must decrease' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION model_call_transient_retry_count(p_context_id uuid)
RETURNS bigint LANGUAGE sql STABLE AS $$
SELECT count(*) FROM model_call_contexts current
JOIN model_call_contexts failure ON failure.agent_id = current.agent_id
    AND failure.operation_kind = current.operation_kind
WHERE current.id = p_context_id
  AND failure.state = 'failed' AND failure.recovery_kind = 'retry'
  AND failure.recovery_max_output_tokens IS NULL
  AND failure.recovery_checkpoint_retained_bytes IS NULL
  AND failure.created_at <= current.created_at
  AND ((current.operation_kind = 'normal' AND failure.input_event_sequence = current.input_event_sequence)
    OR (current.operation_kind = 'compaction'
        AND failure.parent_normal_model_call_context_id = current.parent_normal_model_call_context_id))
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
     AND retry_context.recovery_kind IN ('retry', 'restore_output')
     AND retry_context.retry_at IS NOT NULL
    WHERE NOT context.has_later_semantic_event
    LIMIT 1
),
resume_normal AS (
    SELECT context.turn_id, child.parent_normal_model_call_context_id AS model_call_context_id,
           context.input_event_sequence AS opening_watermark, child.completed_at AS ready_at
    FROM continuable context
    JOIN model_call_contexts child ON child.id = context.model_call_context_id
    WHERE child.state = 'failed' AND child.recovery_kind = 'resume_normal'
      AND NOT context.has_later_semantic_event
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
    SELECT 'resume', model_call_context_id, NULL::uuid, turn_id,
           opening_watermark, ready_at, 2, 1
    FROM resume_normal
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_context_checkpoint_lineage()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    producer_operation_kind text;
    producer_state text;
    producer_source_end bigint;
    producer_input_event_sequence bigint;
    checkpoint_event_sequence bigint;
    prior_summarized_through bigint;
    prior_checkpoint_id uuid;
    prior_summary_bytes bigint;
    producer_replaces_checkpoint_id uuid;
BEGIN
    SELECT context.operation_kind,
           context.state,
           context.source_event_sequence_end,
           context.input_event_sequence, context.replaces_checkpoint_id
    INTO producer_operation_kind,
         producer_state,
         producer_source_end,
         producer_input_event_sequence, producer_replaces_checkpoint_id
    FROM model_call_contexts context
    WHERE context.agent_id = NEW.agent_id
      AND context.id = NEW.producer_model_call_context_id;

    IF producer_operation_kind <> 'compaction'
       OR producer_state <> 'succeeded'
       OR producer_source_end <> NEW.summarized_through_event_sequence THEN
        RAISE EXCEPTION 'checkpoint must match its succeeded compaction context'
            USING ERRCODE = '23514';
    END IF;

    SELECT event.sequence
    INTO checkpoint_event_sequence
    FROM agent_events event
    WHERE event.agent_id = NEW.agent_id
      AND event.context_checkpoint_id = NEW.id
      AND event.event_kind = 'context_checkpoint';

    IF checkpoint_event_sequence IS NULL THEN
        RAISE EXCEPTION 'context checkpoint % must have a typed context_checkpoint event', NEW.id
            USING ERRCODE = '23514';
    ELSIF checkpoint_event_sequence <= NEW.summarized_through_event_sequence THEN
        RAISE EXCEPTION 'context checkpoint event must follow its summarized frontier'
            USING ERRCODE = '23514';
    END IF;

    SELECT prior.summarized_through_event_sequence, prior.id, octet_length(prior.summary)::bigint
    INTO prior_summarized_through, prior_checkpoint_id, prior_summary_bytes
    FROM context_checkpoints prior
    JOIN agent_events prior_event ON prior_event.agent_id = prior.agent_id
      AND prior_event.context_checkpoint_id = prior.id
      AND prior_event.event_kind = 'context_checkpoint'
    WHERE prior.agent_id = NEW.agent_id
      AND prior_event.sequence <= producer_input_event_sequence
    ORDER BY prior_event.sequence DESC LIMIT 1;

    IF producer_replaces_checkpoint_id IS NOT NULL THEN
        IF producer_replaces_checkpoint_id IS DISTINCT FROM prior_checkpoint_id
           OR NEW.summarized_through_event_sequence IS DISTINCT FROM prior_summarized_through
           OR octet_length(NEW.summary)::bigint * 10 > prior_summary_bytes * 9 THEN
            RAISE EXCEPTION 'replacement checkpoint must reduce its latest applicable summary by at least ten percent'
                USING ERRCODE = '23514';
        END IF;
    ELSIF prior_summarized_through IS NOT NULL
       AND NEW.summarized_through_event_sequence <= prior_summarized_through THEN
        RAISE EXCEPTION 'checkpoint must advance beyond the applicable prior checkpoint'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN parent_normal_model_call_context_id uuid,
    ADD COLUMN input_fingerprint text,
    ADD CONSTRAINT model_call_contexts_input_fingerprint CHECK (
        input_fingerprint IS NULL OR (
            operation_kind = 'normal' AND state = 'succeeded'
            AND coalesce(input_tokens_total, 0) > 0
            AND input_fingerprint ~ '^[0-9a-f]{64}$'
        )
    );

-- Existing compactions have exactly one blocked normal operation at their frontier.
-- Backfill before strengthening the immutable identity and parent constraints.
ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard;
DROP INDEX model_call_contexts_compaction_identity_idx;
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
WITH numbered AS (
    SELECT id, row_number() OVER (
        PARTITION BY parent_normal_model_call_context_id
        ORDER BY source_event_sequence_end DESC, attempt_number
    )::integer AS attempt_number
    FROM model_call_contexts WHERE operation_kind = 'compaction'
)
UPDATE model_call_contexts context
SET attempt_number = numbered.attempt_number
FROM numbered WHERE numbered.id = context.id;
ALTER TABLE model_call_contexts ENABLE TRIGGER model_call_contexts_transition_guard;

-- +goose StatementBegin
DO $$
DECLARE existing_name text;
BEGIN
    SELECT conname INTO STRICT existing_name FROM pg_constraint
    WHERE conrelid = 'model_call_contexts'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%reduce_compaction_source%';
    EXECUTE format('ALTER TABLE model_call_contexts DROP CONSTRAINT %I', existing_name);
END;
$$;
-- +goose StatementEnd

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_recovery CHECK (
        recovery_kind IS NULL OR (
            state = 'failed' AND (
                (operation_kind = 'normal' AND recovery_kind IN ('retry', 'compact', 'compact_optional')) OR
                (operation_kind = 'compaction' AND recovery_kind IN ('retry', 'reduce_compaction_source'))
            )
        )
    ),
    ADD CONSTRAINT model_call_contexts_parent FOREIGN KEY (agent_id, parent_normal_model_call_context_id)
        REFERENCES model_call_contexts(agent_id, id),
    ADD CONSTRAINT model_call_contexts_parent_operation CHECK (
        (operation_kind = 'compaction') = (parent_normal_model_call_context_id IS NOT NULL)
    );

CREATE INDEX agent_events_checkpoint_sequence_idx ON agent_events(agent_id, sequence)
    WHERE event_kind = 'context_checkpoint';

CREATE UNIQUE INDEX model_call_contexts_compaction_identity_idx ON model_call_contexts(
    project_id, agent_id, parent_normal_model_call_context_id, attempt_number
) WHERE operation_kind = 'compaction';

DROP INDEX model_call_contexts_one_compaction_outcome_idx;
CREATE UNIQUE INDEX model_call_contexts_one_compaction_outcome_idx
    ON model_call_contexts(project_id, agent_id, parent_normal_model_call_context_id)
    WHERE operation_kind = 'compaction'
      AND (state = 'succeeded' OR (state = 'failed' AND recovery_kind IS NULL));

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
WITH latest_normal AS MATERIALIZED (
    SELECT context.id
    FROM model_call_contexts context
    WHERE context.project_id = p_project_id AND context.agent_id = p_agent_id
      AND context.operation_kind = 'normal'
    ORDER BY context.input_event_sequence DESC, context.attempt_number DESC LIMIT 1
), current_context AS (
    SELECT coalesce((
        SELECT child.id FROM model_call_contexts child
        WHERE child.project_id = p_project_id AND child.agent_id = p_agent_id
          AND child.operation_kind = 'compaction'
          AND child.parent_normal_model_call_context_id = latest_normal.id
        ORDER BY child.attempt_number DESC LIMIT 1
    ), latest_normal.id) AS id
    FROM latest_normal
)
SELECT context_turn.turn_id,
       context.id,
       context.input_event_sequence,
       model_call_context_has_later_semantic_event(
           context.project_id,
           context.agent_id,
           context.id
       )
FROM current_context
JOIN model_call_contexts context ON context.id = current_context.id
CROSS JOIN LATERAL (
    SELECT agent_turn_id_at_event_sequence(context.agent_id, context.input_event_sequence) AS turn_id
) context_turn
WHERE context_turn.turn_id = agent_latest_turn_id(p_project_id, p_agent_id)
  AND (
      context.state = 'started'
      OR (
          context.state = 'failed'
          AND (context.recovery_kind IS NOT NULL OR EXISTS (
              SELECT 1 FROM model_call_contexts parent
              WHERE parent.id = context.parent_normal_model_call_context_id
                AND parent.recovery_kind = 'compact_optional'
          ))
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
CREATE OR REPLACE FUNCTION enforce_model_call_context_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
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

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION model_call_transient_retry_count(p_context_id uuid)
RETURNS bigint LANGUAGE sql STABLE AS $$
SELECT count(*) FROM model_call_contexts current
JOIN model_call_contexts failure ON failure.project_id = current.project_id
    AND failure.agent_id = current.agent_id
    AND failure.input_event_sequence = current.input_event_sequence
    AND failure.operation_kind = current.operation_kind
WHERE current.id = p_context_id
  AND failure.state = 'failed' AND failure.recovery_kind = 'retry'
  AND failure.attempt_number <= current.attempt_number
  AND (current.operation_kind = 'normal'
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
     AND retry_context.recovery_kind = 'retry'
     AND retry_context.retry_at IS NOT NULL
    WHERE NOT context.has_later_semantic_event
    LIMIT 1
),
optional_failure AS (
    SELECT context.turn_id, child.parent_normal_model_call_context_id AS model_call_context_id,
           context.input_event_sequence AS opening_watermark, child.completed_at AS ready_at
    FROM continuable context
    JOIN model_call_contexts child ON child.id = context.model_call_context_id
    JOIN model_call_contexts parent ON parent.id = child.parent_normal_model_call_context_id
      AND parent.recovery_kind = 'compact_optional'
    WHERE child.state = 'failed' AND child.recovery_kind IS NULL
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
    FROM optional_failure
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
CREATE OR REPLACE FUNCTION enforce_model_call_context_terminal_outcome()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    output_count bigint;
    checkpoint_count bigint;
BEGIN
    IF NEW.state = 'started' THEN
        RETURN NEW;
    END IF;

    SELECT count(*) INTO output_count
    FROM model_outputs output
    WHERE output.agent_id = NEW.agent_id
      AND output.model_call_context_id = NEW.id;

    SELECT count(*) INTO checkpoint_count
    FROM context_checkpoints checkpoint
    WHERE checkpoint.agent_id = NEW.agent_id
      AND checkpoint.producer_model_call_context_id = NEW.id;

    IF NEW.state = 'succeeded' AND NEW.operation_kind = 'normal' THEN
        IF NOT (output_count = 1 AND checkpoint_count = 0) THEN
            RAISE EXCEPTION 'succeeded normal context must own exactly one model output'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.state = 'succeeded' AND NEW.operation_kind = 'compaction' THEN
        IF NOT (output_count = 0 AND checkpoint_count = 1) THEN
            RAISE EXCEPTION 'succeeded compaction context must own exactly one checkpoint'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.state = 'failed'
       AND NEW.recovery_kind IS NULL
       AND NOT EXISTS (
           SELECT 1 FROM model_call_contexts parent
           WHERE parent.id = NEW.parent_normal_model_call_context_id
             AND parent.recovery_kind = 'compact_optional'
       ) THEN
        IF NOT (output_count = 1 AND checkpoint_count = 0) THEN
            RAISE EXCEPTION 'stopped model call context must own exactly one error output'
                USING ERRCODE = '23514';
        END IF;
    ELSIF output_count <> 0 OR checkpoint_count <> 0 THEN
        RAISE EXCEPTION 'model call context outcome does not match its terminal state'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

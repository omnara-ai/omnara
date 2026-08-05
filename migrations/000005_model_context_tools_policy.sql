-- +goose Up

CREATE TABLE model_call_contexts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    operation_kind text NOT NULL,
    attempt_number integer NOT NULL,
    agent_config_id uuid NOT NULL,
    configured_model_revision_id uuid NOT NULL,
    input_event_sequence bigint NOT NULL,
    source_event_sequence_end bigint,
    runtime_lock_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'started',
    recovery_kind text,
    api_format text NOT NULL DEFAULT '',
    api_variant text NOT NULL DEFAULT '',
    provider_request_id text NOT NULL DEFAULT '',
    provider_response_id text NOT NULL DEFAULT '',
    error_kind text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    error_details jsonb NOT NULL DEFAULT '{}'::jsonb,
    retry_at timestamptz,
    input_tokens_total integer,
    uncached_input_tokens integer,
    cache_read_input_tokens integer,
    cache_write_input_tokens integer,
    output_tokens_total integer,
    reasoning_output_tokens integer,
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    CHECK (operation_kind IN ('normal', 'compaction')),
    CHECK (attempt_number > 0),
    CHECK (input_event_sequence > 0),
    CHECK (state IN ('started', 'succeeded', 'failed', 'canceled')),
    CHECK (
        recovery_kind IS NULL
        OR (
            operation_kind = 'normal'
            AND recovery_kind IN ('retry', 'compact')
            AND state = 'failed'
        )
        OR (
            operation_kind = 'compaction'
            AND state = 'failed'
            AND recovery_kind IN ('retry', 'reduce_compaction_source')
        )
    ),
    CHECK (jsonb_typeof(error_details) = 'object'),
    CHECK (input_tokens_total IS NULL OR input_tokens_total >= 0),
    CHECK (uncached_input_tokens IS NULL OR uncached_input_tokens >= 0),
    CHECK (cache_read_input_tokens IS NULL OR cache_read_input_tokens >= 0),
    CHECK (cache_write_input_tokens IS NULL OR cache_write_input_tokens >= 0),
    CHECK (output_tokens_total IS NULL OR output_tokens_total >= 0),
    CHECK (reasoning_output_tokens IS NULL OR reasoning_output_tokens >= 0),
    CHECK (
        input_tokens_total IS NULL OR
        input_tokens_total = coalesce(uncached_input_tokens, 0) +
            coalesce(cache_read_input_tokens, 0) +
            coalesce(cache_write_input_tokens, 0)
    ),
    CHECK (
        reasoning_output_tokens IS NULL OR output_tokens_total IS NULL OR
        reasoning_output_tokens <= output_tokens_total
    ),
    CHECK (
        (
            operation_kind = 'normal'
            AND source_event_sequence_end IS NULL
        )
        OR (
            operation_kind = 'compaction'
            AND source_event_sequence_end IS NOT NULL
            AND source_event_sequence_end > 0
            AND source_event_sequence_end <= input_event_sequence
        )
    ),
    CHECK ((api_format = '') = (api_variant = '')),
    CHECK (completed_at IS NULL OR completed_at >= created_at),
    CHECK ((recovery_kind IS NOT DISTINCT FROM 'retry') = (retry_at IS NOT NULL)),
    CHECK ((state = 'started') = (completed_at IS NULL)),
    CHECK (
        state NOT IN ('started', 'succeeded')
        OR (
            error_kind = ''
            AND error_code = ''
            AND error_message = ''
            AND error_details = '{}'::jsonb
        )
    ),
    CHECK (
        state <> 'started'
        OR api_format = ''
    ),
    CHECK (
        api_format <> ''
        OR (
            provider_request_id = ''
            AND provider_response_id = ''
            AND input_tokens_total IS NULL
            AND uncached_input_tokens IS NULL
            AND cache_read_input_tokens IS NULL
            AND cache_write_input_tokens IS NULL
            AND output_tokens_total IS NULL
            AND reasoning_output_tokens IS NULL
        )
    ),
    CHECK (state <> 'succeeded' OR api_format <> ''),
    CHECK (state NOT IN ('failed', 'canceled') OR error_kind <> ''),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, agent_config_id) REFERENCES agent_configs(project_id, id),
    FOREIGN KEY (org_id, configured_model_revision_id) REFERENCES configured_model_revisions(org_id, id),
    UNIQUE (agent_id, id)
);

CREATE UNIQUE INDEX model_call_contexts_normal_identity_idx
    ON model_call_contexts(
        project_id,
        agent_id,
        input_event_sequence,
        attempt_number
    )
    WHERE operation_kind = 'normal';

CREATE UNIQUE INDEX model_call_contexts_compaction_identity_idx
    ON model_call_contexts(
        project_id,
        agent_id,
        input_event_sequence,
        source_event_sequence_end,
        attempt_number
    )
    WHERE operation_kind = 'compaction';

CREATE UNIQUE INDEX model_call_contexts_one_live_idx
    ON model_call_contexts(project_id, agent_id)
    WHERE state = 'started';

CREATE UNIQUE INDEX model_call_contexts_one_normal_outcome_idx
    ON model_call_contexts(project_id, agent_id, input_event_sequence)
    WHERE operation_kind = 'normal'
      AND (
          state = 'succeeded'
          OR (
              state = 'failed'
              AND recovery_kind IS NULL
          )
      );

CREATE UNIQUE INDEX model_call_contexts_one_compaction_outcome_idx
    ON model_call_contexts(project_id, agent_id, input_event_sequence)
    WHERE operation_kind = 'compaction'
      AND (
          state = 'succeeded'
          OR (
              state = 'failed'
              AND recovery_kind IS NULL
          )
      );

CREATE INDEX model_call_contexts_resumable_idx
    ON model_call_contexts(project_id, agent_id, input_event_sequence, attempt_number)
    WHERE state = 'started'
       OR (state = 'failed' AND recovery_kind IS NOT NULL);

CREATE INDEX model_call_contexts_input_event_sequence_idx
    ON model_call_contexts(project_id, agent_id, input_event_sequence);

CREATE VIEW model_call_context_turns AS
SELECT context.project_id,
       context.agent_id,
       context.id AS model_call_context_id,
       context.input_event_sequence,
       context.operation_kind,
       context.attempt_number,
       context.source_event_sequence_end,
       context.state,
       context.recovery_kind,
       context_turn.turn_id
FROM model_call_contexts context
JOIN LATERAL (
    SELECT agent_turn_id_at_event_sequence(context.agent_id, context.input_event_sequence) AS turn_id
) context_turn ON context_turn.turn_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION agent_latest_turn_id(p_project_id uuid, p_agent_id uuid)
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
    SELECT turn.id
    FROM agent_turns turn
    JOIN agents agent ON agent.id = turn.agent_id
    WHERE agent.project_id = p_project_id
      AND turn.agent_id = p_agent_id
    ORDER BY turn.turn_sequence DESC
    LIMIT 1
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent_model_call_opening_content_inputs(
    p_project_id uuid,
    p_agent_id uuid,
    p_turn_id uuid,
    p_max_event_sequence bigint
)
RETURNS TABLE (
    input_id uuid,
    event_sequence bigint
)
LANGUAGE sql
STABLE
AS $$
WITH latest_boundary AS MATERIALIZED (
    SELECT greatest(
        coalesce((
            SELECT event.sequence
            FROM agent_events event
            WHERE event.agent_id = p_agent_id
              AND event.event_kind = 'model_output'
              AND event.sequence <= p_max_event_sequence
            ORDER BY event.sequence DESC
            LIMIT 1
        ), 0),
        coalesce((
            SELECT stop_event.sequence
            FROM agent_stop_events stop_event
            WHERE stop_event.project_id = p_project_id
              AND stop_event.agent_id = p_agent_id
              AND stop_event.sequence <= p_max_event_sequence
            ORDER BY stop_event.sequence DESC
            LIMIT 1
        ), 0)
    )::bigint AS sequence
),
unanswered AS MATERIALIZED (
    SELECT input.id AS input_id,
           event.sequence AS event_sequence
    FROM latest_boundary boundary
    JOIN agent_events event ON event.agent_id = p_agent_id
      AND event.event_kind = 'agent_input'
      AND event.is_opening_event
      AND event.sequence > boundary.sequence
      AND event.sequence <= p_max_event_sequence
    JOIN agent_inputs input ON input.project_id = p_project_id
      AND input.agent_id = event.agent_id
      AND input.id = event.agent_input_id
      AND input.input_kind = 'content'
      AND input.state = 'resolved'
      AND input.admitted_event_id = event.id
)
SELECT unanswered.input_id,
       unanswered.event_sequence
FROM unanswered
UNION ALL
SELECT opening.input_id,
       opening.event_sequence
FROM agent_turn_opening_content_inputs(
    p_agent_id,
    p_turn_id,
    p_max_event_sequence
) opening
WHERE NOT EXISTS (SELECT 1 FROM unanswered)
  AND EXISTS (
      SELECT 1
      FROM agents agent
      WHERE agent.project_id = p_project_id
        AND agent.id = p_agent_id
  )
ORDER BY event_sequence
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent_latest_unstarted_model_call_opening_inputs(p_project_id uuid, p_agent_id uuid)
RETURNS TABLE (
    turn_id uuid,
    opening_event_sequence bigint
)
LANGUAGE sql
STABLE
AS $$
WITH latest_turn AS MATERIALIZED (
    SELECT agent_latest_turn_id(p_project_id, p_agent_id) AS turn_id
),
turn_opening_watermark AS MATERIALIZED (
    SELECT turn.turn_id,
           min(event.sequence)::bigint AS first_opening_sequence,
           max(event.sequence)::bigint AS last_opening_sequence
    FROM latest_turn turn
    JOIN agent_events event ON event.agent_id = p_agent_id
      AND event.turn_id = turn.turn_id
      AND event.is_opening_event
    GROUP BY turn.turn_id
)
SELECT watermark.turn_id,
       opening.event_sequence AS opening_event_sequence
FROM turn_opening_watermark watermark
CROSS JOIN LATERAL agent_model_call_opening_content_inputs(
    p_project_id,
    p_agent_id,
    watermark.turn_id,
    watermark.last_opening_sequence
) opening
WHERE NOT EXISTS (
    SELECT 1
    FROM model_call_contexts context
    WHERE context.project_id = p_project_id
      AND context.agent_id = p_agent_id
      AND context.input_event_sequence >= watermark.first_opening_sequence
)
  AND NOT EXISTS (
    SELECT 1
    FROM agent_stop_events stop_event
    WHERE stop_event.project_id = p_project_id
      AND stop_event.agent_id = p_agent_id
      AND stop_event.sequence > watermark.first_opening_sequence
)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION model_call_context_has_later_semantic_event(
    p_project_id uuid,
    p_agent_id uuid,
    p_model_call_context_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM model_call_contexts context
        JOIN agent_turns turn ON turn.agent_id = context.agent_id
          AND turn.id = agent_latest_turn_id(p_project_id, p_agent_id)
        JOIN agent_events semantic_event ON semantic_event.agent_id = turn.agent_id
          AND semantic_event.id = turn.latest_semantic_event_id
        WHERE context.project_id = p_project_id
          AND context.agent_id = p_agent_id
          AND context.id = p_model_call_context_id
          AND semantic_event.sequence > context.input_event_sequence
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent_continuable_model_contexts(p_project_id uuid, p_agent_id uuid)
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
        AND (
            (
                newer.source_event_sequence_end IS NOT DISTINCT FROM context.source_event_sequence_end
                AND newer.attempt_number > context.attempt_number
            )
            OR (
                context.operation_kind = 'compaction'
                AND newer.source_event_sequence_end < context.source_event_sequence_end
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
        AND later_normal.input_event_sequence > context.input_event_sequence
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
                AND dependency.input_event_sequence = context.input_event_sequence
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
CREATE FUNCTION enforce_model_call_context_transition()
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

CREATE TRIGGER model_call_contexts_transition_guard
BEFORE INSERT OR UPDATE OR DELETE ON model_call_contexts
FOR EACH ROW EXECUTE FUNCTION enforce_model_call_context_transition();

CREATE TABLE model_outputs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    model_call_context_id uuid NOT NULL,
    served_provider_model_slug text NOT NULL DEFAULT '',
    stop_reason text NOT NULL,
    provider_replay jsonb,
    created_at timestamptz NOT NULL,
    CHECK (stop_reason IN ('end_turn', 'tool_use', 'max_tokens', 'refusal', 'content_filter', 'error')),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_id, model_call_context_id) REFERENCES model_call_contexts(agent_id, id),
    UNIQUE (agent_id, id),
    UNIQUE (agent_id, model_call_context_id)
);

ALTER TABLE agent_events
    ADD FOREIGN KEY (agent_id, model_output_id) REFERENCES model_outputs(agent_id, id);

CREATE UNIQUE INDEX agent_events_model_output_idx
    ON agent_events(agent_id, model_output_id)
    WHERE model_output_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION prevent_model_output_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'model_outputs are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER model_outputs_immutable
BEFORE UPDATE OR DELETE ON model_outputs
FOR EACH ROW EXECUTE FUNCTION prevent_model_output_mutation();

CREATE TABLE context_checkpoints (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    summarized_through_event_sequence bigint NOT NULL,
    producer_model_call_context_id uuid NOT NULL,
    summary text NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK (summarized_through_event_sequence > 0),
    CHECK (summary <> ''),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_id, producer_model_call_context_id) REFERENCES model_call_contexts(agent_id, id),
    UNIQUE (agent_id, id),
    UNIQUE (agent_id, summarized_through_event_sequence),
    UNIQUE (agent_id, producer_model_call_context_id)
);

ALTER TABLE agent_events
    ADD FOREIGN KEY (agent_id, context_checkpoint_id)
    REFERENCES context_checkpoints(agent_id, id);

CREATE UNIQUE INDEX agent_events_context_checkpoint_idx
    ON agent_events(agent_id, context_checkpoint_id)
    WHERE context_checkpoint_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION prevent_context_checkpoint_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'context_checkpoints are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER context_checkpoints_immutable
BEFORE UPDATE OR DELETE ON context_checkpoints
FOR EACH ROW EXECUTE FUNCTION prevent_context_checkpoint_mutation();

-- +goose StatementBegin
CREATE FUNCTION agent_unconsumed_context_checkpoint_frontiers(
    p_project_id uuid,
    p_agent_id uuid
)
RETURNS TABLE (
    turn_id uuid,
    checkpoint_event_sequence bigint,
    ready_at timestamptz
)
LANGUAGE sql
STABLE
AS $$
WITH latest_turn AS MATERIALIZED (
    SELECT agent_latest_turn_id(p_project_id, p_agent_id) AS turn_id
)
SELECT checkpoint_event.turn_id,
       checkpoint_event.sequence AS checkpoint_event_sequence,
       checkpoint.created_at AS ready_at
FROM context_checkpoints checkpoint
JOIN model_call_contexts producer_context
  ON producer_context.project_id = p_project_id
 AND producer_context.agent_id = checkpoint.agent_id
 AND producer_context.id = checkpoint.producer_model_call_context_id
JOIN agent_events checkpoint_event ON checkpoint_event.agent_id = checkpoint.agent_id
  AND checkpoint_event.context_checkpoint_id = checkpoint.id
  AND checkpoint_event.event_kind = 'context_checkpoint'
JOIN latest_turn ON latest_turn.turn_id = checkpoint_event.turn_id
WHERE checkpoint.agent_id = p_agent_id
  AND NOT EXISTS (
      SELECT 1
      FROM model_call_contexts context
      WHERE context.project_id = p_project_id
        AND context.agent_id = checkpoint.agent_id
        AND context.operation_kind = 'normal'
        AND context.input_event_sequence >= checkpoint_event.sequence
  )
  AND NOT EXISTS (
      SELECT 1
      FROM agent_stop_events stop_event
      WHERE stop_event.project_id = p_project_id
        AND stop_event.agent_id = checkpoint.agent_id
        AND stop_event.sequence > checkpoint_event.sequence
  )
ORDER BY checkpoint_event.sequence DESC
LIMIT 1
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent_unconsumed_config_change_frontiers(
    p_project_id uuid,
    p_agent_id uuid
)
RETURNS TABLE (
    turn_id uuid,
    config_event_sequence bigint,
    ready_at timestamptz
)
LANGUAGE sql
STABLE
AS $$
WITH latest_turn AS MATERIALIZED (
    SELECT turn.id AS turn_id,
           latest_event.sequence AS latest_event_sequence
    FROM agent_turns turn
    JOIN agent_events latest_event ON latest_event.agent_id = turn.agent_id
      AND latest_event.id = turn.latest_event_id
    WHERE turn.agent_id = p_agent_id
      AND turn.id = agent_latest_turn_id(p_project_id, p_agent_id)
),
opening_boundary AS MATERIALIZED (
    SELECT min(opening.event_sequence)::bigint AS first_content_sequence
    FROM latest_turn turn
    CROSS JOIN LATERAL agent_turn_opening_content_inputs(
        p_agent_id,
        turn.turn_id,
        turn.latest_event_sequence
    ) opening
),
latest_stop AS MATERIALIZED (
    SELECT stop_event.sequence
    FROM agent_stop_events stop_event
    WHERE stop_event.project_id = p_project_id
      AND stop_event.agent_id = p_agent_id
    ORDER BY stop_event.sequence DESC
    LIMIT 1
),
config_events AS MATERIALIZED (
    SELECT event.turn_id,
           event.sequence,
           event.created_at
    FROM agent_inputs input
    JOIN agent_events event ON event.agent_id = input.agent_id
      AND event.agent_input_id = input.id
      AND event.id = input.admitted_event_id
      AND event.event_kind = 'agent_input'
    JOIN latest_turn ON latest_turn.turn_id = event.turn_id
    WHERE input.project_id = p_project_id
      AND input.agent_id = p_agent_id
      AND input.input_kind = 'config_change'
      AND input.state = 'resolved'
)
SELECT event.turn_id,
       event.sequence AS config_event_sequence,
       event.created_at AS ready_at
FROM config_events event
WHERE event.sequence >= (
      SELECT boundary.first_content_sequence
      FROM opening_boundary boundary
  )
  AND NOT EXISTS (
      SELECT 1
      FROM model_call_contexts context
      WHERE context.project_id = p_project_id
        AND context.agent_id = p_agent_id
        AND context.operation_kind = 'normal'
        AND context.input_event_sequence >= event.sequence
  )
  AND coalesce((SELECT stop.sequence FROM latest_stop stop), 0) <= event.sequence
ORDER BY event.sequence DESC
LIMIT 1
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION enforce_model_call_context_terminal_outcome()
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
       AND NEW.recovery_kind IS NULL THEN
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

CREATE CONSTRAINT TRIGGER model_call_contexts_terminal_outcome_valid
AFTER UPDATE OF state ON model_call_contexts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_model_call_context_terminal_outcome();

-- +goose StatementBegin
CREATE FUNCTION enforce_model_output_lineage()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    context_kind text;
    context_state text;
    context_recovery_kind text;
BEGIN
    SELECT context.operation_kind, context.state, context.recovery_kind
    INTO context_kind, context_state, context_recovery_kind
    FROM model_call_contexts context
    WHERE context.agent_id = NEW.agent_id
      AND context.id = NEW.model_call_context_id;

    IF NEW.stop_reason = 'error'
       AND NOT (
           context_state = 'failed'
           AND context_recovery_kind IS NULL
       ) THEN
        RAISE EXCEPTION 'error model output must be produced by a stopped model call context'
            USING ERRCODE = '23514';
    ELSIF NEW.stop_reason <> 'error'
       AND NOT (context_kind = 'normal' AND context_state = 'succeeded') THEN
        RAISE EXCEPTION 'provider model output must be produced by a succeeded normal context'
            USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM agent_events event
        WHERE event.agent_id = NEW.agent_id
          AND event.model_output_id = NEW.id
          AND event.event_kind = 'model_output'
    ) THEN
        RAISE EXCEPTION 'model output % must have a typed model_output agent event', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER model_outputs_lineage_valid
AFTER INSERT ON model_outputs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_model_output_lineage();

-- +goose StatementBegin
CREATE FUNCTION enforce_context_checkpoint_lineage()
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
BEGIN
    SELECT context.operation_kind,
           context.state,
           context.source_event_sequence_end,
           context.input_event_sequence
    INTO producer_operation_kind,
         producer_state,
         producer_source_end,
         producer_input_event_sequence
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

    SELECT max(prior.summarized_through_event_sequence)
    INTO prior_summarized_through
    FROM context_checkpoints prior
    JOIN agent_events prior_event ON prior_event.agent_id = prior.agent_id
      AND prior_event.context_checkpoint_id = prior.id
      AND prior_event.event_kind = 'context_checkpoint'
    WHERE prior.agent_id = NEW.agent_id
      AND prior_event.sequence <= producer_input_event_sequence;

    IF prior_summarized_through IS NOT NULL
       AND NEW.summarized_through_event_sequence <= prior_summarized_through THEN
        RAISE EXCEPTION 'checkpoint must advance beyond the applicable prior checkpoint'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER context_checkpoints_lineage_valid
AFTER INSERT ON context_checkpoints
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_context_checkpoint_lineage();

CREATE TABLE tool_calls (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    model_output_id uuid NOT NULL,
    provider_call_id text NOT NULL,
    name text NOT NULL,
    input jsonb NOT NULL,
    type text NOT NULL,
    state text NOT NULL,
    runtime_lock_id uuid,
    created_at timestamptz NOT NULL,
    CHECK (provider_call_id <> ''),
    CHECK (octet_length(provider_call_id) <= 2000),
    CHECK (name <> ''),
    CHECK (jsonb_typeof(input) = 'object'),
    CHECK (type IN ('built_in', 'custom', 'mcp')),
    CHECK (state IN ('awaiting_authorization', 'awaiting_permission', 'ready', 'running', 'waiting', 'completed')),
    CHECK ((state = 'running') = (runtime_lock_id IS NOT NULL)),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_id, model_output_id) REFERENCES model_outputs(agent_id, id),
    FOREIGN KEY (runtime_lock_id) REFERENCES agent_runtime_locks(id),
    UNIQUE (agent_id, id),
    UNIQUE (agent_id, model_output_id, provider_call_id)
);

ALTER TABLE agent_machine_bindings
    ADD FOREIGN KEY (agent_id, create_tool_call_id) REFERENCES tool_calls(agent_id, id),
    ADD FOREIGN KEY (agent_id, delete_tool_call_id) REFERENCES tool_calls(agent_id, id);

CREATE INDEX tool_calls_active_idx
    ON tool_calls(agent_id, state, id)
    WHERE state <> 'completed';

CREATE INDEX tool_calls_runtime_lock_idx
    ON tool_calls(agent_id, runtime_lock_id)
    WHERE state = 'running' AND runtime_lock_id IS NOT NULL;

CREATE INDEX tool_calls_agent_history_idx
    ON tool_calls(agent_id, created_at, id);

CREATE VIEW tool_call_read_projection AS
SELECT tool_call.id,
       context.project_id,
       tool_call.agent_id,
       source_event.turn_id,
       source_event.id AS source_event_id,
       source_event.sequence AS source_event_sequence,
       model_output.model_call_context_id,
       tool_call.model_output_id,
       tool_call.provider_call_id,
       tool_call.name,
       tool_call.input,
       tool_call.type,
       tool_call.state,
       tool_call.runtime_lock_id,
       tool_call.created_at
FROM tool_calls tool_call
JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
  AND model_output.id = tool_call.model_output_id
JOIN model_call_contexts context ON context.agent_id = model_output.agent_id
  AND context.id = model_output.model_call_context_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output';

-- +goose StatementBegin
CREATE FUNCTION prevent_tool_call_fact_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'tool call facts are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tool_call_facts_immutable
BEFORE UPDATE OF
    id, agent_id, model_output_id, provider_call_id,
    name, input, type, created_at
OR DELETE ON tool_calls
FOR EACH ROW EXECUTE FUNCTION prevent_tool_call_fact_mutation();

-- +goose StatementBegin
CREATE FUNCTION enforce_tool_call_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'awaiting_authorization' THEN
            RAISE EXCEPTION 'tool_calls must be inserted in awaiting_authorization state'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.state = 'running' AND NOT EXISTS (
        SELECT 1
        FROM agent_runtime_locks runtime_lock
        WHERE runtime_lock.agent_id = NEW.agent_id
          AND runtime_lock.id = NEW.runtime_lock_id
    ) THEN
        RAISE EXCEPTION 'running tool_call must be owned by its agent runtime lock'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.state IS NOT DISTINCT FROM OLD.state THEN
        IF NEW.runtime_lock_id IS DISTINCT FROM OLD.runtime_lock_id THEN
            RAISE EXCEPTION 'tool_call runtime ownership may change only with state'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.state = 'completed' THEN
        RAISE EXCEPTION 'completed tool_calls are immutable'
            USING ERRCODE = '25006';
    END IF;

    IF NOT (
        (OLD.state = 'awaiting_authorization' AND NEW.state IN ('awaiting_permission', 'ready', 'completed'))
        OR (OLD.state = 'awaiting_permission' AND NEW.state IN ('ready', 'completed'))
        OR (OLD.state = 'ready' AND NEW.state IN ('running', 'waiting', 'completed'))
        OR (OLD.state = 'running' AND NEW.state IN ('ready', 'waiting', 'completed'))
        OR (OLD.state = 'waiting' AND NEW.state = 'completed')
    ) THEN
        RAISE EXCEPTION 'invalid tool_call transition % -> %', OLD.state, NEW.state
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tool_calls_transition_guard
BEFORE INSERT OR UPDATE OF state, runtime_lock_id ON tool_calls
FOR EACH ROW EXECUTE FUNCTION enforce_tool_call_transition();

-- +goose StatementBegin
CREATE FUNCTION agent_tool_work_frontiers(p_project_id uuid, p_agent_id uuid)
RETURNS TABLE (
    turn_id uuid,
    model_call_context_id uuid,
    model_output_id uuid,
    source_event_id uuid,
    ready_at timestamptz,
    frontier_order_created_at timestamptz,
    frontier_order_tool_call_id text
)
LANGUAGE sql
STABLE
AS $$
WITH latest_turn AS MATERIALIZED (
    SELECT agent_latest_turn_id(p_project_id, p_agent_id) AS turn_id
)
SELECT source_event.turn_id,
       model_output.model_call_context_id,
       tool_call.model_output_id,
       source_event.id,
       min(tool_call.created_at) AS ready_at,
       min(tool_call.created_at) AS frontier_order_created_at,
       min(tool_call.id::text) AS frontier_order_tool_call_id
FROM tool_calls tool_call
JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
  AND model_output.id = tool_call.model_output_id
JOIN model_call_contexts context ON context.project_id = p_project_id
  AND context.agent_id = model_output.agent_id
  AND context.id = model_output.model_call_context_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output'
JOIN latest_turn ON latest_turn.turn_id = source_event.turn_id
WHERE tool_call.agent_id = p_agent_id
  AND (
      tool_call.state = 'awaiting_authorization'
      OR (
          tool_call.state = 'ready'
          AND tool_call.type IN ('built_in', 'mcp')
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM agent_stop_events stop_event
      WHERE stop_event.project_id = p_project_id
        AND stop_event.agent_id = tool_call.agent_id
        AND stop_event.sequence > source_event.sequence
  )
GROUP BY tool_call.agent_id,
         source_event.turn_id,
         model_output.model_call_context_id,
         tool_call.model_output_id,
         source_event.id
$$;
-- +goose StatementEnd

CREATE TABLE tool_call_results (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    tool_call_id uuid NOT NULL,
    outcome text NOT NULL,
    completed_at timestamptz NOT NULL,
    CHECK (outcome IN ('succeeded', 'failed', 'denied', 'canceled')),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id),
    UNIQUE (agent_id, id),
    UNIQUE (agent_id, tool_call_id)
);

ALTER TABLE agent_events
    ADD FOREIGN KEY (agent_id, tool_call_result_id) REFERENCES tool_call_results(agent_id, id);

CREATE UNIQUE INDEX agent_events_tool_call_result_idx
    ON agent_events(agent_id, tool_call_result_id)
    WHERE tool_call_result_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION agent_has_incomplete_tool_batch(p_project_id uuid, p_agent_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
WITH latest_turn AS MATERIALIZED (
    SELECT agent_latest_turn_id(p_project_id, p_agent_id) AS turn_id
)
SELECT EXISTS (
    SELECT 1
    FROM tool_calls tool_call
    JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
      AND model_output.id = tool_call.model_output_id
    JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
      AND source_event.model_output_id = tool_call.model_output_id
      AND source_event.event_kind = 'model_output'
    JOIN latest_turn ON latest_turn.turn_id = source_event.turn_id
    WHERE tool_call.agent_id = p_agent_id
      AND tool_call.state <> 'completed'
      AND NOT EXISTS (
          SELECT 1
          FROM agent_stop_events stop_event
          WHERE stop_event.project_id = p_project_id
            AND stop_event.agent_id = tool_call.agent_id
            AND stop_event.sequence > source_event.sequence
      )
)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent_model_result_frontiers(p_project_id uuid, p_agent_id uuid)
RETURNS TABLE (
    turn_id uuid,
    model_call_context_id uuid,
    model_output_id uuid,
    ready_at timestamptz,
    frontier_order_created_at timestamptz,
    frontier_order_tool_call_id text
)
LANGUAGE sql
STABLE
AS $$
WITH latest_turn AS MATERIALIZED (
    SELECT agent_latest_turn_id(p_project_id, p_agent_id) AS turn_id
),
complete_batches AS MATERIALIZED (
    SELECT tool_call.agent_id,
           source_event.turn_id,
           model_output.model_call_context_id,
           tool_call.model_output_id,
           max(result.completed_at) AS ready_at,
           min(tool_call.created_at) AS frontier_order_created_at,
           min(tool_call.id::text) AS frontier_order_tool_call_id,
           max(result_event.sequence)::bigint AS max_result_event_sequence
    FROM tool_calls tool_call
    JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
      AND model_output.id = tool_call.model_output_id
    JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
      AND source_event.model_output_id = tool_call.model_output_id
      AND source_event.event_kind = 'model_output'
    JOIN latest_turn ON latest_turn.turn_id = source_event.turn_id
    LEFT JOIN tool_call_results result ON result.agent_id = tool_call.agent_id
      AND result.tool_call_id = tool_call.id
    LEFT JOIN agent_events result_event ON result_event.agent_id = result.agent_id
      AND result_event.tool_call_result_id = result.id
      AND result_event.event_kind = 'tool_result'
    WHERE tool_call.agent_id = p_agent_id
      AND NOT EXISTS (
          SELECT 1
          FROM agent_stop_events stop_event
          WHERE stop_event.project_id = p_project_id
            AND stop_event.agent_id = tool_call.agent_id
            AND stop_event.sequence > source_event.sequence
      )
    GROUP BY tool_call.agent_id,
             source_event.turn_id,
             model_output.model_call_context_id,
             tool_call.model_output_id
    HAVING count(result.id) = count(tool_call.id)
       AND count(result_event.id) = count(tool_call.id)
)
SELECT batch.turn_id,
       batch.model_call_context_id,
       batch.model_output_id,
       batch.ready_at,
       batch.frontier_order_created_at,
       batch.frontier_order_tool_call_id
FROM complete_batches batch
WHERE NOT EXISTS (
    SELECT 1
    FROM agent_events later_event
    JOIN model_outputs later_output ON later_output.agent_id = later_event.agent_id
      AND later_output.id = later_event.model_output_id
    JOIN model_call_contexts later_context ON later_context.project_id = p_project_id
      AND later_context.agent_id = later_output.agent_id
      AND later_context.id = later_output.model_call_context_id
    WHERE later_event.agent_id = batch.agent_id
      AND later_event.turn_id = batch.turn_id
      AND later_event.event_kind = 'model_output'
      AND later_event.sequence > batch.max_result_event_sequence
      AND later_context.input_event_sequence >= batch.max_result_event_sequence
)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION agent_next_model_work(p_project_id uuid, p_agent_id uuid)
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
    SELECT 'start', NULL::uuid, NULL::uuid, turn_id,
           opening_watermark, ready_at, 3, 2
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
CREATE FUNCTION enforce_agent_event_payload_turn()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    expected_turn_id uuid;
BEGIN
    IF NEW.event_kind = 'model_output' THEN
        SELECT context_turn.turn_id
        INTO expected_turn_id
        FROM model_outputs output
        JOIN model_call_context_turns context_turn ON context_turn.agent_id = output.agent_id
          AND context_turn.model_call_context_id = output.model_call_context_id
        WHERE output.agent_id = NEW.agent_id
          AND output.id = NEW.model_output_id;

        IF expected_turn_id IS NULL OR expected_turn_id IS DISTINCT FROM NEW.turn_id THEN
            RAISE EXCEPTION 'model_output event % must use turn % derived from model context input sequence', NEW.id, expected_turn_id
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.event_kind = 'tool_result' THEN
        SELECT source_event.turn_id
        INTO expected_turn_id
        FROM tool_call_results result
        JOIN tool_calls tool_call ON tool_call.agent_id = result.agent_id
          AND tool_call.id = result.tool_call_id
        JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
          AND source_event.model_output_id = tool_call.model_output_id
          AND source_event.event_kind = 'model_output'
        WHERE result.agent_id = NEW.agent_id
          AND result.id = NEW.tool_call_result_id;

        IF expected_turn_id IS NULL OR expected_turn_id IS DISTINCT FROM NEW.turn_id THEN
            RAISE EXCEPTION 'tool_result event % must use turn % derived from tool call source event', NEW.id, expected_turn_id
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.event_kind = 'context_checkpoint' THEN
        SELECT context_turn.turn_id
        INTO expected_turn_id
        FROM context_checkpoints checkpoint
        JOIN model_call_context_turns context_turn ON context_turn.agent_id = checkpoint.agent_id
          AND context_turn.model_call_context_id = checkpoint.producer_model_call_context_id
        WHERE checkpoint.agent_id = NEW.agent_id
          AND checkpoint.id = NEW.context_checkpoint_id;

        IF expected_turn_id IS NULL OR expected_turn_id IS DISTINCT FROM NEW.turn_id THEN
            RAISE EXCEPTION 'context_checkpoint event % must use turn % derived from producer context', NEW.id, expected_turn_id
                USING ERRCODE = '23514';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_events_payload_turn
BEFORE INSERT ON agent_events
FOR EACH ROW EXECUTE FUNCTION enforce_agent_event_payload_turn();

-- +goose StatementBegin
CREATE FUNCTION enforce_tool_call_result_typed_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM agent_events event
        WHERE event.agent_id = NEW.agent_id
          AND event.tool_call_result_id = NEW.id
          AND event.event_kind = 'tool_result'
    ) THEN
        RAISE EXCEPTION 'tool call result % must have a typed tool_result agent event', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER tool_call_results_typed_event_required
AFTER INSERT ON tool_call_results
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_tool_call_result_typed_event();

-- +goose StatementBegin
CREATE FUNCTION enforce_tool_call_result()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    has_result boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM tool_call_results result
        WHERE result.agent_id = NEW.agent_id
          AND result.tool_call_id = NEW.id
    ) INTO has_result;

    IF (NEW.state = 'completed') IS DISTINCT FROM has_result THEN
        RAISE EXCEPTION 'tool call % completion state must match result existence', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER tool_call_result_required
AFTER INSERT OR UPDATE OF state ON tool_calls
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_tool_call_result();

-- +goose StatementBegin
CREATE FUNCTION enforce_tool_result_completed_call()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    tool_call_state text;
BEGIN
    SELECT tool_call.state
    INTO tool_call_state
    FROM tool_calls tool_call
    WHERE tool_call.agent_id = NEW.agent_id
      AND tool_call.id = NEW.tool_call_id;

    IF tool_call_state IS DISTINCT FROM 'completed' THEN
        RAISE EXCEPTION 'tool result % requires a completed tool call', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER tool_result_completed_call_required
AFTER INSERT ON tool_call_results
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_tool_result_completed_call();

-- +goose StatementBegin
CREATE FUNCTION prevent_tool_call_result_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'tool_call_results are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tool_call_results_immutable
BEFORE UPDATE OR DELETE ON tool_call_results
FOR EACH ROW EXECUTE FUNCTION prevent_tool_call_result_mutation();

CREATE TABLE agent_interactions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    tool_call_id uuid NOT NULL,
    interaction_kind text NOT NULL,
    state text NOT NULL,
    request jsonb NOT NULL DEFAULT '{}'::jsonb,
    resolution jsonb NOT NULL DEFAULT '{}'::jsonb,
    resolved_by_input_id uuid,
    created_at timestamptz NOT NULL,
    resolved_at timestamptz,
    CHECK (interaction_kind IN ('permission', 'question')),
    CHECK (state IN ('open', 'resolved', 'canceled')),
    CHECK (state <> 'open' OR (resolved_at IS NULL AND resolved_by_input_id IS NULL)),
    CHECK (state = 'open' OR resolved_at IS NOT NULL),
    CHECK (jsonb_typeof(request) = 'object'),
    CHECK (jsonb_typeof(resolution) = 'object'),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_id, resolved_by_input_id) REFERENCES agent_inputs(agent_id, id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id),
    UNIQUE (agent_id, id)
);

CREATE INDEX agent_interactions_open_idx
    ON agent_interactions(agent_id, created_at, id)
    WHERE state = 'open';

CREATE INDEX agent_interactions_agent_history_idx
    ON agent_interactions(agent_id, created_at, id);

CREATE UNIQUE INDEX agent_interactions_tool_call_kind_idx
    ON agent_interactions(agent_id, tool_call_id, interaction_kind);

CREATE VIEW agent_interaction_read_projection AS
SELECT interaction.id,
       tool_call.project_id,
       interaction.agent_id,
       tool_call.turn_id,
       tool_call.model_call_context_id,
       interaction.tool_call_id,
       tool_call.provider_call_id,
       interaction.interaction_kind,
       interaction.state,
       interaction.request,
       interaction.resolution,
       interaction.resolved_by_input_id,
       interaction.created_at,
       interaction.resolved_at
FROM agent_interactions interaction
JOIN tool_call_read_projection tool_call ON tool_call.agent_id = interaction.agent_id
  AND tool_call.id = interaction.tool_call_id;

-- +goose StatementBegin
CREATE FUNCTION enforce_agent_interaction_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent_interactions are immutable'
            USING ERRCODE = '25006';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'open' THEN
            RAISE EXCEPTION 'agent_interactions must be inserted in open state'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.state <> 'open' THEN
        RAISE EXCEPTION 'terminal agent_interaction is immutable'
            USING ERRCODE = '25006';
    END IF;
    IF NEW.state NOT IN ('resolved', 'canceled') THEN
        RAISE EXCEPTION 'agent_interaction must transition from open to a terminal state'
            USING ERRCODE = '25006';
    END IF;
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.agent_id IS DISTINCT FROM NEW.agent_id
       OR OLD.tool_call_id IS DISTINCT FROM NEW.tool_call_id
       OR OLD.interaction_kind IS DISTINCT FROM NEW.interaction_kind
       OR OLD.request IS DISTINCT FROM NEW.request
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'agent_interaction lineage is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_interactions_transition_guard
BEFORE INSERT OR UPDATE OR DELETE ON agent_interactions
FOR EACH ROW EXECUTE FUNCTION enforce_agent_interaction_transition();

-- +goose StatementBegin
CREATE FUNCTION agent_next_wakeup_ready_at(
    p_project_id uuid,
    p_agent_id uuid
)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    model_ready_at timestamptz;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_tool_work_frontiers(p_project_id, p_agent_id)
    ) THEN
        RETURN statement_timestamp();
    END IF;

    IF agent_has_incomplete_tool_batch(p_project_id, p_agent_id) THEN
        RETURN NULL;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM agent_inputs input
        WHERE input.project_id = p_project_id
          AND input.agent_id = p_agent_id
          AND input.state = 'received'
          AND input.delivery_mode = 'steering'
          AND input.input_kind = 'content'
    ) THEN
        RETURN statement_timestamp();
    END IF;

    SELECT frontier.ready_at
    INTO model_ready_at
    FROM agent_next_model_work(p_project_id, p_agent_id) frontier
    LIMIT 1;

    IF model_ready_at IS NOT NULL THEN
        RETURN model_ready_at;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM agent_inputs input
        WHERE input.project_id = p_project_id
          AND input.agent_id = p_agent_id
          AND input.state = 'received'
          AND input.delivery_mode = 'queued'
          AND input.input_kind = 'content'
    ) THEN
        RETURN statement_timestamp();
    END IF;

    RETURN NULL;
END;
$$;
-- +goose StatementEnd

ALTER TABLE agent_inputs
    ADD FOREIGN KEY (agent_id, target_interaction_id) REFERENCES agent_interactions(agent_id, id);

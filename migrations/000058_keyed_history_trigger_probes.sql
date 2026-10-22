-- +goose Up

CREATE INDEX context_checkpoints_agent_frontier_idx
    ON context_checkpoints(agent_id, summarized_through_event_sequence DESC) INCLUDE (id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_execution_opening_members() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    member_id uuid;
    admitted_id uuid;
    member_sequence bigint;
    previous_sequence bigint;
BEGIN
    FOREACH member_id IN ARRAY coalesce(NEW.opening_input_ids, '{}'::uuid[]) LOOP
        SELECT admitted_event_id INTO admitted_id
        FROM agent_inputs
        WHERE agent_id = NEW.agent_id AND id = member_id
          AND input_kind = 'content' AND state = 'resolved';

        SELECT sequence INTO member_sequence
        FROM agent_events
        WHERE agent_id = NEW.agent_id AND id = admitted_id AND is_opening_event;

        IF member_sequence IS NULL OR member_sequence <= previous_sequence
           OR (previous_sequence IS NULL AND member_sequence <> NEW.opening_event_sequence) THEN
            RAISE EXCEPTION 'opening inputs must belong to the agent in event sequence order' USING ERRCODE='23514';
        END IF;
        previous_sequence := member_sequence;
    END LOOP;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_event_payload_turn()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    expected_turn_id uuid;
    context_id uuid;
    call_id uuid;
    output_id uuid;
BEGIN
    IF NEW.event_kind = 'model_output' THEN
        SELECT model_call_context_id INTO context_id
        FROM model_outputs WHERE agent_id = NEW.agent_id AND id = NEW.model_output_id;
        SELECT turn_id INTO expected_turn_id
        FROM model_call_contexts WHERE agent_id = NEW.agent_id AND id = context_id;

        IF expected_turn_id IS NULL OR expected_turn_id IS DISTINCT FROM NEW.turn_id THEN
            RAISE EXCEPTION 'model_output event % must use turn % captured by model context', NEW.id, expected_turn_id
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.event_kind = 'tool_result' THEN
        SELECT tool_call_id INTO call_id
        FROM tool_call_results WHERE agent_id = NEW.agent_id AND id = NEW.tool_call_result_id;
        SELECT model_output_id INTO output_id
        FROM tool_calls WHERE agent_id = NEW.agent_id AND id = call_id;
        SELECT turn_id INTO expected_turn_id
        FROM agent_events
        WHERE agent_id = NEW.agent_id AND model_output_id = output_id AND event_kind = 'model_output';

        IF expected_turn_id IS NULL OR expected_turn_id IS DISTINCT FROM NEW.turn_id THEN
            RAISE EXCEPTION 'tool_result event % must use turn % derived from tool call source event', NEW.id, expected_turn_id
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.event_kind = 'context_checkpoint' THEN
        SELECT producer_model_call_context_id INTO context_id
        FROM context_checkpoints WHERE agent_id = NEW.agent_id AND id = NEW.context_checkpoint_id;
        SELECT turn_id INTO expected_turn_id
        FROM model_call_contexts WHERE agent_id = NEW.agent_id AND id = context_id;

        IF expected_turn_id IS NULL OR expected_turn_id IS DISTINCT FROM NEW.turn_id THEN
            RAISE EXCEPTION 'context_checkpoint event % must use turn % derived from producer context', NEW.id, expected_turn_id
                USING ERRCODE = '23514';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_open_model_output_content_block_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_context_state text;
    owner_context_id uuid;
    owner_context_recovery_kind text;
    owner_output_stop_reason text;
BEGIN
    IF NEW.owner_kind <> 'model_output' THEN
        RETURN NEW;
    END IF;

    SELECT model_call_context_id, stop_reason INTO owner_context_id, owner_output_stop_reason
    FROM model_outputs
    WHERE agent_id = NEW.agent_id AND id = NEW.owner_model_output_id;

    SELECT state, recovery_kind INTO owner_context_state, owner_context_recovery_kind
    FROM model_call_contexts
    WHERE agent_id = NEW.agent_id AND id = owner_context_id
    FOR SHARE;

    IF owner_context_state = 'started' THEN
        RETURN NEW;
    END IF;

    IF NEW.block_kind = 'error'
       AND NEW.ordinal = 0
       AND owner_output_stop_reason = 'error'
       AND owner_context_state = 'failed'
       AND owner_context_recovery_kind IS NULL
       AND NOT EXISTS (
         SELECT 1
         FROM content_blocks existing
         WHERE existing.agent_id = NEW.agent_id
           AND existing.owner_model_output_id = NEW.owner_model_output_id
       ) THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'model-output content blocks require a writable model output'
        USING ERRCODE = '23514';
END;
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

    SELECT prior.summarized_through_event_sequence
    INTO prior_summarized_through
    FROM context_checkpoints prior
    CROSS JOIN LATERAL (
        SELECT event.sequence
        FROM agent_events event
        WHERE event.agent_id = NEW.agent_id AND event.context_checkpoint_id = prior.id
          AND event.event_kind = 'context_checkpoint'
        LIMIT 1
    ) prior_event
    WHERE prior.agent_id = NEW.agent_id
      AND prior.summarized_through_event_sequence < producer_input_event_sequence
      AND prior_event.sequence <= producer_input_event_sequence
    ORDER BY prior.summarized_through_event_sequence DESC
    LIMIT 1;

    IF prior_summarized_through IS NOT NULL
       AND NEW.summarized_through_event_sequence <= prior_summarized_through THEN
        RAISE EXCEPTION 'checkpoint must advance beyond the applicable prior checkpoint'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

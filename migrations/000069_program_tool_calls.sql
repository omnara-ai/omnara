-- +goose Up

-- Programmatic tool calls: a shell process started by run_command calls other
-- tools through `omnarad call`. Each nested call is a child tool_calls row that
-- shares the owning run_command call's model output, has no tool_call content
-- block or agent events, never holds the runtime lock, and stays outside the
-- model context. Workers execute children under a claim lease instead.
ALTER TABLE tool_calls
    ADD COLUMN caller text NOT NULL DEFAULT 'model',
    ADD COLUMN parent_tool_call_id uuid,
    ADD COLUMN program_key text,
    ADD COLUMN program_claim_id uuid,
    ADD COLUMN program_claim_expires_at timestamptz,
    ADD CONSTRAINT tool_calls_caller_check CHECK (caller IN ('model', 'program')),
    -- Authorization leases are re-claimable after expiry; execution leases
    -- (waiting) are not, so a child body runs at most once.
    ADD CONSTRAINT tool_calls_program_shape CHECK (
        (caller = 'model' AND parent_tool_call_id IS NULL AND program_key IS NULL
            AND program_claim_id IS NULL AND program_claim_expires_at IS NULL)
        OR (caller = 'program' AND parent_tool_call_id IS NOT NULL AND program_key IS NOT NULL
            AND program_key <> '' AND octet_length(program_key) <= 200
            AND state <> 'running'
            AND (program_claim_id IS NULL) = (program_claim_expires_at IS NULL)
            AND (program_claim_id IS NOT NULL OR state <> 'waiting')
            AND (program_claim_id IS NULL OR state IN ('awaiting_authorization', 'waiting')))
    ),
    ADD CONSTRAINT tool_calls_agent_id_id_model_output_id_key UNIQUE (agent_id, id, model_output_id),
    ADD CONSTRAINT tool_calls_program_key_key UNIQUE (agent_id, parent_tool_call_id, program_key),
    ADD CONSTRAINT tool_calls_program_process_fkey
        FOREIGN KEY (agent_id, parent_tool_call_id) REFERENCES processes(agent_id, tool_call_id),
    ADD CONSTRAINT tool_calls_program_parent_fkey
        FOREIGN KEY (agent_id, parent_tool_call_id, model_output_id)
        REFERENCES tool_calls(agent_id, id, model_output_id);

CREATE INDEX tool_calls_program_open_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND state <> 'completed';

-- +goose StatementBegin
CREATE FUNCTION enforce_program_tool_call_parent()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM tool_calls parent
        WHERE parent.agent_id = NEW.agent_id AND parent.id = NEW.parent_tool_call_id
          AND parent.caller = 'model'
    ) THEN
        RAISE EXCEPTION 'program tool call % requires a model-called parent', NEW.id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tool_calls_program_parent_valid
BEFORE INSERT ON tool_calls
FOR EACH ROW WHEN (NEW.caller = 'program')
EXECUTE FUNCTION enforce_program_tool_call_parent();

DROP TRIGGER tool_call_facts_immutable ON tool_calls;
CREATE TRIGGER tool_call_facts_immutable
BEFORE UPDATE OF
    id, agent_id, model_output_id, provider_call_id,
    name, input, type, created_at, caller, parent_tool_call_id, program_key
OR DELETE ON tool_calls
FOR EACH ROW EXECUTE FUNCTION prevent_tool_call_fact_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_tool_call_content_block()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.caller = 'program' THEN
        IF EXISTS (
            SELECT 1 FROM content_blocks block
            WHERE block.agent_id = NEW.agent_id AND block.tool_call_id = NEW.id
        ) THEN
            RAISE EXCEPTION 'program tool call % must not have a model-output content block', NEW.id
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM content_blocks block
        WHERE block.agent_id = NEW.agent_id
          AND block.owner_kind = 'model_output'
          AND block.owner_model_output_id = NEW.model_output_id
          AND block.block_kind = 'tool_call'
          AND block.tool_call_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'tool call % must have exactly one matching model-output content block', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_tool_call_result_typed_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    has_event boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM agent_events event
        WHERE event.agent_id = NEW.agent_id
          AND event.tool_call_result_id = NEW.id
          AND event.event_kind = 'tool_result'
    ) INTO has_event;

    IF EXISTS (
        SELECT 1 FROM tool_calls call
        WHERE call.agent_id = NEW.agent_id AND call.id = NEW.tool_call_id AND call.caller = 'program'
    ) THEN
        IF has_event THEN
            RAISE EXCEPTION 'program tool call result % must not have an agent event', NEW.id
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NOT has_event THEN
        RAISE EXCEPTION 'tool call result % must have a typed tool_result agent event', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE VIEW tool_call_read_projection AS
SELECT tool_call.id,
       agent.project_id,
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
       tool_call.created_at,
       tool_call.caller,
       tool_call.parent_tool_call_id
FROM tool_calls tool_call
JOIN agents agent ON agent.id = tool_call.agent_id
JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
  AND model_output.id = tool_call.model_output_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output';

-- Program-call interactions are resolved directly by an actor, without an
-- interaction_response agent input; replays compare the recorded actor.
ALTER TABLE agent_interactions
    ADD COLUMN resolved_by_actor_id uuid REFERENCES actors(id),
    ADD CONSTRAINT agent_interactions_resolved_by_actor_check CHECK (
        resolved_by_actor_id IS NULL OR (state = 'resolved' AND resolved_by_input_id IS NULL)
    );

ALTER TABLE machine_pools
    ADD COLUMN default_programmatic_tool_calls boolean NOT NULL DEFAULT false;

ALTER TABLE machines
    ADD COLUMN programmatic_tool_calls boolean NOT NULL DEFAULT false;

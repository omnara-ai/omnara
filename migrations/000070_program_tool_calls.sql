-- +goose NO TRANSACTION
-- +goose Up

ALTER TABLE tool_calls
    ALTER COLUMN model_output_id DROP NOT NULL,
    ADD COLUMN caller text NOT NULL DEFAULT 'model',
    ADD COLUMN parent_tool_call_id uuid,
    ADD COLUMN program_claim_id uuid,
    ADD COLUMN program_claim_expires_at timestamptz,
    ADD CONSTRAINT tool_calls_caller_shape CHECK (
        (caller = 'model' AND model_output_id IS NOT NULL AND parent_tool_call_id IS NULL
            AND program_claim_id IS NULL AND program_claim_expires_at IS NULL)
        OR (caller = 'program' AND model_output_id IS NULL AND parent_tool_call_id IS NOT NULL
            AND state <> 'running'
            AND (program_claim_id IS NULL) = (program_claim_expires_at IS NULL)
            AND (program_claim_id IS NOT NULL OR state <> 'waiting')
            AND (program_claim_id IS NULL OR state IN ('awaiting_authorization', 'waiting')))
    ) NOT VALID,
    ADD CONSTRAINT tool_calls_program_process_fkey
        FOREIGN KEY (agent_id, parent_tool_call_id) REFERENCES processes(agent_id, tool_call_id) NOT VALID;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_tool_call_transition()
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
        OR (OLD.state = 'waiting' AND NEW.state = 'ready' AND NEW.caller = 'program')
    ) THEN
        RAISE EXCEPTION 'invalid tool_call transition % -> %', OLD.state, NEW.state
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

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
    id, project_id, agent_id, model_output_id, provider_call_id,
    name, input, type, created_at, caller, parent_tool_call_id
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
       tool_call.project_id,
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
LEFT JOIN LATERAL (
    SELECT parent.model_output_id FROM tool_calls parent
    WHERE tool_call.parent_tool_call_id IS NOT NULL
      AND parent.agent_id = tool_call.agent_id AND parent.id = tool_call.parent_tool_call_id
    LIMIT 1
) parent ON true
JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
  AND model_output.id = coalesce(tool_call.model_output_id, parent.model_output_id)
JOIN agent_events source_event ON source_event.agent_id = model_output.agent_id
  AND source_event.model_output_id = model_output.id
  AND source_event.event_kind = 'model_output';

ALTER TABLE agent_interactions
    ADD COLUMN resolved_by_actor_id uuid,
    ADD CONSTRAINT agent_interactions_resolved_by_actor_id_fkey
        FOREIGN KEY (resolved_by_actor_id) REFERENCES actors(id) NOT VALID,
    ADD CONSTRAINT agent_interactions_resolved_by_actor_check CHECK (
        resolved_by_actor_id IS NULL OR (state = 'resolved' AND resolved_by_input_id IS NULL)
    ) NOT VALID;

ALTER TABLE machine_pools
    ADD COLUMN default_programmatic_tool_calls boolean NOT NULL DEFAULT false;

ALTER TABLE machines
    ADD COLUMN programmatic_tool_calls boolean NOT NULL DEFAULT false;

CREATE UNIQUE INDEX CONCURRENTLY tool_calls_program_key_idx
    ON tool_calls(agent_id, parent_tool_call_id, provider_call_id) WHERE caller = 'program';

CREATE INDEX CONCURRENTLY tool_calls_program_open_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND state <> 'completed';

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_caller_shape;

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_program_process_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_id_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_check;

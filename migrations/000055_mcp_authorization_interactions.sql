-- +goose Up

-- An MCP tool call the server rejected for missing OAuth authorization waits on
-- an authorization interaction, then returns to ready once access is granted.
ALTER TABLE agent_interactions
    DROP CONSTRAINT agent_interactions_interaction_kind_check,
    ADD CONSTRAINT agent_interactions_interaction_kind_check
        CHECK (interaction_kind IN ('permission', 'question', 'authorization'));

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
        OR (OLD.state = 'waiting' AND NEW.state = 'ready' AND NEW.type = 'mcp' AND EXISTS (
            SELECT 1
            FROM agent_interactions interaction
            WHERE interaction.agent_id = NEW.agent_id
              AND interaction.tool_call_id = NEW.id
              AND interaction.interaction_kind = 'authorization'
              AND interaction.state = 'resolved'
        ))
    ) THEN
        RAISE EXCEPTION 'invalid tool_call transition % -> %', OLD.state, NEW.state
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

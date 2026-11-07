-- +goose Up

UPDATE tool_calls tool_call
SET project_id = agent.project_id
FROM agents agent
WHERE agent.id = tool_call.agent_id
  AND tool_call.state = 'completed';

LOCK TABLE agents IN EXCLUSIVE MODE;
LOCK TABLE tool_calls, agent_interactions IN ACCESS EXCLUSIVE MODE;

UPDATE tool_calls tool_call
SET project_id = agent.project_id
FROM agents agent
WHERE agent.id = tool_call.agent_id
  AND tool_call.project_id IS NULL;

ALTER TABLE agent_interactions DISABLE TRIGGER agent_interactions_transition_guard;

UPDATE agent_interactions interaction
SET project_id = agent.project_id
FROM agents agent
WHERE agent.id = interaction.agent_id;

ALTER TABLE agent_interactions ENABLE TRIGGER agent_interactions_transition_guard;

ALTER TABLE tool_calls
    ALTER COLUMN project_id SET NOT NULL,
    ADD FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id);

ALTER TABLE agent_interactions
    ALTER COLUMN project_id SET NOT NULL,
    ADD FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id);

DROP TRIGGER tool_call_facts_immutable ON tool_calls;

CREATE TRIGGER tool_call_facts_immutable
BEFORE UPDATE OF
    id, project_id, agent_id, model_output_id, provider_call_id,
    name, input, type, created_at
OR DELETE ON tool_calls
FOR EACH ROW EXECUTE FUNCTION prevent_tool_call_fact_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_interaction_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent_interactions are immutable'
            USING ERRCODE = '25006';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'open' OR NEW.presentation_receipt IS NOT NULL THEN
            RAISE EXCEPTION 'agent_interactions must be inserted in open state'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    -- Record late confirmed sends after cancellation so cleanup can dismiss them.
    IF OLD.presentation_receipt IS NULL AND NEW.presentation_receipt IS NOT NULL
       AND NEW.destination IS NOT NULL
       AND (to_jsonb(OLD) - 'presentation_receipt') = (to_jsonb(NEW) - 'presentation_receipt') THEN
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
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.agent_id IS DISTINCT FROM NEW.agent_id
       OR OLD.tool_call_id IS DISTINCT FROM NEW.tool_call_id
       OR OLD.interaction_kind IS DISTINCT FROM NEW.interaction_kind
       OR OLD.request IS DISTINCT FROM NEW.request
       OR OLD.created_at IS DISTINCT FROM NEW.created_at
       OR OLD.destination IS DISTINCT FROM NEW.destination
       OR OLD.presentation_receipt IS DISTINCT FROM NEW.presentation_receipt THEN
        RAISE EXCEPTION 'agent_interaction lineage is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE INDEX tool_calls_project_unfinished_idx
    ON tool_calls(project_id, created_at, id)
    WHERE state <> 'completed';

CREATE INDEX agent_interactions_project_open_idx
    ON agent_interactions(project_id, created_at, id)
    WHERE state = 'open';

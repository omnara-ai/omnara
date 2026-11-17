-- +goose Up

-- source records the surface that sent a content input, for usage analytics.
-- It is null for other input kinds and for inputs received before it was recorded.
ALTER TABLE agent_inputs
    ADD COLUMN source text,
    ADD CONSTRAINT agent_inputs_source CHECK (
        source IS NULL OR (
            input_kind = 'content'
            AND source IN ('web', 'api', 'mcp', 'integration', 'cron', 'subagent', 'process')
        )
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_input_mutation_policy()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent_inputs are immutable' USING ERRCODE = '25006';
    END IF;
    IF OLD.state <> 'received' AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal agent_inputs are immutable' USING ERRCODE = '25006';
    END IF;
    IF (NEW.delivery_mode IS DISTINCT FROM OLD.delivery_mode
        OR NEW.input_rank IS DISTINCT FROM OLD.input_rank)
       AND NOT (OLD.state = 'received' AND NEW.state = 'received' AND OLD.input_kind = 'content') THEN
        RAISE EXCEPTION 'agent_input delivery may change only while content remains received'
            USING ERRCODE = '25006';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.agent_id IS DISTINCT FROM OLD.agent_id
       OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
       OR NEW.input_kind IS DISTINCT FROM OLD.input_kind
       OR NEW.integration_target_id IS DISTINCT FROM OLD.integration_target_id
       OR NEW.control_type IS DISTINCT FROM OLD.control_type
       OR NEW.target_interaction_id IS DISTINCT FROM OLD.target_interaction_id
       OR NEW.agent_config_id IS DISTINCT FROM OLD.agent_config_id
       OR NEW.idempotency_scope IS DISTINCT FROM OLD.idempotency_scope
       OR NEW.input_idempotency_key IS DISTINCT FROM OLD.input_idempotency_key
       OR NEW.queued_at IS DISTINCT FROM OLD.queued_at
       OR NEW.metadata IS DISTINCT FROM OLD.metadata
       OR NEW.source IS DISTINCT FROM OLD.source THEN
        RAISE EXCEPTION 'agent_input intent and identity are immutable' USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

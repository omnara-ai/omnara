-- +goose Up

-- A scheduled content input is held outside the execution backlog until its
-- scheduled_for time, when the schedule loop releases it to 'received'.
ALTER TABLE agent_inputs
    ADD COLUMN scheduled_for timestamptz,
    DROP CONSTRAINT agent_inputs_state_check,
    DROP CONSTRAINT agent_inputs_lifecycle,
    ADD CONSTRAINT agent_inputs_state_check
        CHECK (state IN ('scheduled', 'received', 'resolved', 'canceled')) NOT VALID,
    ADD CONSTRAINT agent_inputs_lifecycle CHECK (
        (state IN ('scheduled', 'received') AND admitted_event_id IS NULL AND admitted_at IS NULL
            AND resolved_at IS NULL AND canceled_at IS NULL)
        OR (state = 'resolved' AND admitted_event_id IS NOT NULL AND admitted_at IS NOT NULL
            AND resolved_at IS NOT NULL AND canceled_at IS NULL)
        OR (state = 'canceled' AND admitted_event_id IS NULL AND admitted_at IS NULL
            AND resolved_at IS NULL AND canceled_at IS NOT NULL)
    ) NOT VALID,
    ADD CONSTRAINT agent_inputs_scheduled_shape CHECK (
        (scheduled_for IS NULL OR input_kind = 'content')
        AND (state <> 'scheduled' OR scheduled_for IS NOT NULL)
    ) NOT VALID;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_agent_input_mutation_policy()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent_inputs are immutable' USING ERRCODE = '25006';
    END IF;
    IF OLD.state NOT IN ('scheduled', 'received') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal agent_inputs are immutable' USING ERRCODE = '25006';
    END IF;
    IF OLD.state = 'received' AND NEW.state = 'scheduled' THEN
        RAISE EXCEPTION 'received agent_inputs cannot be rescheduled' USING ERRCODE = '25006';
    END IF;
    IF (NEW.delivery_mode IS DISTINCT FROM OLD.delivery_mode
        OR NEW.input_rank IS DISTINCT FROM OLD.input_rank)
       AND NOT (OLD.state IN ('scheduled', 'received') AND NEW.state = 'received'
                AND OLD.input_kind = 'content') THEN
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
       OR NEW.scheduled_for IS DISTINCT FROM OLD.scheduled_for
       OR NEW.metadata IS DISTINCT FROM OLD.metadata THEN
        RAISE EXCEPTION 'agent_input intent and identity are immutable' USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_content_block_agent_input_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_input_kind text;
    owner_control_type text;
    owner_state text;
BEGIN
    IF NEW.owner_kind <> 'agent_input' THEN
        RETURN NEW;
    END IF;

    SELECT input_kind, control_type, state
    INTO owner_input_kind, owner_control_type, owner_state
    FROM agent_inputs
    WHERE agent_id = NEW.agent_id
      AND id = NEW.owner_agent_input_id
    FOR SHARE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'content block references missing agent input %', NEW.owner_agent_input_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;

    IF owner_input_kind <> 'content' OR owner_control_type IS NOT NULL
       OR owner_state NOT IN ('scheduled', 'received') THEN
        RAISE EXCEPTION 'content blocks may only be owned by content agent inputs'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

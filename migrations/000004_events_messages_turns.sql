-- +goose Up

-- Agent events, inputs, wakeups, workers, turns, and runtime locks.
CREATE TABLE agent_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    turn_id uuid NOT NULL,
    sequence bigint NOT NULL,
    event_kind text NOT NULL,
    idempotency_key text,
    agent_input_id uuid,
    model_output_id uuid,
    tool_call_result_id uuid,
    context_checkpoint_id uuid,
    is_opening_event boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    CHECK (sequence > 0),
    CHECK (event_kind IN (
        'agent_input',
        'model_output',
        'tool_result',
        'context_checkpoint'
    )),
    CHECK (
        (
            event_kind = 'agent_input'
            AND agent_input_id IS NOT NULL
            AND model_output_id IS NULL
            AND tool_call_result_id IS NULL
            AND context_checkpoint_id IS NULL
        )
        OR (
            event_kind = 'model_output'
            AND agent_input_id IS NULL
            AND model_output_id IS NOT NULL
            AND tool_call_result_id IS NULL
            AND context_checkpoint_id IS NULL
        )
        OR (
            event_kind = 'tool_result'
            AND agent_input_id IS NULL
            AND model_output_id IS NULL
            AND tool_call_result_id IS NOT NULL
            AND context_checkpoint_id IS NULL
        )
        OR (
            event_kind = 'context_checkpoint'
            AND agent_input_id IS NULL
            AND model_output_id IS NULL
            AND tool_call_result_id IS NULL
            AND context_checkpoint_id IS NOT NULL
        )
    ),
    CHECK (NOT is_opening_event OR event_kind = 'agent_input'),
    UNIQUE (agent_id, sequence),
    UNIQUE (agent_id, id),
    UNIQUE (agent_id, turn_id, id),
    UNIQUE (agent_id, idempotency_key),
    FOREIGN KEY (agent_id) REFERENCES agents(id)
);

CREATE INDEX agent_events_turn_sequence_idx
    ON agent_events(agent_id, turn_id, sequence);

CREATE INDEX agent_events_opening_idx
    ON agent_events(agent_id, turn_id, sequence)
    WHERE is_opening_event;

CREATE INDEX agent_events_opening_sequence_idx
    ON agent_events(agent_id, sequence DESC)
    WHERE is_opening_event;

-- +goose StatementBegin
CREATE FUNCTION reject_agent_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'agent_events are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_events_immutable
    BEFORE UPDATE OR DELETE
    ON agent_events
    FOR EACH ROW
    EXECUTE FUNCTION reject_agent_event_mutation();

CREATE TABLE agent_inputs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    state text NOT NULL,
    input_rank bigint NOT NULL DEFAULT 1,
    actor_id uuid,
    input_kind text NOT NULL DEFAULT 'content',
    delivery_mode text NOT NULL DEFAULT 'queued',
    control_type text,
    target_interaction_id uuid,
    agent_config_id uuid,
    integration_target_id uuid,
    idempotency_scope text,
    input_idempotency_key text,
    queued_at timestamptz NOT NULL,
    admitted_event_id uuid,
    admitted_at timestamptz,
    resolved_at timestamptz,
    canceled_at timestamptz,
    rejected_reason text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    CHECK (state IN ('received', 'resolved', 'rejected', 'canceled')),
    CHECK (delivery_mode IN ('queued', 'steering', 'immediate')),
    CHECK (control_type IS NULL OR control_type = 'cancel_current'),
    CHECK (input_rank > 0),
    CHECK (input_kind <> ''),
    CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (
        (
            input_kind = 'content'
            AND delivery_mode IN ('queued', 'steering')
            AND control_type IS NULL
            AND target_interaction_id IS NULL
            AND agent_config_id IS NULL
        )
        OR (
            input_kind = 'control'
            AND delivery_mode = 'immediate'
            AND control_type IS NOT NULL
            AND target_interaction_id IS NULL
            AND agent_config_id IS NULL
            AND integration_target_id IS NULL
        )
        OR (
            input_kind = 'interaction_response'
            AND delivery_mode = 'immediate'
            AND control_type IS NULL
            AND target_interaction_id IS NOT NULL
            AND agent_config_id IS NULL
        )
        OR (
            input_kind = 'config_change'
            AND delivery_mode = 'immediate'
            AND control_type IS NULL
            AND target_interaction_id IS NULL
            AND agent_config_id IS NOT NULL
            AND integration_target_id IS NULL
        )
    ),
    CHECK (
        (
            state = 'received'
            AND admitted_event_id IS NULL
            AND admitted_at IS NULL
            AND resolved_at IS NULL
            AND canceled_at IS NULL
            AND rejected_reason IS NULL
        )
        OR (
            state = 'resolved'
            AND canceled_at IS NULL
            AND rejected_reason IS NULL
            AND resolved_at IS NOT NULL
            AND input_kind IN ('content', 'control', 'config_change', 'interaction_response')
            AND admitted_event_id IS NOT NULL
            AND admitted_at IS NOT NULL
        )
        OR (
            state = 'rejected'
            AND admitted_event_id IS NULL
            AND admitted_at IS NULL
            AND resolved_at IS NOT NULL
            AND canceled_at IS NULL
            AND rejected_reason IS NOT NULL
        )
        OR (
            state = 'canceled'
            AND admitted_event_id IS NULL
            AND admitted_at IS NULL
            AND resolved_at IS NULL
            AND canceled_at IS NOT NULL
        )
    ),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (project_id, actor_id) REFERENCES actors(project_id, id),
    FOREIGN KEY (project_id, agent_config_id) REFERENCES agent_configs(project_id, id),
    FOREIGN KEY (project_id, agent_id, integration_target_id) REFERENCES integration_targets(project_id, agent_id, id),
    FOREIGN KEY (agent_id, admitted_event_id) REFERENCES agent_events(agent_id, id),
    UNIQUE (agent_id, id)
);

-- +goose StatementBegin
CREATE FUNCTION enforce_agent_input_mutation_policy()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
	IF TG_OP = 'DELETE' THEN
		RAISE EXCEPTION 'agent_inputs are immutable'
			USING ERRCODE = '25006';
	END IF;

	IF OLD.state IN ('resolved', 'rejected', 'canceled') AND NEW IS DISTINCT FROM OLD THEN
		RAISE EXCEPTION 'terminal agent_inputs are immutable'
			USING ERRCODE = '25006';
	END IF;

    IF (NEW.delivery_mode IS DISTINCT FROM OLD.delivery_mode
        OR NEW.input_rank IS DISTINCT FROM OLD.input_rank)
       AND NOT (
           OLD.state = 'received'
           AND NEW.state = 'received'
           AND OLD.input_kind = 'content'
       ) THEN
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
       OR NEW.metadata IS DISTINCT FROM OLD.metadata THEN
        RAISE EXCEPTION 'agent_input intent and identity are immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_inputs_mutation_policy
    BEFORE UPDATE OR DELETE
    ON agent_inputs
    FOR EACH ROW
    EXECUTE FUNCTION enforce_agent_input_mutation_policy();

-- +goose StatementBegin
CREATE FUNCTION enforce_agent_input_admitted_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    admitted_event_kind text;
    admitted_event_idempotency_key text;
    admitted_agent_input_id uuid;
BEGIN
    IF NEW.admitted_event_id IS NOT NULL THEN
        SELECT event_kind, idempotency_key, agent_input_id
        INTO admitted_event_kind, admitted_event_idempotency_key, admitted_agent_input_id
        FROM agent_events
        WHERE agent_id = NEW.agent_id
          AND id = NEW.admitted_event_id;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'admitted agent_input % references missing event %', NEW.id, NEW.admitted_event_id
                USING ERRCODE = '23514';
        END IF;

        IF NOT (
            admitted_event_kind = 'agent_input'
            AND admitted_agent_input_id = NEW.id
        )
           OR admitted_event_idempotency_key IS DISTINCT FROM ('agent_input:' || NEW.id) THEN
            RAISE EXCEPTION 'agent_input % admitted by invalid event %', NEW.id, NEW.admitted_event_id
                USING ERRCODE = '23514';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_inputs_admitted_event_shape
    BEFORE INSERT OR UPDATE OF state, admitted_event_id, admitted_at, input_kind
    ON agent_inputs
    FOR EACH ROW
    EXECUTE FUNCTION enforce_agent_input_admitted_event();

CREATE UNIQUE INDEX agent_inputs_idempotency_idx
    ON agent_inputs(project_id, agent_id, idempotency_scope, input_idempotency_key)
    WHERE idempotency_scope IS NOT NULL AND input_idempotency_key IS NOT NULL;

CREATE INDEX agent_inputs_content_queued_idx
    ON agent_inputs(project_id, agent_id, input_rank ASC, queued_at ASC, id ASC)
    WHERE input_kind = 'content' AND delivery_mode = 'queued' AND state = 'received';

CREATE INDEX agent_inputs_content_steering_idx
    ON agent_inputs(project_id, agent_id, input_rank ASC, queued_at ASC, id ASC)
    WHERE input_kind = 'content' AND delivery_mode = 'steering' AND state = 'received';

CREATE INDEX agent_inputs_resolved_controls_idx
    ON agent_inputs(project_id, agent_id, control_type, admitted_event_id)
    WHERE input_kind = 'control' AND state = 'resolved';

CREATE UNIQUE INDEX agent_inputs_live_interaction_response_idx
    ON agent_inputs(project_id, agent_id, target_interaction_id)
    WHERE input_kind = 'interaction_response' AND state IN ('received', 'resolved');

CREATE INDEX agent_inputs_config_change_idx
    ON agent_inputs(project_id, agent_id, agent_config_id)
    WHERE input_kind = 'config_change';

ALTER TABLE agent_events
    ADD FOREIGN KEY (agent_id, agent_input_id) REFERENCES agent_inputs(agent_id, id);

CREATE UNIQUE INDEX agent_events_agent_input_idx
    ON agent_events(agent_id, agent_input_id)
    WHERE agent_input_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION enforce_agent_input_event_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM agent_inputs input
        WHERE input.agent_id = NEW.agent_id
          AND input.id = NEW.agent_input_id
          AND input.state = 'resolved'
          AND input.admitted_event_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'agent_input event % must admit its linked input', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER agent_input_events_admission_valid
AFTER INSERT ON agent_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (NEW.event_kind = 'agent_input')
EXECUTE FUNCTION enforce_agent_input_event_admission();

CREATE VIEW agent_stop_events AS
SELECT input.project_id,
       event.agent_id,
       event.sequence
FROM agent_events event
JOIN agent_inputs input ON input.agent_id = event.agent_id
  AND input.id = event.agent_input_id
WHERE event.event_kind = 'agent_input'
  AND input.input_kind = 'control'
  AND input.control_type = 'cancel_current'
  AND input.state = 'resolved';

-- +goose StatementBegin
-- Caller must first authorize p_agent_id's project.
CREATE FUNCTION agent_turn_id_at_event_sequence(
    p_agent_id uuid,
    p_event_sequence bigint
)
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
    SELECT event.turn_id
    FROM agent_events event
    WHERE event.agent_id = p_agent_id
      AND event.is_opening_event
      AND event.sequence <= p_event_sequence
    ORDER BY event.sequence DESC, event.id DESC
    LIMIT 1
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Caller must first authorize p_agent_id's project.
CREATE FUNCTION agent_turn_opening_content_inputs(
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
    SELECT input.id,
           event.sequence
    FROM agent_events event
    JOIN agent_inputs input ON input.agent_id = event.agent_id
      AND input.id = event.agent_input_id
      AND input.input_kind = 'content'
      AND input.state = 'resolved'
      AND input.admitted_event_id = event.id
    WHERE event.agent_id = p_agent_id
      AND event.turn_id = p_turn_id
      AND event.is_opening_event
      AND event.event_kind = 'agent_input'
      AND event.agent_input_id IS NOT NULL
      AND event.sequence <= p_max_event_sequence
$$;
-- +goose StatementEnd

CREATE TABLE agent_turns (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    turn_sequence bigint NOT NULL,
    latest_event_id uuid NOT NULL,
    latest_semantic_event_id uuid NOT NULL,
    CHECK (turn_sequence > 0),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_id, id, latest_event_id) REFERENCES agent_events(agent_id, turn_id, id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (agent_id, id, latest_semantic_event_id) REFERENCES agent_events(agent_id, turn_id, id) DEFERRABLE INITIALLY DEFERRED,
    UNIQUE (agent_id, id),
    UNIQUE (agent_id, turn_sequence)
);

-- +goose StatementBegin
CREATE FUNCTION agent_turns_reject_identity_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.agent_id IS DISTINCT FROM NEW.agent_id
       OR OLD.turn_sequence IS DISTINCT FROM NEW.turn_sequence THEN
        RAISE EXCEPTION 'agent turn identity is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agent_turns_identity_immutable
BEFORE UPDATE OF id, agent_id, turn_sequence
ON agent_turns
FOR EACH ROW EXECUTE FUNCTION agent_turns_reject_identity_change();

ALTER TABLE agent_events
    ADD FOREIGN KEY (agent_id, turn_id)
    REFERENCES agent_turns(agent_id, id)
    DEFERRABLE INITIALLY DEFERRED;

-- +goose StatementBegin
CREATE FUNCTION enforce_agent_turn_opening_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM agent_events event
        WHERE event.agent_id = NEW.agent_id
          AND event.turn_id = NEW.id
          AND event.is_opening_event
    ) THEN
        RAISE EXCEPTION 'agent turn % must have at least one opening event', NEW.id
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER agent_turns_opening_event_required
AFTER INSERT ON agent_turns
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_agent_turn_opening_event();

CREATE TABLE agent_wakeups (
    agent_id uuid PRIMARY KEY,
    ready_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    CHECK (jsonb_typeof(metadata) = 'object'),
    FOREIGN KEY (agent_id) REFERENCES agents(id)
);

CREATE INDEX agent_wakeups_global_ready_idx
    ON agent_wakeups(ready_at ASC, agent_id ASC);

CREATE TABLE agent_runtime_locks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    worker_process_id uuid NOT NULL,
    started_at timestamptz NOT NULL,
    renewed_at timestamptz NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    cancel_requested_at timestamptz,
    CHECK (renewed_at >= started_at),
    CHECK (lease_expires_at > renewed_at),
    CHECK (cancel_requested_at IS NULL OR cancel_requested_at >= started_at),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    UNIQUE (agent_id)
);

CREATE INDEX agent_runtime_locks_expiry_idx
    ON agent_runtime_locks(lease_expires_at ASC, id ASC)
    INCLUDE (agent_id);

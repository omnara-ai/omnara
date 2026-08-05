-- +goose Up

-- Processes, output observations, artifacts, external deliveries, and content blocks.

CREATE TABLE processes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tool_call_id uuid NOT NULL,
    runtime_lock_id uuid NOT NULL,
    agent_machine_binding_id uuid NOT NULL,
    machine_id uuid NOT NULL,
    -- Durable execution authorization, not a daemon identity or lease.
    execution_granted_at timestamptz,
    io_mode text NOT NULL DEFAULT 'pipe',
    command text NOT NULL,
    shell_selector text NOT NULL,
    cwd text NOT NULL DEFAULT '',
    env jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_env jsonb NOT NULL DEFAULT '{}'::jsonb,
    timeout_seconds integer NOT NULL DEFAULT 0,
    initial_wait_ms integer NOT NULL DEFAULT 0,
    default_output_cursor bigint NOT NULL DEFAULT 0,
    state text NOT NULL,
    state_reason_code text,
    state_reason_message text NOT NULL DEFAULT '',
    source_started_at timestamptz,
    source_ended_at timestamptz,
    state_changed_at timestamptz NOT NULL,
    exit_code integer,
    exit_signal text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (command <> ''),
    CHECK (shell_selector <> ''),
    CHECK (jsonb_typeof(env) = 'object'),
    CHECK (jsonb_typeof(secret_env) = 'object'),
    CHECK (io_mode IN ('pipe', 'pty')),
    CHECK (timeout_seconds >= 0),
    CHECK (initial_wait_ms >= 0),
    CHECK (default_output_cursor >= 0),
    CHECK (state IN ('queued', 'starting', 'running', 'exited', 'failed', 'killed', 'unknown')),
    CHECK (NOT (state = 'queued' AND execution_granted_at IS NOT NULL)),
    CHECK (NOT (state IN ('starting', 'running') AND execution_granted_at IS NULL)),
    CHECK (
        source_started_at IS NOT NULL OR state IN ('queued', 'starting', 'failed', 'killed', 'unknown')
    ),
    CHECK (
        source_ended_at IS NULL OR state IN ('exited', 'failed', 'killed', 'unknown')
    ),
    CHECK (state <> 'exited' OR source_ended_at IS NOT NULL),
    CHECK (state <> 'killed' OR source_ended_at IS NOT NULL),
    CHECK (
        state <> 'failed' OR source_started_at IS NULL OR source_ended_at IS NOT NULL
    ),
    CHECK (
        source_started_at IS NULL OR source_ended_at IS NULL OR source_ended_at >= source_started_at
    ),
    CHECK (state <> 'exited' OR exit_code IS NOT NULL),
    CHECK (state IN ('exited', 'failed') OR exit_code IS NULL),
    CHECK (state <> 'failed' OR state_reason_code IS NOT NULL),
    CHECK (state <> 'unknown' OR state_reason_code IS NOT NULL),
    UNIQUE (project_id, agent_id, id),
    UNIQUE (agent_id, tool_call_id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id),
    FOREIGN KEY (project_id, agent_id, agent_machine_binding_id, machine_id) REFERENCES agent_machine_bindings(project_id, agent_id, id, machine_id)
);

CREATE INDEX processes_nonterminal_idx
    ON processes(project_id, agent_id, updated_at)
    WHERE state IN ('queued', 'starting', 'running');

CREATE INDEX processes_daemon_process_offer_idx
    ON processes(org_id, machine_id, created_at, id)
    WHERE state = 'queued';

CREATE INDEX processes_machine_active_idx
    ON processes(org_id, machine_id)
    WHERE state IN ('starting', 'running');

CREATE INDEX processes_unreachable_candidate_idx
    ON processes(created_at, id)
    INCLUDE (org_id, machine_id, agent_id, tool_call_id)
    WHERE state IN ('queued', 'starting', 'running');

CREATE TABLE process_actions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    process_id uuid NOT NULL,
    tool_call_id uuid NOT NULL,
    runtime_lock_id uuid NOT NULL,
    action_kind text NOT NULL,
    seq bigint NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    state text NOT NULL DEFAULT 'queued',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    state_reason_code text,
    state_reason_message text NOT NULL DEFAULT '',
    CHECK (action_kind IN ('write', 'read', 'interrupt', 'terminate')),
    CHECK (state IN ('queued', 'accepted', 'applied', 'failed', 'unknown')),
    CHECK (seq > 0),
    CHECK (state <> 'queued' OR (state_reason_code IS NULL AND state_reason_message = '')),
    CHECK (state <> 'accepted' OR (state_reason_code IS NULL AND state_reason_message = '')),
    CHECK (state <> 'failed' OR state_reason_code IS NOT NULL),
    CHECK (state <> 'unknown' OR state_reason_code IS NOT NULL),
    CHECK (jsonb_typeof(payload) = 'object'),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_id, process_id) REFERENCES processes(project_id, agent_id, id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id),
    UNIQUE (project_id, agent_id, process_id, seq),
    UNIQUE (agent_id, tool_call_id)
);

CREATE INDEX process_actions_queued_idx
    ON process_actions(project_id, agent_id, process_id, seq)
    WHERE state IN ('queued', 'accepted');

CREATE UNIQUE INDEX process_actions_one_accepted_per_process_idx
    ON process_actions(project_id, agent_id, process_id)
    WHERE state = 'accepted';

CREATE INDEX process_actions_org_inflight_idx
    ON process_actions(org_id, state, project_id, agent_id, process_id, created_at, id)
    WHERE state IN ('queued', 'accepted');

CREATE INDEX process_actions_unreachable_candidate_idx
    ON process_actions(created_at, id)
    INCLUDE (org_id, project_id, agent_id, process_id, tool_call_id, action_kind, state)
    WHERE state IN ('queued', 'accepted');

CREATE TABLE artifacts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    content_type text NOT NULL,
    filename text,
    digest text,
    size_bytes bigint,
    idempotency_key text,
    created_at timestamptz NOT NULL,
    CHECK (size_bytes IS NULL OR size_bytes >= 0),
    UNIQUE (agent_id, id),
    UNIQUE (agent_id, idempotency_key),
    FOREIGN KEY (agent_id) REFERENCES agents(id)
);

CREATE TABLE content_blocks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    agent_id uuid NOT NULL,
    owner_kind text NOT NULL,
    owner_agent_input_id uuid,
    owner_model_output_id uuid,
    owner_tool_call_result_id uuid,
    ordinal integer NOT NULL,
    block_kind text NOT NULL,
    text_content text,
    structured_data jsonb,
    artifact_id uuid,
    tool_call_id uuid,
    created_at timestamptz NOT NULL,
    CHECK (owner_kind IN ('agent_input', 'model_output', 'tool_call_result')),
    CHECK (ordinal >= 0),
    CHECK (block_kind IN ('text', 'structured_data', 'artifact', 'reasoning', 'tool_call', 'error')),
    CHECK (
        (owner_kind = 'agent_input' AND owner_agent_input_id IS NOT NULL AND owner_model_output_id IS NULL AND owner_tool_call_result_id IS NULL)
        OR (owner_kind = 'model_output' AND owner_agent_input_id IS NULL AND owner_model_output_id IS NOT NULL AND owner_tool_call_result_id IS NULL)
        OR (owner_kind = 'tool_call_result' AND owner_agent_input_id IS NULL AND owner_model_output_id IS NULL AND owner_tool_call_result_id IS NOT NULL)
    ),
    CHECK (
        (owner_kind = 'agent_input' AND block_kind IN ('text', 'artifact'))
        OR (owner_kind = 'model_output' AND block_kind IN ('text', 'reasoning', 'tool_call', 'error'))
        OR (owner_kind = 'tool_call_result' AND block_kind IN ('text', 'structured_data', 'artifact'))
    ),
    CHECK (
        (block_kind = 'text' AND text_content IS NOT NULL AND structured_data IS NULL AND artifact_id IS NULL AND tool_call_id IS NULL)
        OR (block_kind = 'structured_data' AND text_content IS NULL AND structured_data IS NOT NULL AND artifact_id IS NULL AND tool_call_id IS NULL)
        OR (block_kind = 'reasoning' AND text_content IS NOT NULL AND structured_data IS NULL AND artifact_id IS NULL AND tool_call_id IS NULL)
        OR (block_kind = 'error' AND text_content IS NOT NULL AND structured_data IS NULL AND artifact_id IS NULL AND tool_call_id IS NULL)
        OR (block_kind = 'artifact' AND text_content IS NULL AND structured_data IS NULL AND artifact_id IS NOT NULL AND tool_call_id IS NULL)
        OR (block_kind = 'tool_call' AND text_content IS NULL AND structured_data IS NULL AND artifact_id IS NULL AND tool_call_id IS NOT NULL)
    ),
    FOREIGN KEY (agent_id) REFERENCES agents(id),
    FOREIGN KEY (agent_id, owner_agent_input_id) REFERENCES agent_inputs(agent_id, id),
    FOREIGN KEY (agent_id, owner_model_output_id) REFERENCES model_outputs(agent_id, id),
    FOREIGN KEY (agent_id, owner_tool_call_result_id) REFERENCES tool_call_results(agent_id, id),
    FOREIGN KEY (agent_id, artifact_id) REFERENCES artifacts(agent_id, id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id)
);

-- +goose StatementBegin
CREATE FUNCTION prevent_content_block_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'content_blocks are immutable'
        USING ERRCODE = '25006';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER content_blocks_immutable
BEFORE UPDATE OR DELETE ON content_blocks
FOR EACH ROW EXECUTE FUNCTION prevent_content_block_mutation();

-- +goose StatementBegin
CREATE FUNCTION enforce_open_model_output_content_block_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_context_state text;
    owner_context_recovery_kind text;
    owner_output_stop_reason text;
BEGIN
    IF NEW.owner_kind <> 'model_output' THEN
        RETURN NEW;
    END IF;

    SELECT context.state, context.recovery_kind, output.stop_reason
    INTO owner_context_state, owner_context_recovery_kind, owner_output_stop_reason
    FROM model_outputs output
    JOIN model_call_contexts context ON context.agent_id = output.agent_id
      AND context.id = output.model_call_context_id
    WHERE output.agent_id = NEW.agent_id
      AND output.id = NEW.owner_model_output_id
    FOR SHARE OF context;

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

CREATE TRIGGER content_blocks_open_model_output_owner
BEFORE INSERT ON content_blocks
FOR EACH ROW EXECUTE FUNCTION enforce_open_model_output_content_block_owner();

CREATE UNIQUE INDEX content_blocks_agent_input_ordinal_idx
    ON content_blocks(agent_id, owner_agent_input_id, ordinal)
    WHERE owner_agent_input_id IS NOT NULL;

CREATE UNIQUE INDEX content_blocks_model_output_ordinal_idx
    ON content_blocks(agent_id, owner_model_output_id, ordinal)
    WHERE owner_model_output_id IS NOT NULL;

CREATE UNIQUE INDEX content_blocks_tool_result_ordinal_idx
    ON content_blocks(agent_id, owner_tool_call_result_id, ordinal)
    WHERE owner_tool_call_result_id IS NOT NULL;

CREATE UNIQUE INDEX content_blocks_tool_call_idx
    ON content_blocks(agent_id, tool_call_id)
    WHERE block_kind = 'tool_call';

-- +goose StatementBegin
CREATE FUNCTION enforce_tool_call_content_block()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
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

CREATE CONSTRAINT TRIGGER tool_call_content_block_required
AFTER INSERT ON tool_calls
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_tool_call_content_block();

-- +goose StatementBegin
CREATE FUNCTION enforce_content_block_agent_input_owner()
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

    IF owner_input_kind <> 'content' OR owner_control_type IS NOT NULL OR owner_state <> 'received' THEN
        RAISE EXCEPTION 'content blocks may only be owned by content agent inputs'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER enforce_content_block_agent_input_owner_trigger
BEFORE INSERT ON content_blocks
FOR EACH ROW EXECUTE FUNCTION enforce_content_block_agent_input_owner();

CREATE VIEW agent_event_read_projection AS
SELECT event.id,
       agent.org_id,
       agent.project_id,
       event.agent_id,
       event.turn_id,
       turn.turn_sequence,
       event.is_opening_event,
       event.sequence,
       event.event_kind,
       input.input_kind,
       input.actor_id,
       input.idempotency_scope,
       input.input_idempotency_key,
       event.agent_input_id,
       input.control_type,
       input.target_interaction_id,
       input.agent_config_id,
       tool_result.tool_call_id,
       tool_result.outcome AS tool_outcome,
       model_output.model_call_context_id,
       model_output.stop_reason AS model_stop_reason,
       event.context_checkpoint_id,
       checkpoint.summarized_through_event_sequence,
       checkpoint.summary AS checkpoint_summary,
       block_projection.content_blocks::jsonb AS content_blocks,
       event.created_at
FROM agent_events event
JOIN agents agent
  ON agent.id = event.agent_id
JOIN agent_turns turn
  ON turn.agent_id = event.agent_id
 AND turn.id = event.turn_id
LEFT JOIN agent_inputs input
  ON input.agent_id = event.agent_id
 AND input.id = event.agent_input_id
LEFT JOIN tool_call_results tool_result
  ON tool_result.agent_id = event.agent_id
 AND tool_result.id = event.tool_call_result_id
LEFT JOIN model_outputs model_output
  ON model_output.agent_id = event.agent_id
 AND model_output.id = event.model_output_id
LEFT JOIN context_checkpoints checkpoint
  ON checkpoint.agent_id = event.agent_id
 AND checkpoint.id = event.context_checkpoint_id
CROSS JOIN LATERAL (
  SELECT coalesce(jsonb_agg(
    CASE
      WHEN block.block_kind = 'text' THEN jsonb_build_object('type', 'text', 'text', block.text_content)
      WHEN block.block_kind = 'structured_data' THEN jsonb_build_object('type', 'structured_data', 'value', block.structured_data)
      WHEN block.block_kind = 'artifact' THEN jsonb_build_object('type', 'media_ref', 'artifact_id', block.artifact_id)
      WHEN block.block_kind = 'reasoning' THEN jsonb_build_object('type', 'reasoning', 'text', block.text_content)
      WHEN block.block_kind = 'error' THEN jsonb_build_object('type', 'error', 'text', block.text_content)
      WHEN block.block_kind = 'tool_call' THEN
        jsonb_build_object(
          'type', 'tool_call',
          'tool_call_id', block.tool_call_id,
          'tool_type', tool_block_call.type,
          'name', tool_block_call.name,
          'input', tool_block_call.input
        )
      ELSE NULL
    END
    ORDER BY block.ordinal, block.id
  ) FILTER (WHERE block.id IS NOT NULL AND block.block_kind IN ('text', 'structured_data', 'artifact', 'reasoning', 'tool_call', 'error')), '[]'::jsonb) AS content_blocks
  FROM content_blocks block
  LEFT JOIN tool_calls tool_block_call
    ON tool_block_call.agent_id = block.agent_id
   AND tool_block_call.id = block.tool_call_id
  WHERE block.agent_id = event.agent_id
    AND (
      block.owner_agent_input_id = event.agent_input_id
      OR block.owner_model_output_id = event.model_output_id
      OR block.owner_tool_call_result_id = event.tool_call_result_id
    )
) block_projection;

-- +goose Up

LOCK TABLE agents, agent_inputs, agent_events, agent_turns, model_call_contexts,
    model_outputs, tool_calls, tool_call_results, context_checkpoints IN ACCESS EXCLUSIVE MODE;

ALTER TABLE agents ADD COLUMN root_agent_id uuid;
ALTER TABLE model_call_contexts
    ADD COLUMN turn_id uuid,
    ADD COLUMN opening_input_ids uuid[],
    ADD COLUMN opening_event_sequence bigint;
ALTER TABLE agent_inputs
    ADD COLUMN opening_input_ids uuid[],
    ADD COLUMN opening_event_sequence bigint;
ALTER TABLE context_checkpoints
    ADD COLUMN opening_input_ids uuid[],
    ADD COLUMN opening_event_sequence bigint;

CREATE TABLE agent_execution_state (
    agent_id uuid PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    current_turn_id uuid,
    stop_sequence bigint NOT NULL DEFAULT 0 CHECK (stop_sequence >= 0),
    answered_through_sequence bigint NOT NULL DEFAULT 0 CHECK (answered_through_sequence >= stop_sequence),
    max_normal_input_sequence bigint NOT NULL DEFAULT 0 CHECK (max_normal_input_sequence >= 0),
    max_context_input_sequence bigint NOT NULL DEFAULT 0 CHECK (max_context_input_sequence >= max_normal_input_sequence),
    normal_context_id uuid,
    compaction_context_id uuid,
    pending_tool_output_id uuid,
    pending_output_limit_id uuid,
    pending_config_input_id uuid,
    pending_checkpoint_id uuid,
    turn_continuable boolean NOT NULL,
    incomplete_tools boolean NOT NULL,
    logical_ready_at timestamptz,
    FOREIGN KEY (agent_id, current_turn_id) REFERENCES agent_turns(agent_id, id),
    FOREIGN KEY (agent_id, normal_context_id) REFERENCES model_call_contexts(agent_id, id),
    FOREIGN KEY (agent_id, compaction_context_id) REFERENCES model_call_contexts(agent_id, id),
    FOREIGN KEY (agent_id, pending_tool_output_id) REFERENCES model_outputs(agent_id, id),
    FOREIGN KEY (agent_id, pending_output_limit_id) REFERENCES model_outputs(agent_id, id),
    FOREIGN KEY (agent_id, pending_config_input_id) REFERENCES agent_inputs(agent_id, id),
    FOREIGN KEY (agent_id, pending_checkpoint_id) REFERENCES context_checkpoints(agent_id, id),
    CHECK (compaction_context_id IS NULL OR normal_context_id IS NOT NULL),
    CHECK (pending_tool_output_id IS NULL OR pending_output_limit_id IS NULL),
    CHECK (NOT incomplete_tools OR turn_continuable),
    CHECK (current_turn_id IS NOT NULL OR
        (normal_context_id IS NULL AND compaction_context_id IS NULL AND pending_tool_output_id IS NULL
         AND pending_output_limit_id IS NULL AND pending_config_input_id IS NULL AND pending_checkpoint_id IS NULL))
);

CREATE INDEX tool_calls_output_idx ON tool_calls(agent_id, model_output_id);
CREATE INDEX tool_calls_output_incomplete_idx ON tool_calls(agent_id, model_output_id) WHERE state <> 'completed';
CREATE INDEX tool_calls_output_runnable_idx ON tool_calls(agent_id, model_output_id)
    WHERE state = 'awaiting_authorization' OR (state = 'ready' AND type IN ('built_in', 'mcp'));
CREATE INDEX agent_events_output_boundary_idx ON agent_events(agent_id, sequence DESC) WHERE event_kind = 'model_output';
CREATE INDEX model_call_contexts_turn_kind_idx
    ON model_call_contexts(agent_id, turn_id, operation_kind, input_event_sequence DESC, attempt_number DESC);

WITH RECURSIVE roots AS (
    SELECT id, project_id, id AS root_id FROM agents WHERE parent_agent_id IS NULL
    UNION ALL
    SELECT child.id, child.project_id, roots.root_id
    FROM roots JOIN agents child ON child.project_id = roots.project_id AND child.parent_agent_id = roots.id
)
UPDATE agents SET root_agent_id = roots.root_id FROM roots WHERE agents.id = roots.id;

-- +goose StatementBegin
DO $$
DECLARE invalid_agent uuid;
BEGIN
    SELECT id INTO invalid_agent FROM agents WHERE root_agent_id IS NULL LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'invalid agent ancestry: %', invalid_agent; END IF;
    SELECT e.agent_id INTO invalid_agent FROM model_outputs o
    JOIN model_call_contexts c ON c.agent_id=o.agent_id AND c.id=o.model_call_context_id
    JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
    WHERE e.sequence<=c.input_event_sequence LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'model output precedes captured watermark: %', invalid_agent; END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard;
ALTER TABLE agent_inputs DISABLE TRIGGER agent_inputs_mutation_policy;
ALTER TABLE context_checkpoints DISABLE TRIGGER context_checkpoints_immutable;

UPDATE model_call_contexts c
SET turn_id = agent_turn_id_at_event_sequence(c.agent_id, c.input_event_sequence);
UPDATE model_call_contexts c SET opening_input_ids = o.ids, opening_event_sequence = o.first_sequence
FROM (
    SELECT c.id, coalesce(array_agg(o.input_id ORDER BY o.event_sequence)
        FILTER (WHERE o.input_id IS NOT NULL), '{}'::uuid[]) AS ids, min(o.event_sequence) AS first_sequence
    FROM model_call_contexts c
    LEFT JOIN LATERAL agent_model_call_opening_content_inputs(c.project_id,c.agent_id,c.turn_id,c.input_event_sequence) o
        ON true GROUP BY c.id
) o WHERE c.id = o.id;

UPDATE agent_inputs i SET opening_input_ids = o.ids, opening_event_sequence = o.first_sequence
FROM (
    SELECT i.id, coalesce(array_agg(o.input_id ORDER BY o.event_sequence)
        FILTER (WHERE o.input_id IS NOT NULL), '{}'::uuid[]) AS ids, min(o.event_sequence) AS first_sequence
    FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
    LEFT JOIN LATERAL agent_model_call_opening_content_inputs(i.project_id,i.agent_id,e.turn_id,e.sequence) o ON true
    WHERE i.input_kind='config_change' AND i.state='resolved' GROUP BY i.id
) o WHERE i.id=o.id;

UPDATE context_checkpoints c SET opening_input_ids=o.ids, opening_event_sequence=o.first_sequence
FROM (
    SELECT c.id, coalesce(array_agg(o.input_id ORDER BY o.event_sequence)
        FILTER (WHERE o.input_id IS NOT NULL), '{}'::uuid[]) AS ids, min(o.event_sequence) AS first_sequence
    FROM context_checkpoints c JOIN agents a ON a.id=c.agent_id
    JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
    LEFT JOIN LATERAL agent_model_call_opening_content_inputs(a.project_id,c.agent_id,e.turn_id,e.sequence) o ON true
    GROUP BY c.id
) o WHERE c.id=o.id;

ALTER TABLE model_call_contexts ENABLE TRIGGER model_call_contexts_transition_guard;
ALTER TABLE agent_inputs ENABLE TRIGGER agent_inputs_mutation_policy;
ALTER TABLE context_checkpoints ENABLE TRIGGER context_checkpoints_immutable;

-- +goose StatementBegin
CREATE FUNCTION execution_opening_is_valid(ids uuid[], first_sequence bigint)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
BEGIN
    IF ids IS NULL THEN RETURN false; END IF;
    IF cardinality(ids)=0 THEN RETURN first_sequence IS NULL; END IF;
    IF first_sequence IS NULL OR first_sequence<=0 OR array_ndims(ids)<>1 OR array_lower(ids,1)<>1 THEN
        RETURN false;
    END IF;
    IF cardinality(ids)=1 THEN
        RETURN ids[1] IS NOT NULL AND ids[1]<>'00000000-0000-0000-0000-000000000000'::uuid;
    END IF;
    RETURN NOT EXISTS(SELECT 1 FROM unnest(ids) id WHERE id IS NULL OR id='00000000-0000-0000-0000-000000000000'::uuid)
        AND cardinality(ids)=(SELECT count(DISTINCT id) FROM unnest(ids) id);
END;
$$;
-- +goose StatementEnd

ALTER TABLE agents ALTER COLUMN root_agent_id SET NOT NULL,
    ADD CONSTRAINT agents_root_fk FOREIGN KEY (project_id,root_agent_id) REFERENCES agents(project_id,id),
    ADD CONSTRAINT agents_root_shape CHECK ((parent_agent_id IS NULL)=(root_agent_id=id));
CREATE INDEX agents_root_idx ON agents(project_id,root_agent_id,id);
ALTER TABLE model_call_contexts ALTER COLUMN turn_id SET NOT NULL,
    ALTER COLUMN opening_input_ids SET NOT NULL, ALTER COLUMN opening_event_sequence SET NOT NULL,
    ADD CONSTRAINT model_call_contexts_turn_fk FOREIGN KEY (agent_id,turn_id) REFERENCES agent_turns(agent_id,id),
    ADD CONSTRAINT model_call_contexts_opening_shape CHECK (
        cardinality(opening_input_ids)>0 AND execution_opening_is_valid(opening_input_ids,opening_event_sequence)
        AND opening_event_sequence<=input_event_sequence);
ALTER TABLE agent_inputs ADD CONSTRAINT agent_inputs_opening_shape CHECK (
    CASE WHEN input_kind='config_change' AND state='resolved'
    THEN execution_opening_is_valid(opening_input_ids,opening_event_sequence)
    ELSE opening_input_ids IS NULL AND opening_event_sequence IS NULL END);
ALTER TABLE context_checkpoints ALTER COLUMN opening_input_ids SET NOT NULL,
    ADD CONSTRAINT context_checkpoints_opening_shape CHECK (execution_opening_is_valid(opening_input_ids,opening_event_sequence));

-- +goose StatementBegin
CREATE FUNCTION enforce_execution_lineage_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='agents' THEN
        IF OLD.root_agent_id IS DISTINCT FROM NEW.root_agent_id THEN
            RAISE EXCEPTION 'agent root is immutable' USING ERRCODE='25006';
        END IF;
    ELSE
        IF OLD.turn_id IS DISTINCT FROM NEW.turn_id
           OR OLD.opening_input_ids IS DISTINCT FROM NEW.opening_input_ids
           OR OLD.opening_event_sequence IS DISTINCT FROM NEW.opening_event_sequence THEN
            RAISE EXCEPTION 'context lineage is immutable' USING ERRCODE='25006';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER agents_root_immutable BEFORE UPDATE OF root_agent_id ON agents
    FOR EACH ROW EXECUTE FUNCTION enforce_execution_lineage_identity();
CREATE TRIGGER model_call_contexts_lineage_immutable
    BEFORE UPDATE OF turn_id,opening_input_ids,opening_event_sequence ON model_call_contexts
    FOR EACH ROW EXECUTE FUNCTION enforce_execution_lineage_identity();

INSERT INTO agent_execution_state (
    agent_id,current_turn_id,stop_sequence,answered_through_sequence,max_normal_input_sequence,max_context_input_sequence,
    normal_context_id,compaction_context_id,turn_continuable,incomplete_tools,logical_ready_at)
SELECT a.id, t.id, coalesce(s.sequence,0), greatest(coalesce(s.sequence,0),coalesce(answer.sequence,0)),
       coalesce(w.normal_sequence,0), coalesce(w.context_sequence,0), n.id,c.id,
       a.state='active' AND (agent_has_incomplete_tool_batch(a.project_id,a.id)
          OR EXISTS(SELECT 1 FROM agent_next_model_work(a.project_id,a.id))
          OR EXISTS(SELECT 1 FROM agent_continuable_model_contexts(a.project_id,a.id))),
       a.state='active' AND agent_has_incomplete_tool_batch(a.project_id,a.id),
       CASE WHEN a.state='active' THEN agent_next_wakeup_ready_at(a.project_id,a.id) END
FROM agents a
LEFT JOIN LATERAL (SELECT id FROM agent_turns WHERE agent_id=a.id ORDER BY turn_sequence DESC LIMIT 1) t ON true
LEFT JOIN LATERAL (SELECT max(sequence) AS sequence FROM agent_stop_events WHERE agent_id=a.id AND project_id=a.project_id) s ON true
LEFT JOIN LATERAL (SELECT sequence FROM agent_events WHERE agent_id=a.id AND event_kind='model_output' ORDER BY sequence DESC LIMIT 1) answer ON true
LEFT JOIN LATERAL (SELECT max(input_event_sequence) FILTER (WHERE operation_kind='normal') AS normal_sequence,
    max(input_event_sequence) AS context_sequence FROM model_call_contexts WHERE agent_id=a.id) w ON true
LEFT JOIN LATERAL (SELECT id,input_event_sequence FROM model_call_contexts WHERE agent_id=a.id AND turn_id=t.id
    AND operation_kind='normal' ORDER BY input_event_sequence DESC,attempt_number DESC LIMIT 1) n ON true
LEFT JOIN LATERAL (SELECT id FROM model_call_contexts WHERE agent_id=a.id AND turn_id=t.id
    AND operation_kind='compaction' AND input_event_sequence=n.input_event_sequence
    ORDER BY source_event_sequence_end,attempt_number DESC LIMIT 1) c ON true;

CREATE TEMP TABLE execution_pending_batches ON COMMIT DROP AS
SELECT DISTINCT a.id AS agent_id, eligible.model_output_id
FROM agents a CROSS JOIN LATERAL (
    SELECT c.model_output_id FROM tool_calls c
    JOIN agent_events e ON e.agent_id=c.agent_id AND e.model_output_id=c.model_output_id
    JOIN agent_execution_state h ON h.agent_id=c.agent_id AND h.current_turn_id=e.turn_id
    WHERE c.agent_id=a.id AND c.state<>'completed' AND e.sequence>=h.stop_sequence
    UNION
    SELECT f.model_output_id FROM agent_model_result_frontiers(a.project_id,a.id) f
) eligible;

-- +goose StatementBegin
DO $$
DECLARE invalid_agent uuid;
BEGIN
    SELECT agent_id INTO invalid_agent FROM execution_pending_batches GROUP BY agent_id HAVING count(*)>1 LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'multiple unconsumed tool outputs: %', invalid_agent; END IF;
    SELECT a.id INTO invalid_agent FROM agents a
    WHERE (SELECT count(*) FROM agent_continuable_model_contexts(a.project_id,a.id))>1 LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'multiple governing contexts: %', invalid_agent; END IF;
    SELECT a.id INTO invalid_agent FROM agents a JOIN agent_execution_state h ON h.agent_id=a.id
    WHERE EXISTS(SELECT 1 FROM agent_continuable_model_contexts(a.project_id,a.id) c
        WHERE c.model_call_context_id IS DISTINCT FROM h.normal_context_id
          AND c.model_call_context_id IS DISTINCT FROM h.compaction_context_id) LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'unrepresented governing context: %', invalid_agent; END IF;
END;
$$;
-- +goose StatementEnd

UPDATE agent_execution_state h SET pending_tool_output_id=b.model_output_id
FROM execution_pending_batches b WHERE b.agent_id=h.agent_id;
UPDATE agent_execution_state h SET pending_config_input_id=e.agent_input_id
FROM agents a CROSS JOIN LATERAL agent_unconsumed_config_change_frontiers(a.project_id,a.id) f
JOIN agent_events e ON e.agent_id=a.id AND e.sequence=f.config_event_sequence
WHERE h.agent_id=a.id;
UPDATE agent_execution_state h SET pending_checkpoint_id=e.context_checkpoint_id
FROM agents a CROSS JOIN LATERAL agent_unconsumed_context_checkpoint_frontiers(a.project_id,a.id) f
JOIN agent_events e ON e.agent_id=a.id AND e.sequence=f.checkpoint_event_sequence
WHERE h.agent_id=a.id;

CREATE TEMP TABLE execution_pending_limits ON COMMIT DROP AS
SELECT h.agent_id,o.id FROM agent_execution_state h
JOIN agent_events e ON e.agent_id=h.agent_id AND e.turn_id=h.current_turn_id AND e.event_kind='model_output'
JOIN model_outputs o ON o.agent_id=e.agent_id AND o.id=e.model_output_id
JOIN model_call_contexts c ON c.agent_id=o.agent_id AND c.id=o.model_call_context_id
WHERE o.stop_reason='max_tokens' AND c.operation_kind='normal' AND c.state='succeeded'
  AND e.sequence>h.max_normal_input_sequence AND e.sequence>=h.stop_sequence
  AND NOT EXISTS(SELECT 1 FROM tool_calls tc WHERE tc.agent_id=o.agent_id AND tc.model_output_id=o.id);

-- +goose StatementBegin
DO $$
DECLARE invalid_agent uuid;
BEGIN
    SELECT agent_id INTO invalid_agent FROM execution_pending_limits GROUP BY agent_id HAVING count(*)>1 LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'multiple unconsumed output limits: %', invalid_agent; END IF;
END;
$$;
-- +goose StatementEnd
UPDATE agent_execution_state h SET pending_output_limit_id=o.id FROM execution_pending_limits o WHERE o.agent_id=h.agent_id;

-- +goose StatementBegin
CREATE FUNCTION enforce_execution_root() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_agent_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM agents parent WHERE parent.project_id=NEW.project_id AND parent.id=NEW.parent_agent_id
          AND parent.root_agent_id=NEW.root_agent_id
    ) THEN
        RAISE EXCEPTION 'agent % root must match parent', NEW.id USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER agents_root_valid BEFORE INSERT ON agents FOR EACH ROW EXECUTE FUNCTION enforce_execution_root();

-- +goose StatementBegin
CREATE FUNCTION enforce_execution_context_turn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.turn_id IS DISTINCT FROM (
        SELECT turn_id FROM agent_events WHERE agent_id=NEW.agent_id AND is_opening_event
            AND sequence<=NEW.input_event_sequence ORDER BY sequence DESC LIMIT 1
    ) THEN
        RAISE EXCEPTION 'context % has invalid captured turn', NEW.id USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER model_call_contexts_execution_turn_valid BEFORE INSERT ON model_call_contexts
    FOR EACH ROW EXECUTE FUNCTION enforce_execution_context_turn();

DELETE FROM agent_wakeups w USING agents a,agent_execution_state h
WHERE w.agent_id=a.id AND h.agent_id=a.id AND (a.state='archived' OR h.logical_ready_at IS NULL
    OR EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.agent_id=a.id));
INSERT INTO agent_wakeups(agent_id,ready_at,updated_at)
SELECT h.agent_id,h.logical_ready_at,statement_timestamp()
FROM agent_execution_state h JOIN agents a ON a.id=h.agent_id
WHERE a.state='active' AND h.logical_ready_at IS NOT NULL
  AND NOT EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.agent_id=a.id)
ON CONFLICT(agent_id) DO UPDATE SET ready_at=CASE
    WHEN agent_wakeups.ready_at<=statement_timestamp() AND excluded.ready_at<=statement_timestamp()
    THEN least(agent_wakeups.ready_at,excluded.ready_at) ELSE excluded.ready_at END,
    updated_at=excluded.updated_at;

-- +goose StatementBegin
DO $$
DECLARE invalid_agent uuid;
BEGIN
    SELECT a.id INTO invalid_agent FROM agents a LEFT JOIN agent_execution_state h ON h.agent_id=a.id
    WHERE h.agent_id IS NULL LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'missing execution state: %', invalid_agent; END IF;
    SELECT h.agent_id INTO invalid_agent FROM agent_execution_state h JOIN agents a ON a.id=h.agent_id
    WHERE a.state='active' AND (
        h.incomplete_tools IS DISTINCT FROM agent_has_incomplete_tool_batch(a.project_id,a.id)
        OR h.turn_continuable IS DISTINCT FROM (agent_has_incomplete_tool_batch(a.project_id,a.id)
            OR EXISTS(SELECT 1 FROM agent_next_model_work(a.project_id,a.id))
            OR EXISTS(SELECT 1 FROM agent_continuable_model_contexts(a.project_id,a.id)))
        OR (h.logical_ready_at IS NULL) IS DISTINCT FROM (agent_next_wakeup_ready_at(a.project_id,a.id) IS NULL)
        OR ((h.logical_ready_at>statement_timestamp() OR agent_next_wakeup_ready_at(a.project_id,a.id)>statement_timestamp())
            AND h.logical_ready_at IS DISTINCT FROM agent_next_wakeup_ready_at(a.project_id,a.id))
    ) LIMIT 1;
    IF FOUND THEN RAISE EXCEPTION 'execution summary mismatch: %', invalid_agent; END IF;
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
BEGIN
    IF NEW.event_kind = 'model_output' THEN
        SELECT context_turn.turn_id
        INTO expected_turn_id
        FROM model_outputs output
        JOIN model_call_contexts context_turn ON context_turn.agent_id = output.agent_id
          AND context_turn.id = output.model_call_context_id
        WHERE output.agent_id = NEW.agent_id
          AND output.id = NEW.model_output_id;

        IF expected_turn_id IS NULL OR expected_turn_id IS DISTINCT FROM NEW.turn_id THEN
            RAISE EXCEPTION 'model_output event % must use turn % captured by model context', NEW.id, expected_turn_id
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
        JOIN model_call_contexts context_turn ON context_turn.agent_id = checkpoint.agent_id
          AND context_turn.id = checkpoint.producer_model_call_context_id
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

CREATE OR REPLACE VIEW tool_call_read_projection AS
SELECT tool_call.id,
       context.project_id,
       tool_call.agent_id,
       context.turn_id,
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

DROP VIEW model_call_context_turns;
DROP VIEW agent_stop_events;
DROP FUNCTION agent_next_wakeup_ready_at(uuid,uuid);
DROP FUNCTION agent_next_model_work(uuid,uuid);
DROP FUNCTION agent_model_result_frontiers(uuid,uuid);
DROP FUNCTION agent_has_incomplete_tool_batch(uuid,uuid);
DROP FUNCTION agent_tool_work_frontiers(uuid,uuid);
DROP FUNCTION agent_unconsumed_config_change_frontiers(uuid,uuid);
DROP FUNCTION agent_unconsumed_context_checkpoint_frontiers(uuid,uuid);
DROP FUNCTION agent_continuable_model_contexts(uuid,uuid);
DROP FUNCTION model_call_context_has_later_semantic_event(uuid,uuid,uuid);
DROP FUNCTION agent_latest_unstarted_model_call_opening_inputs(uuid,uuid);
DROP FUNCTION agent_model_call_opening_content_inputs(uuid,uuid,uuid,bigint);
DROP FUNCTION agent_latest_turn_id(uuid,uuid);
DROP FUNCTION agent_turn_opening_content_inputs(uuid,uuid,bigint);
DROP FUNCTION agent_turn_id_at_event_sequence(uuid,bigint);

ALTER TABLE agent_wakeups DROP COLUMN metadata;

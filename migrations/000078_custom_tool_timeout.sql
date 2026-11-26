-- +goose NO TRANSACTION
-- +goose Up

ALTER TABLE tool_calls
    ADD COLUMN IF NOT EXISTS timeout_seconds integer,
    ADD COLUMN IF NOT EXISTS expires_at timestamptz;

ALTER TABLE tool_calls
    DROP CONSTRAINT IF EXISTS tool_calls_timeout_shape,
    DROP CONSTRAINT IF EXISTS tool_calls_expiry_shape,
    ADD CONSTRAINT tool_calls_timeout_shape CHECK (
        timeout_seconds IS NULL OR (type = 'custom' AND timeout_seconds BETWEEN 0 AND 86400)
    ) NOT VALID,
    ADD CONSTRAINT tool_calls_expiry_shape CHECK (
        expires_at IS NULL OR COALESCE(timeout_seconds, 0) > 0
    ) NOT VALID;

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_timeout_shape;
ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_expiry_shape;

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
       tool_call.timeout_seconds,
       tool_call.expires_at
FROM tool_calls tool_call
JOIN model_outputs model_output ON model_output.agent_id = tool_call.agent_id
  AND model_output.id = tool_call.model_output_id
JOIN agent_events source_event ON source_event.agent_id = tool_call.agent_id
  AND source_event.model_output_id = tool_call.model_output_id
  AND source_event.event_kind = 'model_output';

CREATE OR REPLACE TRIGGER tool_call_facts_immutable
BEFORE UPDATE OF
    id, project_id, agent_id, model_output_id, provider_call_id,
    name, input, type, timeout_seconds, created_at
OR DELETE ON tool_calls
FOR EACH ROW EXECUTE FUNCTION prevent_tool_call_fact_mutation();

DROP INDEX CONCURRENTLY IF EXISTS tool_calls_custom_expiry_idx;

CREATE INDEX CONCURRENTLY tool_calls_custom_expiry_idx
    ON tool_calls(expires_at, id)
    WHERE state = 'ready' AND type = 'custom' AND expires_at IS NOT NULL;

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

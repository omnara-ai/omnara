-- +goose NO TRANSACTION
-- +goose Up

ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_source_link_kind_check;
ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_source_link_check;

DROP INDEX CONCURRENTLY IF EXISTS agent_inputs_source_tool_call_idx;

CREATE INDEX CONCURRENTLY agent_inputs_source_tool_call_idx
    ON agent_inputs(source_tool_call_id)
    WHERE source_tool_call_id IS NOT NULL;

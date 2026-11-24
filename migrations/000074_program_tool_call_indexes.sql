-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS tool_calls_program_open_key_idx;

CREATE UNIQUE INDEX CONCURRENTLY tool_calls_program_open_key_idx
    ON tool_calls(agent_id, provider_call_id) WHERE caller = 'program' AND state <> 'completed';

DROP INDEX CONCURRENTLY IF EXISTS tool_calls_program_key_idx;

CREATE INDEX CONCURRENTLY tool_calls_program_key_idx
    ON tool_calls(agent_id, provider_call_id, created_at DESC, id DESC) WHERE caller = 'program';

DROP INDEX CONCURRENTLY IF EXISTS tool_calls_program_parent_idx;

CREATE INDEX CONCURRENTLY tool_calls_program_parent_idx
    ON tool_calls(agent_id, parent_tool_call_id, created_at, id) WHERE caller = 'program';

DROP INDEX CONCURRENTLY IF EXISTS tool_calls_program_open_idx;

CREATE INDEX CONCURRENTLY tool_calls_program_open_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND state <> 'completed';

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_caller_shape;

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_program_process_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_id_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_check;

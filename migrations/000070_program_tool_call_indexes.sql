-- +goose NO TRANSACTION
-- +goose Up

-- Program tool call indexes, built without blocking tool_calls writes. 000071
-- attaches the unique ones as constraints. IF NOT EXISTS keeps this a no-op on
-- databases that ran 000069 before it was split, where the constraints already
-- own these indexes. A build interrupted here leaves an invalid index that
-- 000071 refuses to attach; drop it and rerun.

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS tool_calls_agent_id_id_model_output_id_key
    ON tool_calls(agent_id, id, model_output_id);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS tool_calls_program_key_key
    ON tool_calls(agent_id, parent_tool_call_id, program_key);

CREATE INDEX CONCURRENTLY IF NOT EXISTS tool_calls_program_open_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND state <> 'completed';

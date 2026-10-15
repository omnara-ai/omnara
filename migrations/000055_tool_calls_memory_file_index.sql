-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS tool_calls_memory_file_idx;

CREATE INDEX CONCURRENTLY tool_calls_memory_file_idx
    ON tool_calls(agent_id, (input->>'path'))
    WHERE type = 'built_in' AND name IN ('read_file', 'write_file');

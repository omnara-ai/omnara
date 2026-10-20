-- +goose NO TRANSACTION
-- +goose Up

-- Memory file read tracking looks up storage_read/storage_write calls by path. Model-supplied paths
-- are unbounded, so index their hash to stay within the btree row size limit.
DROP INDEX CONCURRENTLY IF EXISTS tool_calls_memory_file_idx;

CREATE INDEX CONCURRENTLY tool_calls_memory_file_idx
    ON tool_calls(agent_id, md5(input->>'path'))
    WHERE type = 'built_in' AND name IN ('storage_read', 'storage_write');

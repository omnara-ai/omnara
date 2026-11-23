-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS tool_calls_program_runnable_idx;

CREATE INDEX CONCURRENTLY tool_calls_program_runnable_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND program_named
        AND (state = 'awaiting_authorization' OR (state = 'ready' AND type <> 'custom'));

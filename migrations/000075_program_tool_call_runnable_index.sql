-- +goose NO TRANSACTION
-- +goose Up

-- Runs outside a transaction so the index build does not block tool calls. A failed concurrent build
-- leaves an invalid index, which running the migration again drops and rebuilds.

-- Named program tool calls a server can pick up without the command that made them: waiting to be
-- authorized, or approved and waiting to run. Every API server polls for them, so the poll reads
-- only these rather than every open call.
DROP INDEX CONCURRENTLY IF EXISTS tool_calls_program_runnable_idx;

CREATE INDEX CONCURRENTLY tool_calls_program_runnable_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND program_named
        AND (state = 'awaiting_authorization' OR (state = 'ready' AND type <> 'custom'));

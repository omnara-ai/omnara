-- +goose NO TRANSACTION
-- +goose Up

-- Build the program tool call indexes without blocking tool_calls writes and
-- validate 000069's constraints under SHARE UPDATE EXCLUSIVE, which does not
-- block reads or writes. Each statement commits on its own. A build
-- interrupted here leaves an invalid index; drop it and rerun.

CREATE UNIQUE INDEX CONCURRENTLY tool_calls_program_key_idx
    ON tool_calls(agent_id, parent_tool_call_id, provider_call_id) WHERE caller = 'program';

-- Claims scan only claimable calls, skipping ones waiting on a person or on
-- async work; the reaper scans every open call.
CREATE INDEX CONCURRENTLY tool_calls_program_claimable_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND state IN ('ready', 'awaiting_authorization');

CREATE INDEX CONCURRENTLY tool_calls_program_open_idx ON tool_calls(created_at, id)
    WHERE caller = 'program' AND state <> 'completed';

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_caller_shape;

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_program_process_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_id_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_check;

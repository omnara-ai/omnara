-- +goose NO TRANSACTION
-- +goose Up

-- Validate the program tool call constraints added NOT VALID in 000069 and
-- 000071. Each statement commits on its own and scans under SHARE UPDATE
-- EXCLUSIVE, which does not block reads or writes. Validating an already
-- valid constraint is a no-op, so an interrupted run can be repeated.
ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_caller_check;

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_program_shape;

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_program_process_fkey;

ALTER TABLE tool_calls VALIDATE CONSTRAINT tool_calls_program_parent_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_id_fkey;

ALTER TABLE agent_interactions VALIDATE CONSTRAINT agent_interactions_resolved_by_actor_check;

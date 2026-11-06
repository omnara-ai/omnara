-- +goose Up

-- Attach 000070's unique indexes as constraints and add the parent foreign key
-- that needs one, NOT VALID: metadata only, so the ACCESS EXCLUSIVE lock is
-- brief. 000072 validates outside this transaction. Constraints that already
-- exist, from 000069 before it was split, are left alone.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'tool_calls'::regclass AND conname = 'tool_calls_agent_id_id_model_output_id_key'
    ) THEN
        ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_agent_id_id_model_output_id_key
            UNIQUE USING INDEX tool_calls_agent_id_id_model_output_id_key;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'tool_calls'::regclass AND conname = 'tool_calls_program_key_key'
    ) THEN
        ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_program_key_key
            UNIQUE USING INDEX tool_calls_program_key_key;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'tool_calls'::regclass AND conname = 'tool_calls_program_parent_fkey'
    ) THEN
        ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_program_parent_fkey
            FOREIGN KEY (agent_id, parent_tool_call_id, model_output_id)
            REFERENCES tool_calls(agent_id, id, model_output_id) NOT VALID;
    END IF;
END;
$$;
-- +goose StatementEnd

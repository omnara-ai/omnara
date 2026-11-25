-- +goose Up

-- The W3C trace context sent with the request that created the input, so the
-- turn it opens can join the caller's trace. The checks are added NOT VALID so
-- this transaction never scans agent_inputs while holding its lock. The next
-- migration validates them.
ALTER TABLE agent_inputs
    ADD COLUMN trace_parent text,
    ADD COLUMN trace_state text,
    ADD CONSTRAINT agent_inputs_trace_parent_check
        CHECK (trace_parent IS NULL OR trace_parent ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$')
        NOT VALID,
    ADD CONSTRAINT agent_inputs_trace_state_check
        CHECK (trace_state IS NULL OR (trace_parent IS NOT NULL AND octet_length(trace_state) BETWEEN 1 AND 512))
        NOT VALID;

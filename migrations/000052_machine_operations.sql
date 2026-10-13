-- +goose Up

-- Agent-free machine operations (exec, file upload, file download) started
-- through the public machine API. Each row is one daemon process offered with
-- the existing process protocol; it is not an agent process, so it has no
-- project, agent, tool call, or machine binding.
CREATE TABLE machine_operations (
    id                   uuid PRIMARY KEY,
    org_id               uuid NOT NULL,
    machine_id           uuid NOT NULL,
    execution_spec       jsonb NOT NULL,
    timeout_seconds      integer NOT NULL,
    state                text NOT NULL DEFAULT 'queued',
    daemon_runtime_id    uuid,
    exit_code            integer,
    exit_signal          text NOT NULL DEFAULT '',
    state_reason_code    text NOT NULL DEFAULT '',
    state_reason_message text NOT NULL DEFAULT '',
    result               jsonb,
    created_at           timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at           timestamptz NOT NULL DEFAULT statement_timestamp(),

    FOREIGN KEY (org_id, machine_id) REFERENCES machines(org_id, id),
    CHECK (jsonb_typeof(execution_spec) = 'object'),
    CHECK (timeout_seconds > 0),
    CHECK (state IN ('queued', 'starting', 'running', 'exited', 'failed', 'killed', 'unknown')),
    CHECK (state = 'queued' OR state = 'failed' OR daemon_runtime_id IS NOT NULL),
    CHECK (state = 'exited' OR exit_code IS NULL OR state = 'failed')
);

CREATE INDEX machine_operations_machine_idx
    ON machine_operations(org_id, machine_id, created_at);

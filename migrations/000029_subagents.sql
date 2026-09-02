-- +goose Up

ALTER TABLE agents
    ADD COLUMN parent_agent_id uuid,
    ADD COLUMN spawn_tool_call_id uuid,
    ADD COLUMN subagent_handle text NOT NULL DEFAULT '',
    ADD COLUMN archive_after_idle_minutes integer,
    ADD CONSTRAINT agents_parent_agent_fk
        FOREIGN KEY (project_id, parent_agent_id) REFERENCES agents(project_id, id),
    ADD CONSTRAINT agents_subagent_handle_check
        CHECK ((parent_agent_id IS NULL) = (subagent_handle = '')),
    ADD CONSTRAINT agents_spawn_tool_call_requires_parent_check
        CHECK (spawn_tool_call_id IS NULL OR parent_agent_id IS NOT NULL),
    ADD CONSTRAINT agents_archive_after_idle_minutes_check
        CHECK (archive_after_idle_minutes IS NULL OR archive_after_idle_minutes >= 1),
    ADD CONSTRAINT agents_not_own_parent_check
        CHECK (parent_agent_id IS NULL OR parent_agent_id <> id);

CREATE INDEX agents_parent_agent_idx
    ON agents(project_id, parent_agent_id, created_at, id)
    WHERE parent_agent_id IS NOT NULL;

CREATE UNIQUE INDEX agents_spawn_tool_call_idx
    ON agents(project_id, parent_agent_id, spawn_tool_call_id)
    WHERE spawn_tool_call_id IS NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agents_reject_identity_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.org_id IS DISTINCT FROM NEW.org_id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.agent_profile_id IS DISTINCT FROM NEW.agent_profile_id
       OR OLD.parent_agent_id IS DISTINCT FROM NEW.parent_agent_id
       OR OLD.spawn_tool_call_id IS DISTINCT FROM NEW.spawn_tool_call_id
       OR OLD.subagent_handle IS DISTINCT FROM NEW.subagent_handle
       OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'agent identity is immutable'
            USING ERRCODE = '25006';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER agents_identity_immutable ON agents;

CREATE TRIGGER agents_identity_immutable
BEFORE UPDATE OF id, org_id, project_id, agent_profile_id, parent_agent_id, spawn_tool_call_id,
    subagent_handle, idempotency_key, created_at ON agents
FOR EACH ROW EXECUTE FUNCTION agents_reject_identity_change();

CREATE TABLE agent_waits (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tool_call_id uuid NOT NULL,
    mode text NOT NULL,
    state text NOT NULL,
    deadline_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CHECK (mode IN ('all', 'any')),
    CHECK (state IN ('open', 'completed', 'canceled')),
    CHECK ((state = 'open') = (completed_at IS NULL)),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (agent_id, tool_call_id) REFERENCES tool_calls(agent_id, id),
    UNIQUE (agent_id, tool_call_id),
    UNIQUE (project_id, id)
);

CREATE INDEX agent_waits_open_deadline_idx
    ON agent_waits(deadline_at)
    WHERE state = 'open' AND deadline_at IS NOT NULL;

CREATE TABLE agent_wait_targets (
    wait_id uuid NOT NULL REFERENCES agent_waits(id),
    project_id uuid NOT NULL,
    target_agent_id uuid NOT NULL,
    state text NOT NULL,
    result_kind text NOT NULL DEFAULT '',
    result_text text NOT NULL DEFAULT '',
    completed_at timestamptz,
    CHECK (state IN ('pending', 'done')),
    CHECK ((state = 'done') = (completed_at IS NOT NULL)),
    CHECK (state = 'pending' OR result_kind <> ''),
    CHECK (result_kind IN ('', 'result', 'failed', 'waiting_on_parent', 'canceled', 'archived', 'timeout')),
    FOREIGN KEY (project_id, wait_id) REFERENCES agent_waits(project_id, id),
    FOREIGN KEY (project_id, target_agent_id) REFERENCES agents(project_id, id),
    PRIMARY KEY (wait_id, target_agent_id)
);

CREATE INDEX agent_wait_targets_pending_idx
    ON agent_wait_targets(project_id, target_agent_id)
    WHERE state = 'pending';

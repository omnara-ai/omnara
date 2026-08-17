-- +goose Up

CREATE TABLE cron_triggers (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id uuid NOT NULL,
    name text NOT NULL,
    agent_profile_id uuid,
    agent_id uuid,
    cron_expression text NOT NULL,
    timezone text NOT NULL DEFAULT 'UTC',
    message_template text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    last_fired_at timestamptz,
    next_fire_after timestamptz,
    claimed_until timestamptz,
    claim_token uuid,
    failure_report jsonb,
    idempotency_key text,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (name <> ''),
    CHECK (cron_expression <> ''),
    CHECK (timezone <> ''),
    CHECK (message_template <> ''),
    CHECK (failure_report IS NULL OR jsonb_typeof(failure_report) = 'object'),
    CHECK (NOT enabled OR next_fire_after IS NOT NULL),
    CHECK (
        (agent_profile_id IS NOT NULL AND agent_id IS NULL) OR
        (agent_profile_id IS NULL AND agent_id IS NOT NULL)
    ),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (project_id, agent_profile_id) REFERENCES agent_profiles(project_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    UNIQUE (project_id, idempotency_key)
);

CREATE UNIQUE INDEX cron_triggers_active_name_idx ON cron_triggers(project_id, name)
    WHERE deleted_at IS NULL;

CREATE INDEX cron_triggers_name_trgm_idx ON cron_triggers USING gin (name gin_trgm_ops)
    WHERE deleted_at IS NULL;

CREATE INDEX cron_triggers_due_idx ON cron_triggers(next_fire_after, id)
    WHERE enabled AND deleted_at IS NULL;

CREATE INDEX cron_triggers_agent_profile_idx ON cron_triggers(project_id, agent_profile_id)
    WHERE agent_profile_id IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX cron_triggers_agent_idx ON cron_triggers(project_id, agent_id)
    WHERE agent_id IS NOT NULL AND deleted_at IS NULL;

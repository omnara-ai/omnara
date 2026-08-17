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

ALTER TABLE org_resource_limit_overrides
ADD COLUMN max_active_cron_triggers_per_project bigint CHECK (max_active_cron_triggers_per_project >= 0);

CREATE OR REPLACE VIEW default_resource_limits AS
SELECT
    1000::bigint AS max_active_projects_per_org,
    10000::bigint AS max_pending_org_invitations_per_org,
    10000::bigint AS max_active_org_api_keys_per_org,
    10000::bigint AS max_active_tenant_model_provider_configs_per_org,
    10000::bigint AS max_active_configured_models_per_provider,
    10000::bigint AS max_agent_configs_per_project,
    10000::bigint AS max_active_agent_profiles_per_project,
    10000::bigint AS max_active_agents_per_project,
    10000::bigint AS max_active_tenant_secrets_per_owner,
    10000::bigint AS max_active_skills_per_owner,
    10000::bigint AS max_active_tenant_machine_pools_per_org,
    10000::bigint AS max_live_machines_per_org,
    20::bigint AS max_active_byo_daemon_tokens_per_machine,
    32::bigint AS max_non_terminal_processes_per_agent,
    1000::bigint AS max_active_cron_triggers_per_project;

CREATE OR REPLACE VIEW effective_resource_limits AS
SELECT
    orgs.id AS org_id,
    coalesce(overrides.max_active_projects_per_org, defaults.max_active_projects_per_org) AS max_active_projects_per_org,
    coalesce(overrides.max_pending_org_invitations_per_org, defaults.max_pending_org_invitations_per_org) AS max_pending_org_invitations_per_org,
    coalesce(overrides.max_active_org_api_keys_per_org, defaults.max_active_org_api_keys_per_org) AS max_active_org_api_keys_per_org,
    coalesce(overrides.max_active_tenant_model_provider_configs_per_org, defaults.max_active_tenant_model_provider_configs_per_org) AS max_active_tenant_model_provider_configs_per_org,
    coalesce(overrides.max_active_configured_models_per_provider, defaults.max_active_configured_models_per_provider) AS max_active_configured_models_per_provider,
    coalesce(overrides.max_agent_configs_per_project, defaults.max_agent_configs_per_project) AS max_agent_configs_per_project,
    coalesce(overrides.max_active_agent_profiles_per_project, defaults.max_active_agent_profiles_per_project) AS max_active_agent_profiles_per_project,
    coalesce(overrides.max_active_agents_per_project, defaults.max_active_agents_per_project) AS max_active_agents_per_project,
    coalesce(overrides.max_active_tenant_secrets_per_owner, defaults.max_active_tenant_secrets_per_owner) AS max_active_tenant_secrets_per_owner,
    coalesce(overrides.max_active_skills_per_owner, defaults.max_active_skills_per_owner) AS max_active_skills_per_owner,
    coalesce(overrides.max_active_tenant_machine_pools_per_org, defaults.max_active_tenant_machine_pools_per_org) AS max_active_tenant_machine_pools_per_org,
    coalesce(overrides.max_live_machines_per_org, defaults.max_live_machines_per_org) AS max_live_machines_per_org,
    coalesce(overrides.max_active_byo_daemon_tokens_per_machine, defaults.max_active_byo_daemon_tokens_per_machine) AS max_active_byo_daemon_tokens_per_machine,
    coalesce(overrides.max_non_terminal_processes_per_agent, defaults.max_non_terminal_processes_per_agent) AS max_non_terminal_processes_per_agent,
    coalesce(overrides.max_active_cron_triggers_per_project, defaults.max_active_cron_triggers_per_project) AS max_active_cron_triggers_per_project
FROM orgs
CROSS JOIN default_resource_limits AS defaults
LEFT JOIN org_resource_limit_overrides AS overrides ON overrides.org_id = orgs.id
WHERE orgs.deleted_at IS NULL;

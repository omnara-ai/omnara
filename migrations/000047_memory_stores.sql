-- +goose Up

CREATE TABLE memory_stores (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id   uuid NOT NULL REFERENCES projects(id),
    name         text COLLATE "C" NOT NULL,
    description  text NOT NULL DEFAULT '',
    agent_access text NOT NULL DEFAULT 'read_write',
    created_at   timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at   timestamptz NOT NULL DEFAULT statement_timestamp(),
    deleted_at   timestamptz,

    CHECK (char_length(description) <= 1024),
    CHECK (agent_access IN ('read', 'read_write')),
    CHECK (length(name) BETWEEN 1 AND 64 AND name ~ '^[a-z0-9]+(-[a-z0-9]+)*$')
);

CREATE UNIQUE INDEX memory_stores_project_name_idx
    ON memory_stores(project_id, name)
    WHERE deleted_at IS NULL;

ALTER TABLE org_resource_limit_overrides
    ADD COLUMN max_active_memory_stores_per_project bigint CHECK (max_active_memory_stores_per_project >= 0),
    ADD COLUMN max_memories_per_store bigint CHECK (max_memories_per_store >= 0);

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
    1000::bigint AS max_active_cron_triggers_per_project,
    10000::bigint AS max_active_memory_stores_per_project,
    10000::bigint AS max_memories_per_store;

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
    coalesce(overrides.max_active_cron_triggers_per_project, defaults.max_active_cron_triggers_per_project) AS max_active_cron_triggers_per_project,
    coalesce(overrides.max_active_memory_stores_per_project, defaults.max_active_memory_stores_per_project) AS max_active_memory_stores_per_project,
    coalesce(overrides.max_memories_per_store, defaults.max_memories_per_store) AS max_memories_per_store
FROM orgs
CROSS JOIN default_resource_limits AS defaults
LEFT JOIN org_resource_limit_overrides AS overrides ON overrides.org_id = orgs.id
WHERE orgs.deleted_at IS NULL;

ALTER TABLE processes ADD COLUMN execution_spec jsonb;

UPDATE processes SET execution_spec = jsonb_build_object(
    'kind', 'shell',
    'shell', jsonb_build_object('command', command, 'shell_selector', shell_selector, 'io_mode', io_mode)
);

ALTER TABLE processes
    ALTER COLUMN execution_spec SET NOT NULL,
    DROP COLUMN command,
    DROP COLUMN shell_selector,
    DROP COLUMN io_mode,
    ADD CONSTRAINT processes_execution_check CHECK (COALESCE(
        jsonb_typeof(execution_spec) = 'object' AND (
            (execution_spec->>'kind' = 'shell'
                AND jsonb_typeof(execution_spec->'shell') = 'object'
                AND NOT execution_spec ? 'file_transfer'
                AND jsonb_typeof(execution_spec->'shell'->'command') = 'string'
                AND execution_spec->'shell'->>'command' <> ''
                AND jsonb_typeof(execution_spec->'shell'->'shell_selector') = 'string'
                AND execution_spec->'shell'->>'shell_selector' <> ''
                AND execution_spec->'shell'->>'io_mode' IN ('pipe', 'pty'))
            OR (execution_spec->>'kind' = 'file_transfer'
                AND jsonb_typeof(execution_spec->'file_transfer') = 'object'
                AND NOT execution_spec ? 'shell')
        ), false
    ));

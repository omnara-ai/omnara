-- +goose Up

CREATE TABLE org_resource_limit_overrides (
    org_id uuid PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    max_active_projects_per_org bigint CHECK (max_active_projects_per_org >= 0),
    max_pending_org_invitations_per_org bigint CHECK (max_pending_org_invitations_per_org >= 0),
    max_active_org_api_keys_per_org bigint CHECK (max_active_org_api_keys_per_org >= 0),
    max_active_tenant_model_provider_configs_per_org bigint CHECK (max_active_tenant_model_provider_configs_per_org >= 0),
    max_active_configured_models_per_provider bigint CHECK (max_active_configured_models_per_provider >= 0),
    max_agent_configs_per_project bigint CHECK (max_agent_configs_per_project >= 0),
    max_active_agent_profiles_per_project bigint CHECK (max_active_agent_profiles_per_project >= 0),
    max_active_agents_per_project bigint CHECK (max_active_agents_per_project >= 0),
    max_active_tenant_secrets_per_owner bigint CHECK (max_active_tenant_secrets_per_owner >= 0),
    max_active_skills_per_owner bigint CHECK (max_active_skills_per_owner >= 0),
    max_active_tenant_machine_pools_per_org bigint CHECK (max_active_tenant_machine_pools_per_org >= 0),
    max_live_machines_per_org bigint CHECK (max_live_machines_per_org >= 0),
    max_active_byo_daemon_tokens_per_machine bigint CHECK (max_active_byo_daemon_tokens_per_machine >= 0),
    max_non_terminal_processes_per_agent bigint CHECK (max_non_terminal_processes_per_agent >= 0)
);

CREATE VIEW default_resource_limits AS
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
    32::bigint AS max_non_terminal_processes_per_agent;

CREATE VIEW effective_resource_limits AS
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
    coalesce(overrides.max_non_terminal_processes_per_agent, defaults.max_non_terminal_processes_per_agent) AS max_non_terminal_processes_per_agent
FROM orgs
CROSS JOIN default_resource_limits AS defaults
LEFT JOIN org_resource_limit_overrides AS overrides ON overrides.org_id = orgs.id
WHERE orgs.deleted_at IS NULL;

-- name: LockResourceCreation :exec
SELECT pg_advisory_xact_lock(
    hashtextextended(
        'resource_creation:' || sqlc.arg(resource_kind)::text || ':' || sqlc.arg(scope)::text,
        0
    )
);

-- name: GetEffectiveResourceLimits :one
SELECT
    org_id,
    max_active_projects_per_org,
    max_pending_org_invitations_per_org,
    max_active_org_api_keys_per_org,
    max_active_tenant_model_provider_configs_per_org,
    max_active_configured_models_per_provider,
    max_agent_configs_per_project,
    max_active_agent_profiles_per_project,
    max_active_agents_per_project,
    max_active_tenant_secrets_per_owner,
    max_active_skills_per_owner,
    max_active_tenant_machine_pools_per_org,
    max_live_machines_per_org,
    max_active_byo_daemon_tokens_per_machine,
    max_non_terminal_processes_per_agent,
    max_active_cron_triggers_per_project
FROM effective_resource_limits
WHERE org_id = sqlc.arg(org_id);

-- name: CountActiveProjectsForOrg :one
SELECT count(*)::bigint
FROM projects
WHERE org_id = sqlc.arg(org_id)
  AND deleted_at IS NULL;

-- name: CountPendingOrgInvitationsForOrg :one
SELECT count(*)::bigint
FROM org_invitations
WHERE org_id = sqlc.arg(org_id);

-- name: CountActivePersonalAccessTokensForUser :one
SELECT count(*)::bigint
FROM personal_access_tokens
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: CountActiveOrgAPIKeysForOrg :one
SELECT count(*)::bigint
FROM org_api_keys
WHERE org_id = sqlc.arg(org_id)
  AND revoked_at IS NULL;

-- name: CountActiveMachineDaemonTokensForMachine :one
SELECT count(*)::bigint
FROM machine_daemon_tokens
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND revoked_at IS NULL;

-- name: CountActiveTenantModelProviderConfigsForOrg :one
SELECT count(*)::bigint
FROM model_provider_configs
WHERE org_id = sqlc.arg(org_id)
  AND management_kind = 'tenant'
  AND deleted_at IS NULL;

-- name: CountActiveConfiguredModelsForProvider :one
SELECT count(*)::bigint
FROM configured_models
WHERE org_id = sqlc.arg(org_id)
  AND model_provider_config_id = sqlc.arg(model_provider_config_id)
  AND deleted_at IS NULL;

-- name: CountAgentConfigsForProject :one
SELECT count(*)::bigint
FROM agent_configs
WHERE project_id = sqlc.arg(project_id);

-- name: CountActiveAgentProfilesForProject :one
SELECT count(*)::bigint
FROM agent_profiles
WHERE project_id = sqlc.arg(project_id)
  AND deleted_at IS NULL;

-- name: CountActiveAgentsForProject :one
SELECT count(*)::bigint
FROM agents
WHERE project_id = sqlc.arg(project_id)
  AND state = 'active';

-- name: CountActiveTenantSecretsForOwner :one
SELECT count(*)::bigint
FROM secrets
WHERE org_id = sqlc.arg(org_id)
  AND management_kind = 'tenant'
  AND owner_kind = sqlc.arg(owner_kind)
  AND owner_project_id IS NOT DISTINCT FROM sqlc.narg(owner_project_id)::uuid
  AND owner_user_id IS NOT DISTINCT FROM sqlc.narg(owner_user_id)::uuid
  AND deleted_at IS NULL;

-- name: CountActiveSkillsForOwner :one
SELECT count(*)::bigint
FROM skills
WHERE org_id = sqlc.arg(org_id)
  AND owner_kind = sqlc.arg(owner_kind)
  AND owner_project_id IS NOT DISTINCT FROM sqlc.narg(owner_project_id)::uuid
  AND owner_user_id IS NOT DISTINCT FROM sqlc.narg(owner_user_id)::uuid
  AND deleted_at IS NULL;

-- name: CountActiveTenantMachinePoolsForOrg :one
SELECT count(*)::bigint
FROM machine_pools
WHERE org_id = sqlc.arg(org_id)
  AND management_kind = 'tenant'
  AND deleted_at IS NULL;

-- name: CountLiveMachinesForOrg :one
SELECT count(*)::bigint
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND deleted_at IS NULL;

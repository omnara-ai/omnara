-- name: LockResourceCreation :exec
SELECT pg_advisory_xact_lock(
    hashtextextended(
        'resource_creation:' || sqlc.arg(resource_kind)::text || ':' || sqlc.arg(scope)::text,
        0
    )
);

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

-- name: CountActiveUserCreatedMachineDaemonTokensForMachine :one
SELECT count(*)::bigint
FROM machine_daemon_tokens
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND created_by_user_id IS NOT NULL
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

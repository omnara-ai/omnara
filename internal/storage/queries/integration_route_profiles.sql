-- name: LockIntegrationRouteByDeploymentKey :one
SELECT id, project_id, integration_install_id,
  deployment_key, behavior_key, configuration, agent_profile_id, state,
  deleted_at, created_at, updated_at
FROM integration_routes
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND deployment_key = sqlc.arg(deployment_key)
FOR UPDATE;

-- name: UpdateIntegrationRouteProfile :one
UPDATE integration_routes
SET agent_profile_id = sqlc.narg(agent_profile_id),
  configuration = configuration || sqlc.arg(configuration_patch)::jsonb,
  updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
RETURNING id, project_id, integration_install_id,
  deployment_key, behavior_key, configuration, agent_profile_id, state,
  deleted_at, created_at, updated_at;

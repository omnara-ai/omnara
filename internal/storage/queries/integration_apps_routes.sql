-- name: InsertIntegrationApp :one
INSERT INTO integration_apps(
  org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, configuration_revision, state,
  created_at, updated_at
)
VALUES (
  sqlc.arg(org_id), sqlc.narg(owner_project_id), sqlc.arg(provider),
  sqlc.arg(provider_app_ref), sqlc.arg(display_name), sqlc.arg(connector_key),
  sqlc.narg(credential_secret_id), sqlc.narg(installation_credential_kind),
  sqlc.arg(provider_config), 1, sqlc.arg(state),
  transaction_timestamp(), transaction_timestamp()
)
RETURNING id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, configuration_revision, state,
  deleted_at, created_at, updated_at;

-- name: GetIntegrationApp :one
SELECT id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, configuration_revision, state,
  deleted_at, created_at, updated_at
FROM integration_apps
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- Lock only after existing installation lifecycle/row locks. Return the locked
-- current row so Go rechecks provider/project scope, lifecycle and credential
-- policy before acquiring credential locks or writing the installation.
-- name: LockIntegrationAppForInstallation :one
-- @sqlc-vet-disable integration-apps-deleted-at
-- Retirement remains observable; the caller must reject deleted/disabled apps.
SELECT id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, configuration_revision, state,
  deleted_at, created_at, updated_at
FROM integration_apps
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
FOR SHARE;

-- Physical identity is exact in its organization/project ownership scope.
-- Disabled rows still own their unique identity; callers must check lifecycle
-- and immutable connector/credential policy before reusing a conflict winner.
-- name: GetIntegrationAppByProviderRef :one
SELECT id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, configuration_revision, state,
  deleted_at, created_at, updated_at
FROM integration_apps
WHERE org_id = sqlc.arg(org_id)
  AND owner_project_id IS NOT DISTINCT FROM sqlc.narg(owner_project_id)::uuid
  AND provider = sqlc.arg(provider)
  AND provider_app_ref = sqlc.arg(provider_app_ref)
  AND deleted_at IS NULL;

-- name: GetConnectorIntegrationApp :one
SELECT id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, configuration_revision, state,
  deleted_at, created_at, updated_at
FROM integration_apps
WHERE id = sqlc.arg(id)
  AND EXISTS (
    SELECT 1
    FROM generate_subscripts(sqlc.arg(connector_keys)::text[], 1) AS capability(index)
    WHERE (sqlc.arg(connector_keys)::text[])[capability.index] = integration_apps.connector_key
      AND (sqlc.arg(providers)::text[])[capability.index] = integration_apps.provider
  )
  AND state = 'active'
  AND deleted_at IS NULL;

-- name: GetConnectorIntegrationInstall :one
SELECT install.id, install.org_id, install.project_id, install.integration_app_id,
  install.installed_by_user_id,
  install.provider, install.integration_kind, install.connection_mode, install.state,
  install.provider_tenant_id, install.provider_account_ref,
  install.display_name, install.credential_secret_id,
  install.provider_identity, install.metadata,
  install.last_oauth_flow_id, install.deleted_at, install.created_at, install.updated_at,
  install.configuration_revision, install.installed_by_org_api_key_id
FROM integration_installs install
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.state = 'active'
 AND app.deleted_at IS NULL
WHERE install.integration_kind = 'managed'
  AND install.integration_app_id = sqlc.arg(integration_app_id)
  AND install.provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND install.provider_account_ref = sqlc.arg(provider_account_ref)
  AND install.state = 'active'
  AND install.deleted_at IS NULL;

-- name: GetConnectorIntegrationInstallByID :one
SELECT install.id, install.org_id, install.project_id, install.integration_app_id,
  install.installed_by_user_id,
  install.provider, install.integration_kind, install.connection_mode, install.state,
  install.provider_tenant_id, install.provider_account_ref,
  install.display_name, install.credential_secret_id,
  install.provider_identity, install.metadata,
  install.last_oauth_flow_id, install.deleted_at, install.created_at, install.updated_at,
  install.configuration_revision, install.installed_by_org_api_key_id
FROM integration_installs install
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.state = 'active'
 AND app.deleted_at IS NULL
WHERE install.integration_kind = 'managed'
  AND install.integration_app_id = sqlc.arg(integration_app_id)
  AND install.id = sqlc.arg(id)
  AND install.state = 'active'
  AND install.deleted_at IS NULL;

-- name: InsertIntegrationRoute :one
INSERT INTO integration_routes(
  project_id, integration_install_id,
  deployment_key, behavior_key, configuration, agent_profile_id,
  created_at, updated_at
)
SELECT
  sqlc.arg(project_id), sqlc.arg(integration_install_id),
  sqlc.arg(deployment_key), sqlc.arg(behavior_key),
  sqlc.arg(configuration), sqlc.narg(agent_profile_id),
  transaction_timestamp(), transaction_timestamp()
WHERE (
     SELECT count(*)
     FROM integration_routes route
     WHERE route.project_id = sqlc.arg(project_id)
       AND route.integration_install_id = sqlc.arg(integration_install_id)
       AND route.deleted_at IS NULL
   ) < sqlc.arg(max_active_routes)::integer
ON CONFLICT (project_id, integration_install_id, deployment_key) DO NOTHING
RETURNING id, project_id, integration_install_id,
  deployment_key, behavior_key, configuration, agent_profile_id,
  deleted_at, created_at, updated_at;

-- name: GetIntegrationRouteByDeploymentKey :one
SELECT id, project_id, integration_install_id,
  deployment_key, behavior_key, configuration, agent_profile_id,
  deleted_at, created_at, updated_at
FROM integration_routes
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND deployment_key = sqlc.arg(deployment_key);

-- name: GetIntegrationRoute :one
SELECT id, project_id, integration_install_id,
  deployment_key, behavior_key, configuration, agent_profile_id,
  deleted_at, created_at, updated_at
FROM integration_routes
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id);

-- name: LockIntegrationInstallForRouteMutation :one
SELECT id
FROM integration_installs
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(integration_install_id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: DeleteIntegrationRoute :execrows
UPDATE integration_routes
SET deleted_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: ListActiveIntegrationRoutes :many
SELECT id, project_id, integration_install_id,
  deployment_key, behavior_key, configuration, agent_profile_id,
  deleted_at, created_at, updated_at
FROM integration_routes
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND deleted_at IS NULL
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

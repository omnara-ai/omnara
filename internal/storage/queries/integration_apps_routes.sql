-- name: LockIntegrationAppProjectOwner :one
SELECT project.id
FROM projects project
WHERE project.org_id = sqlc.arg(org_id)
  AND project.id = sqlc.arg(owner_project_id)
  AND project.deleted_at IS NULL
FOR SHARE OF project;

-- name: InsertIntegrationApp :one
INSERT INTO integration_apps(
  org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, provider_metadata, configuration_revision, state,
  created_at, updated_at
)
VALUES (
  sqlc.arg(org_id), sqlc.narg(owner_project_id), sqlc.arg(provider),
  sqlc.arg(provider_app_ref), sqlc.arg(display_name), sqlc.arg(connector_key),
  sqlc.narg(credential_secret_id), sqlc.narg(installation_credential_kind),
  sqlc.arg(provider_config), sqlc.arg(provider_metadata), 1, sqlc.arg(state),
  transaction_timestamp(), transaction_timestamp()
)
RETURNING id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, provider_metadata, configuration_revision, state,
  deleted_at, created_at, updated_at;

-- name: GetIntegrationApp :one
SELECT id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, provider_metadata, configuration_revision, state,
  deleted_at, created_at, updated_at
FROM integration_apps
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: GetConnectorIntegrationApp :one
SELECT id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, provider_metadata, configuration_revision, state,
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
  install.agent_profile_id, install.agent_id, install.installed_by_user_id,
  install.provider, install.integration_kind, install.connection_mode, install.state,
  install.provider_tenant_id, install.provider_account_ref,
  install.provider_agent_display_name, install.credential_secret_id,
  install.provider_config, install.provider_identity, install.provider_metadata,
  install.last_oauth_flow_id, install.deleted_at, install.created_at, install.updated_at,
  install.configuration_revision
FROM integration_installs install
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.state = 'active'
 AND app.deleted_at IS NULL
WHERE install.integration_app_id = sqlc.arg(integration_app_id)
  AND install.provider_tenant_id = sqlc.arg(provider_tenant_id)
  AND install.provider_account_ref = sqlc.arg(provider_account_ref)
  AND install.state = 'active'
  AND install.deleted_at IS NULL;

-- name: GetConnectorIntegrationInstallByID :one
SELECT install.id, install.org_id, install.project_id, install.integration_app_id,
  install.agent_profile_id, install.agent_id, install.installed_by_user_id,
  install.provider, install.integration_kind, install.connection_mode, install.state,
  install.provider_tenant_id, install.provider_account_ref,
  install.provider_agent_display_name, install.credential_secret_id,
  install.provider_config, install.provider_identity, install.provider_metadata,
  install.last_oauth_flow_id, install.deleted_at, install.created_at, install.updated_at,
  install.configuration_revision
FROM integration_installs install
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.state = 'active'
 AND app.deleted_at IS NULL
WHERE install.integration_app_id = sqlc.arg(integration_app_id)
  AND install.id = sqlc.arg(id)
  AND install.state = 'active'
  AND install.deleted_at IS NULL;

-- name: InsertIntegrationRoute :one
INSERT INTO integration_routes(
  project_id, integration_install_id,
  deployment_key, handler_key, handler_version, configuration, state,
  created_at, updated_at
)
SELECT
  sqlc.arg(project_id), sqlc.arg(integration_install_id),
  sqlc.arg(deployment_key), sqlc.arg(handler_key),
  sqlc.arg(handler_version), sqlc.arg(configuration), sqlc.arg(state),
  transaction_timestamp(), transaction_timestamp()
WHERE sqlc.arg(state)::text <> 'active'
   OR (
     SELECT count(*)
     FROM integration_routes route
     WHERE route.project_id = sqlc.arg(project_id)
       AND route.integration_install_id = sqlc.arg(integration_install_id)
       AND route.state = 'active'
       AND route.deleted_at IS NULL
   ) < sqlc.arg(max_active_routes)::integer
ON CONFLICT (project_id, integration_install_id, deployment_key) DO NOTHING
RETURNING id, project_id, integration_install_id,
  deployment_key, handler_key, handler_version, configuration, state,
  deleted_at, created_at, updated_at;

-- name: GetIntegrationRouteByDeploymentKey :one
SELECT id, project_id, integration_install_id,
  deployment_key, handler_key, handler_version, configuration, state,
  deleted_at, created_at, updated_at
FROM integration_routes
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND deployment_key = sqlc.arg(deployment_key);

-- name: GetIntegrationRoute :one
SELECT id, project_id, integration_install_id,
  deployment_key, handler_key, handler_version, configuration, state,
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
SET state = 'disabled',
    deleted_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: ListActiveIntegrationRoutes :many
SELECT id, project_id, integration_install_id,
  deployment_key, handler_key, handler_version, configuration, state,
  deleted_at, created_at, updated_at
FROM integration_routes
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND state = 'active'
  AND deleted_at IS NULL
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

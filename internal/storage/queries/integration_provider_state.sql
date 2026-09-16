-- Control discovery deliberately includes disabled installations. Ordinary
-- configuration, receipt and operation queries retain their active-only gates.
-- name: ListConnectorInstallationControlScopes :many
SELECT install.id, install.project_id,
  coalesce(install.provider_tenant_id, '')::text AS provider_tenant_id,
  coalesce(install.provider_account_ref, '')::text AS provider_account_ref,
  install.state, install.configuration_revision, install.provider_identity
FROM integration_installs install
JOIN projects project ON project.id = install.project_id
  AND project.org_id = install.org_id AND project.deleted_at IS NULL
WHERE install.org_id = sqlc.arg(org_id)
  AND install.integration_app_id = sqlc.arg(integration_app_id)
  AND install.integration_kind = 'managed'
  AND install.provider_tenant_id = sqlc.arg(provider_tenant_id)::text
  AND install.state IN ('active', 'disabled') AND install.deleted_at IS NULL
  AND (sqlc.narg(owner_project_id)::uuid IS NULL OR install.project_id = sqlc.narg(owner_project_id)::uuid)
  AND (sqlc.arg(after_id)::uuid = '00000000-0000-0000-0000-000000000000' OR install.id > sqlc.arg(after_id)::uuid)
  AND install.id <= sqlc.arg(through_id)::uuid
ORDER BY install.id
LIMIT sqlc.arg(row_limit);

-- Capture once at receipt admission, not again after configuration rotation or
-- a partial scan. Nil is the fixed upper bound of an initially empty scan.
-- name: GetConnectorInstallationControlScopeEnd :one
SELECT coalesce((
  SELECT install.id
  FROM integration_installs install
  JOIN projects project ON project.id = install.project_id
    AND project.org_id = install.org_id AND project.deleted_at IS NULL
  WHERE install.org_id = sqlc.arg(org_id)
    AND install.integration_app_id = sqlc.arg(integration_app_id)
    AND install.integration_kind = 'managed'
    AND install.provider_tenant_id = sqlc.arg(provider_tenant_id)::text
    AND install.state IN ('active', 'disabled') AND install.deleted_at IS NULL
    AND (sqlc.narg(owner_project_id)::uuid IS NULL OR install.project_id = sqlc.narg(owner_project_id)::uuid)
  ORDER BY install.id DESC LIMIT 1
), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS end_install_id;

-- Acquire after org/project and installation lifecycle gates, before the app
-- SHARE lock, matching installation reauthorization and credential rotation.
-- name: LockConnectorInstallationProviderState :one
SELECT state, configuration_revision
FROM integration_installs
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id) AND integration_app_id = sqlc.arg(integration_app_id)
  AND integration_kind = 'managed'
  AND provider_tenant_id = sqlc.arg(provider_tenant_id)::text
  AND provider_account_ref = sqlc.arg(provider_account_ref)::text
  AND state IN ('active', 'disabled') AND deleted_at IS NULL
FOR UPDATE;

-- Every accepted observation advances the fence, including unchanged state.
-- Otherwise an older delayed disable could overwrite an observed restoration.
-- name: SetConnectorInstallationProviderState :one
UPDATE integration_installs
SET state = sqlc.arg(state), configuration_revision = configuration_revision + 1,
    updated_at = statement_timestamp()
WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id)
  AND integration_app_id = sqlc.arg(integration_app_id)
  AND integration_kind = 'managed' AND deleted_at IS NULL
  AND configuration_revision = sqlc.arg(expected_configuration_revision)
RETURNING state, configuration_revision;

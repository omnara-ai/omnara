-- Connector configuration and receipt mutation take parent locks before the receipt.
-- Credential rotation and installation retirement use the same install/app order.
-- name: LockConnectorIntegrationAuthority :one
WITH installation AS MATERIALIZED (
  SELECT install.id, install.org_id, install.integration_app_id
  FROM integration_installs install
  WHERE install.integration_kind = 'managed'
    AND install.project_id = sqlc.arg(project_id)
    AND install.id = sqlc.arg(integration_install_id)
    AND install.state = 'active' AND install.deleted_at IS NULL
  FOR SHARE
)
SELECT app.id, app.connector_key, app.provider
FROM integration_apps app
JOIN installation install ON install.integration_app_id = app.id AND install.org_id = app.org_id
WHERE app.state = 'active' AND app.deleted_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM generate_subscripts(sqlc.arg(connector_keys)::text[], 1) AS capability(index)
    WHERE (sqlc.arg(connector_keys)::text[])[capability.index] = app.connector_key
      AND (sqlc.arg(providers)::text[])[capability.index] = app.provider
  )
FOR SHARE OF app;

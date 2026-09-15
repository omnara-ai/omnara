-- name: UpsertChannelDefinition :one
INSERT INTO integration_channel_definitions (
  project_id, integration_install_id, implementation_key, kind, description,
  send_params_schema, capabilities, created_at, updated_at
) VALUES (
  sqlc.arg(project_id), sqlc.arg(integration_install_id), sqlc.arg(implementation_key),
  sqlc.arg(kind), sqlc.arg(description), sqlc.arg(send_params_schema),
  sqlc.arg(capabilities), statement_timestamp(), statement_timestamp()
)
ON CONFLICT (project_id, integration_install_id, implementation_key)
DO UPDATE SET description = EXCLUDED.description,
  send_params_schema = EXCLUDED.send_params_schema,
  capabilities = EXCLUDED.capabilities, updated_at = statement_timestamp()
WHERE integration_channel_definitions.kind = EXCLUDED.kind
RETURNING id, project_id, integration_install_id, implementation_key, kind,
  description, send_params_schema, capabilities, created_at, updated_at;

-- name: GetChannelDefinition :one
SELECT id, project_id, integration_install_id, implementation_key, kind,
  description, send_params_schema, capabilities, created_at, updated_at
FROM integration_channel_definitions
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id);

-- name: LockChannelDefinition :one
SELECT id
FROM integration_channel_definitions
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id)
FOR SHARE;

-- name: GetChannelDefinitionByImplementation :one
SELECT id, project_id, integration_install_id, implementation_key, kind,
  description, send_params_schema, capabilities, created_at, updated_at
FROM integration_channel_definitions
WHERE project_id = sqlc.arg(project_id) AND integration_install_id = sqlc.arg(integration_install_id)
  AND implementation_key = sqlc.arg(implementation_key);

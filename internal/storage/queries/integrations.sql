-- name: InsertIntegrationConnection :one
INSERT INTO integration_connections(
  org_id, project_id, installed_by_user_id,
  provider, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, created_at, updated_at
)
VALUES (
  sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(installed_by_user_id),
  sqlc.arg(provider), sqlc.arg(state), sqlc.arg(provider_tenant_id),
  sqlc.arg(provider_account_ref), sqlc.arg(provider_agent_display_name), sqlc.narg(credential_secret_id),
  sqlc.arg(provider_config), sqlc.arg(provider_identity), sqlc.arg(provider_metadata),
  sqlc.narg(last_oauth_flow_id), transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT DO NOTHING
RETURNING id, org_id, project_id, installed_by_user_id,
  provider, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at;

-- name: UpdateIntegrationConnection :one
UPDATE integration_connections
SET installed_by_user_id = sqlc.arg(installed_by_user_id),
    state = sqlc.arg(state),
    provider_agent_display_name = sqlc.arg(provider_agent_display_name),
    credential_secret_id = sqlc.narg(credential_secret_id),
    provider_config = sqlc.arg(provider_config),
    provider_identity = sqlc.arg(provider_identity),
    provider_metadata = sqlc.arg(provider_metadata),
    last_oauth_flow_id = coalesce(sqlc.narg(last_oauth_flow_id), last_oauth_flow_id),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND (
    sqlc.narg(last_oauth_flow_id)::uuid IS NULL
    OR last_oauth_flow_id IS NULL
    OR last_oauth_flow_id < sqlc.narg(last_oauth_flow_id)::uuid
  )
RETURNING id, org_id, project_id, installed_by_user_id,
  provider, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at;

-- name: LockIntegrationConnectionByProviderAccount :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_connections
WHERE provider = sqlc.arg(provider)
  AND provider_tenant_id = sqlc.arg(provider_tenant_id)
  AND provider_account_ref = sqlc.arg(provider_account_ref)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: GetIntegrationConnection :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_connections
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: LockIntegrationConnectionForMutation :one
SELECT id
FROM integration_connections
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: LockIntegrationConnectionLifecycleShared :exec
SELECT pg_advisory_xact_lock_shared(
  hashtextextended('integration_connection_lifecycle:' || sqlc.arg(connection_id)::uuid::text, 0)
);

-- Serializes connection creation/reconnection before its row identity is known.
-- JSON encoding keeps account components unambiguous; hash collisions only
-- cause harmless extra serialization.
-- name: LockIntegrationConnectionAccount :exec
SELECT pg_advisory_xact_lock(hashtextextended(
  'integration_connection_account:' || jsonb_build_array(sqlc.arg(provider)::text,
    sqlc.arg(provider_tenant_id)::text, sqlc.arg(provider_account_ref)::text)::text, 0));

-- name: LockIntegrationConnectionLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(
  hashtextextended('integration_connection_lifecycle:' || sqlc.arg(connection_id)::uuid::text, 0)
);

-- name: GetIntegrationConnectionByID :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_connections
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListIntegrationConnectionsForProject :many
WITH listed AS (
SELECT connection.id, connection.org_id, connection.project_id,
       connection.installed_by_user_id, connection.provider,
       connection.state, connection.provider_tenant_id, connection.provider_account_ref,
       connection.provider_agent_display_name, connection.credential_secret_id,
       connection.provider_config, connection.provider_identity, connection.provider_metadata,
       connection.last_oauth_flow_id, connection.created_at, connection.updated_at,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(connection.provider_agent_display_name)
         WHEN 'created_at' THEN to_char(connection.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(connection.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
       END::text AS sort_key
FROM integration_connections connection
WHERE connection.project_id = sqlc.arg(project_id)
  AND connection.deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR connection.provider_agent_display_name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.narg(oauth_flow_id)::uuid IS NULL OR connection.last_oauth_flow_id = sqlc.narg(oauth_flow_id)::uuid)
)
SELECT id, org_id, project_id, installed_by_user_id,
       provider, state,
       provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
       provider_config, provider_identity, provider_metadata,
       last_oauth_flow_id, created_at, updated_at, sort_key
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit);

-- name: LockIntegrationConnectionForDisable :one
SELECT state, last_oauth_flow_id
FROM integration_connections
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: DisableIntegrationConnection :execrows
UPDATE integration_connections
SET state = 'disabled',
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND state = 'active'
  AND last_oauth_flow_id IS NOT DISTINCT FROM sqlc.narg(expected_oauth_flow_id)::uuid;

-- name: DeleteIntegrationConnection :execrows
-- Clearing the credential releases the secret for deletion; the install
-- keeps whatever active/disabled state it had as provenance.
UPDATE integration_connections
SET credential_secret_id = NULL, deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteIntegrationTargets :exec
UPDATE integration_targets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_connection_id = sqlc.arg(integration_connection_id)
  AND deleted_at IS NULL;

-- name: ListIntegrationConnectionAgentIDsForLifecycle :many
-- @sqlc-vet-disable integration-targets-deleted-at
-- Include historical targets whose agents may still hold references to clear.
SELECT DISTINCT agent_id
FROM integration_targets
WHERE project_id = sqlc.arg(project_id)
  AND integration_connection_id = sqlc.arg(integration_connection_id)
ORDER BY agent_id;

-- name: ClearDeletedIntegrationTargetsFromAgents :exec
-- @sqlc-vet-disable integration-targets-deleted-at
-- Clears agent references before soft deleting the install's targets.
UPDATE agents agent SET integration_target_id = NULL, interaction_resource_key = NULL, updated_at = statement_timestamp()
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.integration_target_id IN (
    SELECT target.id FROM integration_targets target
    WHERE target.project_id = sqlc.arg(project_id)
      AND target.integration_connection_id = sqlc.arg(integration_connection_id)
  );

-- name: IntegrationOAuthFlowConsumed :one
SELECT EXISTS (
  SELECT 1 FROM integration_connections WHERE last_oauth_flow_id = sqlc.arg(last_oauth_flow_id) AND deleted_at IS NULL
) AS consumed;

-- name: GetIntegrationConnectionByProviderAccount :one
SELECT id, org_id, project_id, installed_by_user_id,
  provider, state,
  provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id,
  provider_config, provider_identity, provider_metadata,
  last_oauth_flow_id, deleted_at, created_at, updated_at
FROM integration_connections
WHERE provider = sqlc.arg(provider)
  AND provider_tenant_id = sqlc.arg(provider_tenant_id)
  AND provider_account_ref = sqlc.arg(provider_account_ref)
  AND deleted_at IS NULL;

-- name: GetIntegrationTarget :one
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.integration_connection_id, target.target_ref, target.provider_ref,
  target.provider_ref_kind, target.display_name, target.provider_metadata, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(id)
  AND target.deleted_at IS NULL;

-- name: UpdateIntegrationTargetDisplayNamesByProviderRefPrefix :execrows
UPDATE integration_targets
SET display_name = sqlc.arg(display_name),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_connection_id = sqlc.arg(integration_connection_id)
  AND deleted_at IS NULL
  AND split_part(provider_ref, ':', 1) = sqlc.arg(provider_ref_prefix)
  AND display_name IS DISTINCT FROM sqlc.arg(display_name);

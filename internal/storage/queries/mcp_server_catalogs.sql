-- name: GetMCPServerCatalog :one
SELECT catalog.id, catalog.org_id, catalog.endpoint_url, catalog.secret_id,
       catalog.secret_version_id, catalog.aws_region, catalog.aws_service,
       catalog.revision, catalog.protocol_version, catalog.server_capabilities,
       catalog.server_info, catalog.instructions, catalog.discover_cache_scope,
       catalog.discover_ttl_ms, catalog.discover_expires_at, catalog.tools_snapshot,
       catalog.tools_cache_scope, catalog.tools_ttl_ms, catalog.tools_expires_at,
       catalog.fetched_at, catalog.refresh_owner_token, catalog.refresh_lease_expires_at,
       catalog.created_at, catalog.updated_at, catalog.refresh_error
FROM mcp_server_catalogs catalog
WHERE catalog.org_id = sqlc.arg(org_id)
  AND catalog.endpoint_url = sqlc.arg(endpoint_url)
  AND catalog.secret_id IS NOT DISTINCT FROM sqlc.narg(secret_id)::uuid
  AND catalog.secret_version_id IS NOT DISTINCT FROM sqlc.narg(secret_version_id)::uuid
  AND catalog.aws_region = sqlc.arg(aws_region)
  AND catalog.aws_service = sqlc.arg(aws_service);

-- name: ListAgentMCPConnectionCatalogs :many
SELECT catalog.id, catalog.org_id, catalog.endpoint_url, catalog.secret_id,
       catalog.secret_version_id, catalog.aws_region, catalog.aws_service,
       catalog.revision, catalog.protocol_version, catalog.server_capabilities,
       catalog.server_info, catalog.instructions, catalog.discover_cache_scope,
       catalog.discover_ttl_ms, catalog.discover_expires_at, catalog.tools_snapshot,
       catalog.tools_cache_scope, catalog.tools_ttl_ms, catalog.tools_expires_at,
       catalog.fetched_at, catalog.refresh_owner_token, catalog.refresh_lease_expires_at,
       catalog.created_at, catalog.updated_at, catalog.refresh_error
FROM mcp_server_catalogs catalog
JOIN agent_mcp_connections connection ON connection.catalog_id = catalog.id
JOIN agents agent ON agent.id = connection.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND connection.agent_id = sqlc.arg(agent_id)
ORDER BY catalog.id;

-- name: AcquireMCPServerCatalogRefreshLease :one
INSERT INTO mcp_server_catalogs(
  org_id, endpoint_url, secret_id, secret_version_id, aws_region, aws_service,
  refresh_owner_token, refresh_lease_expires_at, created_at, updated_at
)
VALUES (
  sqlc.arg(org_id), sqlc.arg(endpoint_url), sqlc.narg(secret_id)::uuid,
  sqlc.narg(secret_version_id)::uuid, sqlc.arg(aws_region), sqlc.arg(aws_service),
  sqlc.arg(owner_token)::uuid,
  statement_timestamp() + sqlc.arg(ttl_milliseconds)::bigint * interval '1 millisecond',
  statement_timestamp(), statement_timestamp()
)
ON CONFLICT ON CONSTRAINT mcp_server_catalogs_identity_key DO UPDATE
SET refresh_owner_token = CASE
      WHEN mcp_server_catalogs.refresh_lease_expires_at IS NULL
        OR mcp_server_catalogs.refresh_lease_expires_at <= statement_timestamp()
      THEN EXCLUDED.refresh_owner_token
      ELSE mcp_server_catalogs.refresh_owner_token
    END,
    refresh_lease_expires_at = CASE
      WHEN mcp_server_catalogs.refresh_lease_expires_at IS NULL
        OR mcp_server_catalogs.refresh_lease_expires_at <= statement_timestamp()
      THEN EXCLUDED.refresh_lease_expires_at
      ELSE mcp_server_catalogs.refresh_lease_expires_at
    END,
    updated_at = CASE
      WHEN mcp_server_catalogs.refresh_lease_expires_at IS NULL
        OR mcp_server_catalogs.refresh_lease_expires_at <= statement_timestamp()
      THEN EXCLUDED.updated_at
      ELSE mcp_server_catalogs.updated_at
    END
RETURNING id, org_id, endpoint_url, secret_id, secret_version_id, aws_region, aws_service,
          revision, protocol_version, server_capabilities, server_info, instructions,
          discover_cache_scope, discover_ttl_ms, discover_expires_at, tools_snapshot,
          tools_cache_scope, tools_ttl_ms, tools_expires_at, fetched_at,
          refresh_owner_token, refresh_lease_expires_at, created_at, updated_at, refresh_error;

-- name: MarkMCPServerCatalogFetched :one
UPDATE mcp_server_catalogs catalog
SET revision = catalog.revision + 1,
    refresh_error = '',
    protocol_version = sqlc.arg(protocol_version),
    server_capabilities = sqlc.arg(server_capabilities),
    server_info = sqlc.arg(server_info),
    instructions = sqlc.arg(instructions),
    discover_cache_scope = sqlc.arg(discover_cache_scope),
    discover_ttl_ms = sqlc.arg(discover_ttl_ms),
    discover_expires_at = statement_timestamp() + sqlc.arg(discover_fresh_for_milliseconds)::bigint * interval '1 millisecond',
    tools_snapshot = sqlc.arg(tools_snapshot),
    tools_cache_scope = sqlc.arg(tools_cache_scope),
    tools_ttl_ms = sqlc.arg(tools_ttl_ms),
    tools_expires_at = statement_timestamp() + sqlc.arg(tools_fresh_for_milliseconds)::bigint * interval '1 millisecond',
    fetched_at = statement_timestamp(),
    refresh_owner_token = NULL,
    refresh_lease_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE catalog.org_id = sqlc.arg(org_id)
  AND catalog.id = sqlc.arg(id)
  AND catalog.refresh_owner_token = sqlc.arg(owner_token)::uuid
  AND (catalog.protocol_version < '2026-07-28' OR sqlc.arg(protocol_version)::text >= '2026-07-28')
RETURNING catalog.id, catalog.org_id, catalog.endpoint_url, catalog.secret_id,
          catalog.secret_version_id, catalog.aws_region, catalog.aws_service,
          catalog.revision, catalog.protocol_version, catalog.server_capabilities,
          catalog.server_info, catalog.instructions, catalog.discover_cache_scope,
          catalog.discover_ttl_ms, catalog.discover_expires_at, catalog.tools_snapshot,
          catalog.tools_cache_scope, catalog.tools_ttl_ms, catalog.tools_expires_at,
          catalog.fetched_at, catalog.refresh_owner_token, catalog.refresh_lease_expires_at,
          catalog.created_at, catalog.updated_at, catalog.refresh_error;

-- name: ReleaseMCPServerCatalogRefreshLease :exec
UPDATE mcp_server_catalogs catalog
SET refresh_owner_token = NULL,
    refresh_lease_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE catalog.org_id = sqlc.arg(org_id)
  AND catalog.id = sqlc.arg(id)
  AND catalog.refresh_owner_token = sqlc.arg(owner_token)::uuid;

-- name: MarkMCPServerCatalogRefreshFailed :execrows
UPDATE mcp_server_catalogs catalog
SET refresh_error = sqlc.arg(refresh_error),
    refresh_owner_token = NULL,
    refresh_lease_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE catalog.org_id = sqlc.arg(org_id)
  AND catalog.id = sqlc.arg(id)
  AND catalog.refresh_owner_token = sqlc.arg(owner_token)::uuid;

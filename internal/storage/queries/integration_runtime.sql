-- name: ListPersistentIntegrations :many
WITH integration_types AS (
    SELECT DISTINCT unnest(sqlc.arg(integration_types)::text[]) AS integration_type
)
SELECT page.id, page.project_id
FROM integration_types
CROSS JOIN LATERAL (
    SELECT integration.id, integration.project_id
    FROM project_integrations integration
    JOIN projects project ON project.id = integration.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE integration.integration_type = integration_types.integration_type AND integration.state = 'active' AND integration.deleted_at IS NULL
      AND project.deleted_at IS NULL AND org.deleted_at IS NULL
      AND (sqlc.narg(after_id)::uuid IS NULL OR integration.id > sqlc.narg(after_id)::uuid)
    ORDER BY integration.id
    LIMIT sqlc.arg(row_limit)
) page
ORDER BY page.id
LIMIT sqlc.arg(row_limit);

-- name: CountUnclaimedIntegrationRuntimes :one
SELECT count(*)
FROM project_integrations integration
JOIN projects project ON project.id = integration.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = integration.org_id AND org.deleted_at IS NULL
WHERE integration.integration_type = ANY(sqlc.arg(integration_types)::text[])
  AND integration.state = 'active' AND integration.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM integration_runtime runtime
      WHERE runtime.project_id = integration.project_id AND runtime.integration_id = integration.id
        AND runtime.runtime_key = sqlc.arg(runtime_key)
        AND runtime.claim_expires_at > statement_timestamp()
  );

-- name: GetIntegrationRuntimeFailure :one
SELECT coalesce(runtime.last_error, '') AS message, runtime.available_at AS retry_at
FROM integration_runtime runtime
JOIN project_integrations integration ON integration.project_id = runtime.project_id AND integration.id = runtime.integration_id
JOIN projects project ON project.id = integration.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = integration.org_id AND org.deleted_at IS NULL
JOIN secrets secret ON secret.org_id = integration.org_id AND secret.id = integration.credential_secret_id
    AND secret.deleted_at IS NULL AND secret.management_kind = 'tenant'
    AND secret.current_version_id = runtime.credential_version_id
WHERE integration.project_id = sqlc.arg(project_id) AND integration.id = sqlc.arg(integration_id)
  AND integration.state = 'active' AND integration.deleted_at IS NULL
  AND integration.setup_revision = sqlc.arg(setup_revision) AND runtime.setup_revision = integration.setup_revision
  AND runtime.last_error IS NOT NULL AND runtime.last_error <> ''
  AND ((secret.owner_kind = 'project' AND secret.owner_project_id = integration.project_id)
       OR EXISTS (SELECT 1 FROM secret_grants grant_row
           WHERE grant_row.org_id = integration.org_id AND grant_row.secret_id = secret.id
             AND grant_row.target_project_id = integration.project_id))
ORDER BY runtime.updated_at DESC, runtime.runtime_key
LIMIT 1;

-- Skip the row lock here to avoid contending with heartbeat; ClaimIntegrationRuntime rechecks ownership.
-- name: IntegrationRuntimeClaimable :one
SELECT NOT EXISTS (
    SELECT 1 FROM integration_runtime
    WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND runtime_key = sqlc.arg(runtime_key)
      AND (claim_expires_at > statement_timestamp()
           OR (available_at > statement_timestamp()
               AND setup_revision = sqlc.arg(setup_revision)
               AND credential_version_id = sqlc.arg(credential_version_id)))
) AS claimable;

-- name: ClaimIntegrationRuntime :one
INSERT INTO integration_runtime(project_id, integration_id, runtime_key, setup_revision,
    credential_version_id, claim_token, claim_expires_at)
SELECT integration.project_id, integration.id, sqlc.arg(runtime_key), integration.setup_revision,
    sqlc.arg(credential_version_id), sqlc.arg(claim_token),
    statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
FROM project_integrations integration
WHERE integration.project_id = sqlc.arg(project_id) AND integration.id = sqlc.arg(integration_id)
  AND integration.setup_revision = sqlc.arg(setup_revision) AND integration.deleted_at IS NULL
ON CONFLICT (project_id, integration_id, runtime_key) DO UPDATE
SET checkpoint = CASE WHEN integration_runtime.setup_revision = EXCLUDED.setup_revision
                       AND integration_runtime.credential_version_id = EXCLUDED.credential_version_id
                      THEN integration_runtime.checkpoint ELSE NULL END,
    setup_revision = EXCLUDED.setup_revision, credential_version_id = EXCLUDED.credential_version_id,
    claim_token = EXCLUDED.claim_token, claim_expires_at = EXCLUDED.claim_expires_at,
    last_error = NULL, updated_at = statement_timestamp()
WHERE (integration_runtime.claim_expires_at IS NULL OR integration_runtime.claim_expires_at <= statement_timestamp())
  AND (integration_runtime.available_at <= statement_timestamp()
       OR integration_runtime.setup_revision <> EXCLUDED.setup_revision
       OR integration_runtime.credential_version_id <> EXCLUDED.credential_version_id)
RETURNING checkpoint, claim_expires_at;

-- Use a fresh statement after locking: statement_timestamp() does not advance during lock waits.
-- name: LockIntegrationRuntime :one
SELECT runtime_key FROM integration_runtime
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND runtime_key = sqlc.arg(runtime_key)
FOR UPDATE;

-- name: ReadIntegrationRuntimeLease :one
SELECT checkpoint, claim_expires_at
FROM integration_runtime
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND runtime_key = sqlc.arg(runtime_key)
  AND setup_revision = sqlc.arg(setup_revision) AND credential_version_id = sqlc.arg(credential_version_id)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- name: RenewIntegrationRuntime :execrows
UPDATE integration_runtime
SET claim_expires_at = statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond', updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- name: CheckpointIntegrationRuntime :execrows
UPDATE integration_runtime
SET checkpoint = sqlc.narg(checkpoint)::jsonb, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- name: ReleaseIntegrationRuntime :execrows
UPDATE integration_runtime
SET claim_token = NULL, claim_expires_at = NULL,
    available_at = statement_timestamp() + sqlc.arg(delay_milliseconds)::bigint * interval '1 millisecond',
    last_error = sqlc.narg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

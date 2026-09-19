-- Only actual hosted persistent transports are enumerated. Cursor scanning keeps
-- a busy prefix owned by other workers from hiding connections later in the set.
-- name: ListPersistentIntegrationConnections :many
SELECT connection.id, connection.project_id
FROM integration_connections connection
JOIN projects project ON project.id = connection.project_id
JOIN orgs org ON org.id = project.org_id
WHERE connection.provider = 'discord' AND connection.state = 'active' AND connection.deleted_at IS NULL
  AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  AND (sqlc.narg(after_id)::uuid IS NULL OR connection.id > sqlc.narg(after_id)::uuid)
ORDER BY connection.id
LIMIT sqlc.arg(row_limit);

-- This unlocked hint avoids contending with the owner's heartbeat on every
-- discovery scan. The claim below remains the authoritative atomic decision.
-- name: IntegrationConnectionRuntimeClaimable :one
SELECT NOT EXISTS (
    SELECT 1 FROM integration_connection_runtime
    WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id) AND runtime_key = sqlc.arg(runtime_key)
      AND (claim_expires_at > statement_timestamp()
           OR (available_at > statement_timestamp()
               AND connection_updated_at = sqlc.arg(connection_updated_at)
               AND credential_version_id = sqlc.arg(credential_version_id)))
) AS claimable;

-- Caller holds project/connection/secret reference gates and validates revisions
-- before locking the runtime. Token changes on every claim; stale owners cannot
-- publish checkpoints even when the same process reacquires the unit later.
-- name: ClaimIntegrationConnectionRuntime :one
INSERT INTO integration_connection_runtime(project_id, connection_id, runtime_key, connection_updated_at,
    credential_version_id, claim_token, claim_expires_at)
SELECT connection.project_id, connection.id, sqlc.arg(runtime_key), connection.updated_at,
    sqlc.arg(credential_version_id), sqlc.arg(claim_token),
    statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
FROM integration_connections connection
WHERE connection.project_id = sqlc.arg(project_id) AND connection.id = sqlc.arg(connection_id)
  AND connection.updated_at = sqlc.arg(connection_updated_at) AND connection.deleted_at IS NULL
ON CONFLICT (project_id, connection_id, runtime_key) DO UPDATE
SET checkpoint = CASE WHEN integration_connection_runtime.connection_updated_at = EXCLUDED.connection_updated_at
                       AND integration_connection_runtime.credential_version_id = EXCLUDED.credential_version_id
                      THEN integration_connection_runtime.checkpoint ELSE NULL END,
    connection_updated_at = EXCLUDED.connection_updated_at, credential_version_id = EXCLUDED.credential_version_id,
    claim_token = EXCLUDED.claim_token, claim_expires_at = EXCLUDED.claim_expires_at,
    last_error = NULL, updated_at = statement_timestamp()
WHERE (integration_connection_runtime.claim_expires_at IS NULL OR integration_connection_runtime.claim_expires_at <= statement_timestamp())
  AND (integration_connection_runtime.available_at <= statement_timestamp()
       OR integration_connection_runtime.connection_updated_at <> EXCLUDED.connection_updated_at
       OR integration_connection_runtime.credential_version_id <> EXCLUDED.credential_version_id)
RETURNING checkpoint, claim_expires_at;

-- Lock then re-read in a fresh statement: the lease can expire while waiting.
-- name: LockIntegrationConnectionRuntime :one
SELECT runtime_key FROM integration_connection_runtime
WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id) AND runtime_key = sqlc.arg(runtime_key)
FOR UPDATE;

-- name: ReadIntegrationConnectionRuntimeLease :one
SELECT checkpoint, claim_expires_at
FROM integration_connection_runtime
WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id) AND runtime_key = sqlc.arg(runtime_key)
  AND connection_updated_at = sqlc.arg(connection_updated_at) AND credential_version_id = sqlc.arg(credential_version_id)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- name: RenewIntegrationConnectionRuntime :execrows
UPDATE integration_connection_runtime
SET claim_expires_at = statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond', updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- name: CheckpointIntegrationConnectionRuntime :execrows
UPDATE integration_connection_runtime
SET checkpoint = sqlc.narg(checkpoint)::jsonb, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- Release never publishes a checkpoint. It can relinquish a revoked parent's
-- unit; the exact unexpired token still prevents releasing a replacement owner.
-- name: ReleaseIntegrationConnectionRuntime :execrows
UPDATE integration_connection_runtime
SET claim_token = NULL, claim_expires_at = NULL,
    available_at = statement_timestamp() + sqlc.arg(delay_milliseconds)::bigint * interval '1 millisecond',
    last_error = sqlc.narg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

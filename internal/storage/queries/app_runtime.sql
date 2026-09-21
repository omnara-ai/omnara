-- Only actual hosted persistent transports are enumerated. Cursor scanning keeps
-- a busy prefix owned by other workers from hiding apps later in the set.
-- name: ListPersistentApps :many
WITH app_types AS (
    SELECT DISTINCT unnest(sqlc.arg(app_types)::text[]) AS app_type
)
SELECT page.id, page.project_id
FROM app_types
CROSS JOIN LATERAL (
    -- Limit each ordered type scan before merging the cursor's next page.
    SELECT app.id, app.project_id
    FROM project_apps app
    JOIN projects project ON project.id = app.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE app.app_type = app_types.app_type AND app.state = 'active' AND app.deleted_at IS NULL
      AND project.deleted_at IS NULL AND org.deleted_at IS NULL
      AND (sqlc.narg(after_id)::uuid IS NULL OR app.id > sqlc.narg(after_id)::uuid)
    ORDER BY app.id
    LIMIT sqlc.arg(row_limit)
) page
ORDER BY page.id
LIMIT sqlc.arg(row_limit);

-- This unlocked hint avoids contending with the owner's heartbeat on every
-- discovery scan. The claim below remains the authoritative atomic decision.
-- name: AppRuntimeClaimable :one
SELECT NOT EXISTS (
    SELECT 1 FROM app_runtime
    WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND runtime_key = sqlc.arg(runtime_key)
      AND (claim_expires_at > statement_timestamp()
           OR (available_at > statement_timestamp()
               AND setup_revision = sqlc.arg(setup_revision)
               AND credential_version_id = sqlc.arg(credential_version_id)))
) AS claimable;

-- Caller holds project/app/secret reference gates and validates revisions
-- before locking the runtime. Token changes on every claim; stale owners cannot
-- publish checkpoints even when the same process reacquires the unit later.
-- Only setup/credential revisions invalidate checkpoints or bypass backoff;
-- unrelated app settings edits leave the transport session intact.
-- name: ClaimAppRuntime :one
INSERT INTO app_runtime(project_id, app_id, runtime_key, setup_revision,
    credential_version_id, claim_token, claim_expires_at)
SELECT app.project_id, app.id, sqlc.arg(runtime_key), app.setup_revision,
    sqlc.arg(credential_version_id), sqlc.arg(claim_token),
    statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
FROM project_apps app
WHERE app.project_id = sqlc.arg(project_id) AND app.id = sqlc.arg(app_id)
  AND app.setup_revision = sqlc.arg(setup_revision) AND app.deleted_at IS NULL
ON CONFLICT (project_id, app_id, runtime_key) DO UPDATE
SET checkpoint = CASE WHEN app_runtime.setup_revision = EXCLUDED.setup_revision
                       AND app_runtime.credential_version_id = EXCLUDED.credential_version_id
                      THEN app_runtime.checkpoint ELSE NULL END,
    setup_revision = EXCLUDED.setup_revision, credential_version_id = EXCLUDED.credential_version_id,
    claim_token = EXCLUDED.claim_token, claim_expires_at = EXCLUDED.claim_expires_at,
    last_error = NULL, updated_at = statement_timestamp()
WHERE (app_runtime.claim_expires_at IS NULL OR app_runtime.claim_expires_at <= statement_timestamp())
  AND (app_runtime.available_at <= statement_timestamp()
       OR app_runtime.setup_revision <> EXCLUDED.setup_revision
       OR app_runtime.credential_version_id <> EXCLUDED.credential_version_id)
RETURNING checkpoint, claim_expires_at;

-- Lock then re-read in a fresh statement: the lease can expire while waiting.
-- name: LockAppRuntime :one
SELECT runtime_key FROM app_runtime
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND runtime_key = sqlc.arg(runtime_key)
FOR UPDATE;

-- name: ReadAppRuntimeLease :one
SELECT checkpoint, claim_expires_at
FROM app_runtime
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND runtime_key = sqlc.arg(runtime_key)
  AND setup_revision = sqlc.arg(setup_revision) AND credential_version_id = sqlc.arg(credential_version_id)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- name: RenewAppRuntime :execrows
UPDATE app_runtime
SET claim_expires_at = statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond', updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- name: CheckpointAppRuntime :execrows
UPDATE app_runtime
SET checkpoint = sqlc.narg(checkpoint)::jsonb, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

-- Release never publishes a checkpoint. It can relinquish a revoked parent's
-- unit; the exact unexpired token still prevents releasing a replacement owner.
-- name: ReleaseAppRuntime :execrows
UPDATE app_runtime
SET claim_token = NULL, claim_expires_at = NULL,
    available_at = statement_timestamp() + sqlc.arg(delay_milliseconds)::bigint * interval '1 millisecond',
    last_error = sqlc.narg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND runtime_key = sqlc.arg(runtime_key)
  AND claim_token = sqlc.arg(claim_token)::uuid AND claim_expires_at > statement_timestamp();

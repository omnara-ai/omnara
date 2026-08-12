-- name: CreateOrgAPIKey :one
INSERT INTO org_api_keys(org_id, name, token_id, token_hash, created_by_user_id, created_at, updated_at)
VALUES (sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(token_id), sqlc.arg(token_hash), sqlc.arg(created_by_user_id), transaction_timestamp(), transaction_timestamp())
RETURNING id, org_id, name, token_id, token_hash, created_by_user_id, created_at, updated_at, last_used_at, revoked_at;

-- name: AuthenticateOrgAPIKey :one
WITH authenticated AS MATERIALIZED (
  SELECT k.id AS org_api_key_id, k.org_id, k.last_used_at
  FROM org_api_keys k
  WHERE k.token_hash = sqlc.arg(token_hash)
    AND k.revoked_at IS NULL
  LIMIT 1
), touched AS (
  UPDATE org_api_keys key
  SET last_used_at = transaction_timestamp()
  FROM authenticated
  WHERE key.id = authenticated.org_api_key_id
    AND key.revoked_at IS NULL
    AND (
      key.last_used_at IS NULL
      OR key.last_used_at < transaction_timestamp() - (sqlc.arg(touch_interval_seconds)::bigint * interval '1 second')
    )
  RETURNING key.id
)
SELECT org_api_key_id, org_id, last_used_at
FROM authenticated;

-- name: GetOrgAPIKey :one
SELECT k.id, k.org_id, k.name, k.token_id, k.token_hash, k.created_by_user_id,
       k.created_at, k.updated_at, k.last_used_at, k.revoked_at,
       coalesce(om.role, '') AS org_role
FROM org_api_keys k
LEFT JOIN org_memberships om ON om.org_id = k.org_id AND om.org_api_key_id = k.id
WHERE k.org_id = sqlc.arg(org_id) AND k.id = sqlc.arg(id);

-- name: LockOrgAPIKeyForUpdate :one
SELECT id
FROM org_api_keys
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ListOrgAPIKeysForOrg :many
SELECT k.id, k.org_id, k.name, k.token_id, k.token_hash, k.created_by_user_id,
       k.created_at, k.updated_at, k.last_used_at, k.revoked_at,
       coalesce(om.role, '') AS org_role
FROM org_api_keys k
LEFT JOIN org_memberships om ON om.org_id = k.org_id AND om.org_api_key_id = k.id
WHERE k.org_id = sqlc.arg(org_id)
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (k.created_at, k.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY k.created_at DESC, k.id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: RenameOrgAPIKey :one
UPDATE org_api_keys
SET name = sqlc.arg(name),
    updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING id, org_id, name, token_id, token_hash, created_by_user_id, created_at, updated_at, last_used_at, revoked_at;

-- name: RevokeOrgAPIKey :one
UPDATE org_api_keys
SET updated_at = CASE WHEN revoked_at IS NULL THEN transaction_timestamp() ELSE updated_at END,
    revoked_at = coalesce(revoked_at, transaction_timestamp())
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING id, org_id, name, token_id, token_hash, created_by_user_id, created_at, updated_at, last_used_at, revoked_at;

-- name: DeleteOrganizationOrgAPIKeys :exec
DELETE FROM org_api_keys
WHERE org_id = sqlc.arg(org_id);

-- name: TouchOrgAPIKeyUpdatedAt :one
UPDATE org_api_keys
SET updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING id, org_id, name, token_id, token_hash, created_by_user_id, created_at, updated_at, last_used_at, revoked_at;

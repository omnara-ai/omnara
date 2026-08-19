-- name: InsertSecret :one
INSERT INTO secrets(id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id, name, kind, metadata, current_version_id, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(management_kind), sqlc.arg(owner_kind), sqlc.narg(owner_project_id), sqlc.narg(owner_user_id), sqlc.arg(name), sqlc.arg(kind), sqlc.arg(metadata), sqlc.arg(current_version_id), transaction_timestamp(), transaction_timestamp())
RETURNING id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id, name, kind, metadata, current_version_id, created_at, updated_at;

-- name: InsertSecretVersion :one
INSERT INTO secret_versions(id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at, mcp_oauth_flow_id, oauth_access_token_expires_at)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(secret_id), sqlc.arg(version_number), sqlc.arg(payload_keys), sqlc.arg(encryption_scheme), sqlc.arg(key_id), sqlc.arg(dek_wrapped_by), sqlc.arg(encrypted_dek), sqlc.arg(encrypted_dek_nonce), sqlc.arg(nonce), sqlc.arg(ciphertext), statement_timestamp(), sqlc.narg(mcp_oauth_flow_id), CASE WHEN sqlc.arg(oauth_access_token_expires)::boolean THEN statement_timestamp() + make_interval(secs => sqlc.arg(oauth_access_token_ttl_seconds)::bigint) END)
RETURNING id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at;

-- name: MCPOAuthFlowConsumed :one
SELECT EXISTS (
    SELECT 1 FROM secret_versions WHERE mcp_oauth_flow_id = sqlc.arg(mcp_oauth_flow_id)
) AS consumed;

-- name: AcquireSecretOAuthRefreshLease :one
INSERT INTO secret_oauth_refresh_leases(org_id, secret_id, owner_token, expected_secret_version_id, expires_at, updated_at)
VALUES (sqlc.arg(org_id), sqlc.arg(secret_id), sqlc.arg(owner_token), sqlc.arg(expected_secret_version_id), statement_timestamp() + sqlc.arg(ttl_milliseconds)::bigint * interval '1 millisecond', statement_timestamp())
ON CONFLICT (org_id, secret_id) DO UPDATE
SET owner_token = EXCLUDED.owner_token,
    expected_secret_version_id = EXCLUDED.expected_secret_version_id,
    expires_at = EXCLUDED.expires_at,
    updated_at = EXCLUDED.updated_at
WHERE secret_oauth_refresh_leases.expires_at <= statement_timestamp()
RETURNING owner_token, expected_secret_version_id;

-- name: LockSecretOAuthRefreshLease :one
SELECT secret_id
FROM secret_oauth_refresh_leases
WHERE org_id = sqlc.arg(org_id)
  AND secret_id = sqlc.arg(secret_id)
  AND owner_token = sqlc.arg(owner_token)
  AND expected_secret_version_id = sqlc.arg(expected_secret_version_id)
FOR UPDATE;

-- name: SecretOAuthRefreshLeaseActive :one
SELECT expires_at > statement_timestamp() AS active
FROM secret_oauth_refresh_leases
WHERE org_id = sqlc.arg(org_id)
  AND secret_id = sqlc.arg(secret_id)
  AND owner_token = sqlc.arg(owner_token);

-- name: ReleaseSecretOAuthRefreshLease :exec
DELETE FROM secret_oauth_refresh_leases
WHERE org_id = sqlc.arg(org_id)
  AND secret_id = sqlc.arg(secret_id)
  AND owner_token = sqlc.arg(owner_token);

-- name: SetSecretCurrentVersion :one
UPDATE secrets
SET current_version_id = sqlc.arg(current_version_id), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND management_kind = 'tenant' AND deleted_at IS NULL
RETURNING id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id, name, kind, metadata, current_version_id, created_at, updated_at;

-- name: NextSecretVersionNumber :one
SELECT coalesce(max(version_number), 0)::integer + 1 AS version_number
FROM secret_versions
WHERE secret_id = sqlc.arg(secret_id);

-- name: GetSecret :one
SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, v.version_number AS current_version_number, v.payload_keys, s.created_at, s.updated_at
FROM secrets s
JOIN secret_versions v
  ON v.secret_id = s.id
 AND v.id = s.current_version_id
WHERE s.org_id = sqlc.arg(org_id) AND s.id = sqlc.arg(id) AND s.deleted_at IS NULL;

-- name: GetSecretByOwnerName :one
SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, v.version_number AS current_version_number, v.payload_keys, s.created_at, s.updated_at
FROM secrets s
JOIN secret_versions v
  ON v.secret_id = s.id
 AND v.id = s.current_version_id
WHERE s.org_id = sqlc.arg(org_id)
  AND s.owner_kind = sqlc.arg(owner_kind)
  AND s.owner_project_id IS NOT DISTINCT FROM sqlc.narg(owner_project_id)
  AND s.owner_user_id IS NOT DISTINCT FROM sqlc.narg(owner_user_id)
  AND s.name = sqlc.arg(name)
  AND s.deleted_at IS NULL;

-- name: LockSecret :one
-- @sqlc-vet-disable secrets-deleted-at
-- Lock helper: follow-up reads enforce liveness.
SELECT id
FROM secrets
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: GetSecretVersion :one
SELECT id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at,
       (oauth_access_token_expires_at IS NOT NULL)::boolean AS oauth_access_token_expires,
       coalesce(greatest(extract(epoch FROM oauth_access_token_expires_at - statement_timestamp()), 0), 0)::double precision AS oauth_access_token_remaining_seconds
FROM secret_versions
WHERE org_id = sqlc.arg(org_id) AND secret_id = sqlc.arg(secret_id) AND id = sqlc.arg(id);

-- name: ListSecretVersionsByKeyID :many
-- @sqlc-vet-disable secrets-deleted-at
-- Key rewrap sweeps versions of soft-deleted secrets too.
SELECT v.id, v.org_id, v.secret_id, v.version_number, v.payload_keys, v.encryption_scheme, v.key_id, v.dek_wrapped_by, v.encrypted_dek, v.encrypted_dek_nonce, v.nonce, v.ciphertext, v.created_at, s.kind
FROM secret_versions v
JOIN secrets s
  ON s.org_id = v.org_id
 AND s.id = v.secret_id
WHERE v.key_id = sqlc.arg(key_id)
ORDER BY v.org_id, v.secret_id, v.version_number, v.id;

-- name: CountSecretVersionsByKeyID :one
SELECT count(*)::integer AS count
FROM secret_versions
WHERE key_id = sqlc.arg(key_id);

-- name: UpdateSecretVersionKeyEnvelope :one
UPDATE secret_versions
SET key_id = sqlc.arg(key_id),
    dek_wrapped_by = sqlc.arg(dek_wrapped_by),
    encrypted_dek = sqlc.arg(encrypted_dek),
    encrypted_dek_nonce = sqlc.arg(encrypted_dek_nonce)
WHERE org_id = sqlc.arg(org_id)
  AND secret_id = sqlc.arg(secret_id)
  AND id = sqlc.arg(id)
  AND key_id = sqlc.arg(previous_key_id)
RETURNING id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at;

-- name: ListVisibleOwnedSecrets :many
WITH listed AS (
SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, v.version_number AS current_version_number, v.payload_keys, s.created_at, s.updated_at,
  CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(s.name) WHEN 'created_at' THEN to_char(s.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'updated_at' THEN to_char(s.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'owner_kind' THEN s.owner_kind WHEN 'kind' THEN s.kind END::text AS sort_key,
  false AS sort_is_null
FROM secrets s
JOIN secret_versions v
  ON v.secret_id = s.id
 AND v.id = s.current_version_id
WHERE s.org_id = sqlc.arg(org_id)
  AND s.deleted_at IS NULL
  AND s.metadata @> sqlc.arg(metadata_filter)::jsonb
	AND (sqlc.arg(name_pattern)::text = '' OR s.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
	AND (COALESCE(cardinality(sqlc.arg(kinds)::text[]), 0) = 0 OR s.kind = ANY(sqlc.arg(kinds)::text[]))
  AND (sqlc.arg(owner_kind)::text = '' OR s.owner_kind = sqlc.arg(owner_kind)::text)
  AND (sqlc.narg(owner_project_id)::uuid IS NULL OR s.owner_project_id = sqlc.narg(owner_project_id)::uuid)
  AND (sqlc.narg(mcp_oauth_flow_id)::uuid IS NULL OR EXISTS (
    SELECT 1
    FROM secret_versions fv
    WHERE fv.secret_id = s.id
      AND fv.mcp_oauth_flow_id = sqlc.narg(mcp_oauth_flow_id)::uuid
  ))
  AND (
    (s.owner_kind = 'org' AND EXISTS (
      SELECT 1
      FROM org_memberships om
      WHERE om.org_id = s.org_id
        AND (
          (sqlc.narg(user_id)::uuid IS NOT NULL AND om.user_id = sqlc.narg(user_id)::uuid)
          OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND om.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
        )
        AND om.role IN ('owner', 'admin')
    ))
    OR (s.owner_kind = 'project' AND EXISTS (
      SELECT 1
      FROM principal_project_authorization_roles roles
      WHERE roles.org_id = s.org_id
        AND roles.project_id = s.owner_project_id
        AND (
          (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
          OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
        )
        AND roles.role IN ('admin', 'developer')
    ))
    OR (s.owner_kind = 'user' AND s.owner_user_id = sqlc.narg(user_id)::uuid AND EXISTS (
      SELECT 1
      FROM org_memberships om
      WHERE om.org_id = s.org_id
        AND om.user_id = sqlc.narg(user_id)::uuid
    ))
  )
)
SELECT id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id,
 name, kind, metadata, current_version_id, current_version_number,
 payload_keys, created_at, updated_at, sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
 OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListProjectAvailableSecrets :many
WITH project_owned AS (
  SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, v.version_number AS current_version_number, v.payload_keys, NULL::uuid AS grant_id, 'direct'::text AS availability_source, s.created_at, s.updated_at
  FROM secrets s
  JOIN secret_versions v
    ON v.secret_id = s.id
   AND v.id = s.current_version_id
  WHERE s.org_id = sqlc.arg(org_id)
    AND s.deleted_at IS NULL
    AND s.management_kind = 'tenant'
    AND s.owner_kind = 'project'
    AND s.owner_project_id = sqlc.arg(project_id)::uuid
),
granted AS (
  SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, v.version_number AS current_version_number, v.payload_keys, sg.id AS grant_id, 'grant'::text AS availability_source, s.created_at, s.updated_at
  FROM secret_grants sg
  JOIN secrets s
    ON s.org_id = sg.org_id
   AND s.id = sg.secret_id
  JOIN secret_versions v
    ON v.secret_id = s.id
   AND v.id = s.current_version_id
  WHERE sg.org_id = sqlc.arg(org_id)
    AND sg.target_project_id = sqlc.arg(project_id)::uuid
    AND s.deleted_at IS NULL
    AND s.management_kind = 'tenant'
),
available AS (
  SELECT id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id, name, kind, metadata, current_version_id, current_version_number, payload_keys, grant_id, availability_source, created_at, updated_at
  FROM project_owned
  UNION ALL
  SELECT id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id, name, kind, metadata, current_version_id, current_version_number, payload_keys, grant_id, availability_source, created_at, updated_at
  FROM granted
), listed AS (
 SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id,
  s.owner_user_id, s.name, s.kind, s.metadata,
  s.current_version_id, s.current_version_number,
  s.payload_keys, s.grant_id, s.availability_source,
  s.created_at, s.updated_at,
  CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(s.name) WHEN 'created_at' THEN to_char(s.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'updated_at' THEN to_char(s.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'owner_kind' THEN s.owner_kind WHEN 'kind' THEN s.kind WHEN 'availability_source' THEN s.availability_source END::text AS sort_key,
  false AS sort_is_null
 FROM available s
 WHERE s.metadata @> sqlc.arg(metadata_filter)::jsonb
  AND (sqlc.arg(name_pattern)::text = '' OR s.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.arg(owner_kind)::text = '' OR s.owner_kind = sqlc.arg(owner_kind)::text)
  AND (COALESCE(cardinality(sqlc.arg(availability_sources)::text[]), 0) = 0 OR s.availability_source = ANY(sqlc.arg(availability_sources)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(kinds)::text[]), 0) = 0 OR s.kind = ANY(sqlc.arg(kinds)::text[]))
)
SELECT id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id,
 name, kind, metadata, current_version_id, current_version_number,
 payload_keys, grant_id, availability_source, created_at, updated_at,
 sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
 OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: GetProjectAvailableSecret :one
WITH available AS (
  SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, v.version_number AS current_version_number, v.payload_keys, NULL::uuid AS grant_id, s.created_at, s.updated_at
  FROM secrets s
  JOIN secret_versions v
    ON v.secret_id = s.id
   AND v.id = s.current_version_id
  WHERE s.org_id = sqlc.arg(org_id)
    AND s.deleted_at IS NULL
    AND s.id = sqlc.arg(secret_id)
    AND s.management_kind = 'tenant'
    AND s.owner_kind = 'project'
    AND s.owner_project_id = sqlc.arg(project_id)::uuid
  UNION ALL
  SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, v.version_number AS current_version_number, v.payload_keys, sg.id AS grant_id, s.created_at, s.updated_at
  FROM secret_grants sg
  JOIN secrets s
    ON s.org_id = sg.org_id
   AND s.id = sg.secret_id
  JOIN secret_versions v
    ON v.secret_id = s.id
   AND v.id = s.current_version_id
  WHERE sg.org_id = sqlc.arg(org_id)
    AND sg.target_project_id = sqlc.arg(project_id)::uuid
    AND sg.secret_id = sqlc.arg(secret_id)
    AND s.deleted_at IS NULL
    AND s.management_kind = 'tenant'
)
SELECT s.id, s.org_id, s.management_kind, s.owner_kind, s.owner_project_id, s.owner_user_id, s.name, s.kind, s.metadata, s.current_version_id, s.current_version_number, s.payload_keys, s.grant_id, s.created_at, s.updated_at
FROM available s;

-- name: UpdateSecretMetadata :one
UPDATE secrets
SET name = sqlc.arg(name), metadata = sqlc.arg(metadata), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
  AND management_kind = 'tenant' AND deleted_at IS NULL
RETURNING id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id, name, kind, metadata, current_version_id, created_at, updated_at;

-- name: DeleteSecret :one
-- The secret row is soft-deleted; its versions (the ciphertext) are destroyed
-- by DeleteSecretVersions in the same transaction.
UPDATE secrets
SET deleted_at = statement_timestamp(),
    current_version_id = NULL,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
  AND management_kind = 'tenant' AND deleted_at IS NULL
RETURNING id, org_id, management_kind, owner_kind, owner_project_id, owner_user_id, name, kind, metadata, current_version_id, created_at, updated_at;

-- name: DeleteSecretVersions :exec
DELETE FROM secret_versions
WHERE org_id = sqlc.arg(org_id) AND secret_id = sqlc.arg(secret_id);

-- name: DeleteSecretGrantsForSecret :exec
DELETE FROM secret_grants
WHERE org_id = sqlc.arg(org_id) AND secret_id = sqlc.arg(secret_id);

-- name: DeleteSecretOAuthLeases :exec
DELETE FROM secret_oauth_refresh_leases
WHERE org_id = sqlc.arg(org_id) AND secret_id = sqlc.arg(secret_id);

-- name: SecretIsReferenced :one
-- @sqlc-vet-disable model-provider-configs-deleted-at integration-installs-deleted-at machine-pools-deleted-at
-- Soft-deleted secrets no longer trip foreign keys, so referencing rows are
-- checked explicitly. Deleted configs, pools, and installs clear their
-- credential references; any row still holding one blocks deletion.
SELECT EXISTS (
  SELECT 1 FROM model_provider_configs config
  WHERE config.org_id = sqlc.arg(org_id) AND config.credential_secret_id = sqlc.arg(secret_id)::uuid
  UNION ALL
  SELECT 1 FROM machine_pools pool
  WHERE pool.org_id = sqlc.arg(org_id) AND pool.provider_auth_secret_id = sqlc.arg(secret_id)::uuid
  UNION ALL
  SELECT 1 FROM integration_installs install
  WHERE install.org_id = sqlc.arg(org_id) AND install.credential_secret_id = sqlc.arg(secret_id)::uuid
) AS is_referenced;

-- name: InsertSecretGrant :one
INSERT INTO secret_grants(id, org_id, secret_id, target_project_id, created_at)
SELECT sqlc.arg(id), secret.org_id, secret.id, project.id, transaction_timestamp()
FROM secrets secret
JOIN projects project
  ON project.org_id = secret.org_id
 AND project.id = sqlc.arg(target_project_id)
WHERE secret.org_id = sqlc.arg(org_id)
  AND secret.id = sqlc.arg(secret_id)
  AND secret.management_kind = 'tenant'
  AND secret.deleted_at IS NULL
RETURNING id, org_id, secret_id, target_project_id, created_at;

-- name: GetSecretGrant :one
SELECT id, org_id, secret_id, target_project_id, created_at
FROM secret_grants
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: GetSecretGrantForSourceSecret :one
SELECT id, org_id, secret_id, target_project_id, created_at
FROM secret_grants
WHERE org_id = sqlc.arg(org_id) AND secret_id = sqlc.arg(secret_id) AND id = sqlc.arg(id);

-- name: GetSecretGrantForTargetProject :one
SELECT id, org_id, secret_id, target_project_id, created_at
FROM secret_grants
WHERE org_id = sqlc.arg(org_id) AND target_project_id = sqlc.arg(target_project_id) AND id = sqlc.arg(id);

-- name: ListSecretGrantsBySecret :many
WITH listed AS (
 SELECT g.id, g.org_id, g.secret_id, g.target_project_id, g.created_at,
 p.name AS target_project_name, p.created_at AS target_project_created_at, p.updated_at AS target_project_updated_at,
 CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(p.name) WHEN 'created_at' THEN to_char(g.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') END::text AS sort_key, false AS sort_is_null
 FROM secret_grants g JOIN projects p ON p.org_id = g.org_id AND p.id = g.target_project_id AND p.deleted_at IS NULL
 WHERE g.org_id = sqlc.arg(org_id) AND g.secret_id = sqlc.arg(secret_id)
  AND (sqlc.arg(name_pattern)::text = '' OR p.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.narg(target_project_id)::uuid IS NULL OR p.id = sqlc.narg(target_project_id)::uuid)
)
SELECT id, org_id, secret_id, target_project_id, created_at,
 target_project_name, target_project_created_at, target_project_updated_at, sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
 OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: DeleteSecretGrant :one
DELETE FROM secret_grants
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING id, org_id, secret_id, target_project_id, created_at;

-- name: SecretAvailableToProject :one
SELECT EXISTS (
  SELECT 1
  FROM secrets s
  WHERE s.org_id = sqlc.arg(org_id)
    AND s.id = sqlc.arg(secret_id)
    AND s.deleted_at IS NULL
    AND s.management_kind = 'tenant'
    AND (
      (s.owner_kind = 'project' AND s.owner_project_id = sqlc.arg(project_id))
      OR EXISTS (
        SELECT 1
        FROM secret_grants sg
        WHERE sg.org_id = s.org_id
          AND sg.secret_id = s.id
          AND sg.target_project_id = sqlc.arg(project_id)
      )
    )
)::boolean AS available;

-- The caller holds its receipt, then the conversation gate. Locking another
-- receipt here would invert that order and deadlock concurrent launch claims.
-- name: FindInboxLaunchClaims :many
WITH matches AS MATERIALIZED (
  SELECT id, state
  FROM integration_inbox
  WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
    AND id <> sqlc.arg(receipt_id) AND plan IS NOT NULL
    AND state IN ('queued', 'processing')
    AND jsonb_path_query_array(plan, '$.recipients.*.launch_claim') @> jsonb_build_array(sqlc.arg(launch_claim)::jsonb)
)
SELECT id, state FROM matches
ORDER BY id
LIMIT 2;

-- name: FindUnplannedIntegrationLaunchClaim :one
SELECT id, state FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND reserved_scope_kind = sqlc.arg(kind)::text AND reserved_scope_ref = sqlc.arg(ref)::text
  AND id <> sqlc.arg(receipt_id) AND plan IS NULL AND state IN ('queued', 'processing')
  AND (source = 'state' OR (sqlc.arg(include_provider)::boolean AND source = 'provider'
    AND id < sqlc.arg(receipt_id)))
ORDER BY id LIMIT 1;

-- Provider markers protect follow-ups, but do not arbitrate competing launches.
-- name: MarkIntegrationInboxPendingLaunch :execrows
UPDATE integration_inbox
SET reserved_scope_kind = sqlc.arg(kind)::text, reserved_scope_ref = sqlc.arg(ref)::text,
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND source = 'provider' AND plan IS NULL
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp()
  AND (reserved_scope_kind IS NULL
    OR (reserved_scope_kind = sqlc.arg(kind)::text AND reserved_scope_ref = sqlc.arg(ref)::text));

-- name: InsertIntegrationControlReceipt :exec
INSERT INTO integration_control_receipts (
  org_id, integration_app_id, connector_key, provider, provider_tenant_id, event_id, payload, end_install_id
) VALUES (
  sqlc.arg(org_id), sqlc.arg(integration_app_id), sqlc.arg(connector_key), sqlc.arg(provider),
  sqlc.arg(provider_tenant_id), sqlc.arg(event_id), sqlc.arg(payload), sqlc.arg(end_install_id)
)
ON CONFLICT (integration_app_id, event_id) DO NOTHING;

-- Read after INSERT so a concurrently committed winner is visible.
-- name: GetIntegrationControlReceiptByIdentity :one
SELECT id, org_id, integration_app_id, connector_key, provider, provider_tenant_id, event_id, payload,
  last_install_id, end_install_id, state, attempts_since_progress, available_at,
  lease_token, lease_generation, lease_expires_at, last_error, completed_at, created_at, updated_at
FROM integration_control_receipts
WHERE integration_app_id = sqlc.arg(integration_app_id) AND event_id = sqlc.arg(event_id);

-- Claims reserve work, not child mutation authority. Parent checks take no
-- locks after receipt locks; actual mutation must recheck its own live scope.
-- The saturated backoff counter NEVER limits the number of claims or pages.
-- name: ClaimNextIntegrationControlReceipt :one
WITH candidate AS MATERIALIZED (
  SELECT receipt.id
  FROM integration_control_receipts receipt
  JOIN integration_apps app ON app.org_id = receipt.org_id AND app.id = receipt.integration_app_id
    AND app.connector_key = receipt.connector_key AND app.provider = receipt.provider
    AND app.state = 'active' AND app.deleted_at IS NULL
  JOIN orgs organization ON organization.id = receipt.org_id AND organization.deleted_at IS NULL
  LEFT JOIN projects owner ON owner.id = app.owner_project_id AND owner.org_id = app.org_id
  WHERE receipt.connector_key = sqlc.arg(connector_key) AND receipt.provider = sqlc.arg(provider)
    AND (app.owner_project_id IS NULL OR (owner.id IS NOT NULL AND owner.deleted_at IS NULL))
    AND receipt.state IN ('pending', 'processing')
    AND receipt.available_at <= statement_timestamp()
    AND (receipt.lease_expires_at IS NULL OR receipt.lease_expires_at <= statement_timestamp())
  ORDER BY receipt.available_at, receipt.id
  FOR UPDATE OF receipt SKIP LOCKED LIMIT 1
)
UPDATE integration_control_receipts receipt
SET state = 'processing', attempts_since_progress = least(receipt.attempts_since_progress, 29) + 1,
    lease_token = uuidv7(), lease_generation = receipt.lease_generation + 1,
    lease_expires_at = statement_timestamp() + sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond',
    available_at = statement_timestamp() + sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond',
    updated_at = statement_timestamp()
FROM candidate WHERE receipt.id = candidate.id
RETURNING receipt.id, receipt.org_id, receipt.integration_app_id, receipt.connector_key,
  receipt.provider, receipt.provider_tenant_id, receipt.event_id, receipt.payload,
  receipt.last_install_id, receipt.end_install_id, receipt.state, receipt.attempts_since_progress, receipt.available_at,
  receipt.lease_token, receipt.lease_generation, receipt.lease_expires_at,
  receipt.last_error, receipt.completed_at, receipt.created_at, receipt.updated_at;

-- Only acknowledged forward progress resets the backoff counter. An unchanged
-- yield cannot manufacture progress. The fixed bound is not caller-mutable.
-- name: FinishIntegrationControlReceipt :one
UPDATE integration_control_receipts receipt
SET state = CASE WHEN sqlc.arg(outcome)::text IN ('yield', 'retry') THEN 'pending' ELSE sqlc.arg(outcome)::text END,
    last_install_id = coalesce(sqlc.narg(last_install_id)::uuid, receipt.last_install_id),
    attempts_since_progress = CASE WHEN sqlc.narg(last_install_id)::uuid > receipt.last_install_id
      THEN 0 ELSE receipt.attempts_since_progress END,
    available_at = CASE WHEN sqlc.arg(outcome)::text = 'retry'
      THEN statement_timestamp() + make_interval(secs => greatest(
        sqlc.arg(retry_after_microseconds)::bigint::double precision / 1000000.0,
        least(300.0,
          power(2.0, least(CASE WHEN sqlc.narg(last_install_id)::uuid > receipt.last_install_id
            THEN 0 ELSE receipt.attempts_since_progress END, 9)) *
          (0.8 + 0.4 * ((hashtextextended(receipt.id::text, receipt.lease_generation) & 2147483647)::double precision /
            2147483647.0)))))
      ELSE statement_timestamp() END,
    lease_token = NULL, lease_expires_at = NULL, last_error = sqlc.arg(last_error),
    completed_at = CASE WHEN sqlc.arg(outcome)::text IN ('completed', 'failed') THEN statement_timestamp() ELSE NULL END,
    updated_at = statement_timestamp()
WHERE receipt.integration_app_id = sqlc.arg(integration_app_id) AND receipt.id = sqlc.arg(id)
  AND receipt.state = 'processing' AND receipt.lease_token = sqlc.arg(lease_token)::uuid
  AND receipt.lease_generation = sqlc.arg(lease_generation) AND receipt.lease_expires_at > statement_timestamp()
  AND (sqlc.narg(last_install_id)::uuid IS NULL OR
    (sqlc.narg(last_install_id)::uuid >= receipt.last_install_id AND sqlc.narg(last_install_id)::uuid <= receipt.end_install_id))
  AND (sqlc.arg(outcome)::text <> 'yield' OR sqlc.narg(last_install_id)::uuid > receipt.last_install_id)
RETURNING receipt.id, receipt.org_id, receipt.integration_app_id, receipt.connector_key,
  receipt.provider, receipt.provider_tenant_id, receipt.event_id, receipt.payload,
  receipt.last_install_id, receipt.end_install_id, receipt.state, receipt.attempts_since_progress, receipt.available_at,
  receipt.lease_token, receipt.lease_generation, receipt.lease_expires_at,
  receipt.last_error, receipt.completed_at, receipt.created_at, receipt.updated_at;

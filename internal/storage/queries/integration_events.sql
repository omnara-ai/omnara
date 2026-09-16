-- name: InsertIntegrationEventReceipt :exec
INSERT INTO integration_event_receipts (
  project_id, integration_install_id, integration_app_id, connector_key, provider,
  event_id, payload, state, available_at, created_at, updated_at
) VALUES (
  sqlc.arg(project_id), sqlc.arg(integration_install_id), sqlc.arg(integration_app_id),
  sqlc.arg(connector_key), sqlc.arg(provider), sqlc.arg(event_id), sqlc.arg(payload),
  'pending', statement_timestamp(), statement_timestamp(), statement_timestamp()
)
ON CONFLICT (project_id, integration_install_id, event_id) DO NOTHING;

-- A separate statement after INSERT also sees a concurrently committed winner.
-- name: GetIntegrationEventReceiptByIdentity :one
SELECT id, project_id, integration_install_id, integration_app_id, connector_key, provider,
  event_id, payload, state, attempt_count, available_at, lease_token, lease_generation,
  lease_expires_at, last_error, completed_at, created_at, updated_at
FROM integration_event_receipts
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND event_id = sqlc.arg(event_id);

-- Claims need no parent locks: they reserve replay work, not permission to
-- mutate an agent. Each recipient mutation must recheck current authority.
-- name: ClaimNextIntegrationEventReceipt :one
WITH candidates AS MATERIALIZED (
  SELECT receipt.id
  FROM integration_event_receipts receipt
  JOIN integration_installs install
    ON install.project_id = receipt.project_id AND install.id = receipt.integration_install_id
   AND install.integration_app_id = receipt.integration_app_id
   AND install.integration_kind = 'managed'
   AND install.state = 'active' AND install.deleted_at IS NULL
  JOIN integration_apps app
    ON app.org_id = install.org_id AND app.id = receipt.integration_app_id
   AND app.state = 'active' AND app.deleted_at IS NULL
  JOIN projects project
    ON project.id = receipt.project_id AND project.org_id = install.org_id
   AND project.deleted_at IS NULL
  JOIN orgs organization
    ON organization.id = project.org_id AND organization.deleted_at IS NULL
  WHERE receipt.connector_key = sqlc.arg(connector_key)
    AND receipt.provider = sqlc.arg(provider)
    AND receipt.state IN ('pending', 'processing')
    AND receipt.attempt_count < sqlc.arg(max_attempts)::integer
    AND receipt.available_at <= statement_timestamp()
    AND (receipt.lease_expires_at IS NULL OR receipt.lease_expires_at <= statement_timestamp())
  ORDER BY receipt.available_at, receipt.id
  FOR UPDATE OF receipt SKIP LOCKED
  LIMIT 1
)
UPDATE integration_event_receipts receipt
SET state = 'processing', attempt_count = receipt.attempt_count + 1,
    lease_token = uuidv7(), lease_generation = receipt.lease_generation + 1,
    lease_expires_at = statement_timestamp() + sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond',
    available_at = statement_timestamp() + sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond',
    updated_at = statement_timestamp()
FROM candidates
WHERE receipt.id = candidates.id
RETURNING receipt.id, receipt.project_id, receipt.integration_install_id,
  receipt.integration_app_id, receipt.connector_key, receipt.provider, receipt.event_id,
  receipt.payload, receipt.state, receipt.attempt_count, receipt.available_at,
  receipt.lease_token, receipt.lease_generation, receipt.lease_expires_at,
  receipt.last_error, receipt.completed_at, receipt.created_at, receipt.updated_at;

-- Bound the indexed candidate page BEFORE checking ownership or taking receipt
-- locks. Healthy and locked rows consume the same finite page budget. A durable
-- cursor and fixed cycle end revisit skipped rows despite sustained new ingress.
-- Maintenance never steals an unexpired claim, including the final attempt, or
-- acquires parent locks in the opposite order to admission and owner teardown.
-- name: FailUnprocessableIntegrationEvents :execrows
WITH sweep_cursor AS MATERIALIZED (
  SELECT last_item_id, cycle_end_id
  FROM integration_sweep_cursors
  WHERE sweep_kind = 'event_unprocessable'
  FOR UPDATE SKIP LOCKED
), cycle AS MATERIALIZED (
  SELECT cursor.last_item_id,
         coalesce(cursor.cycle_end_id, upper_bound.id) AS cycle_end_id
  FROM sweep_cursor cursor
  LEFT JOIN LATERAL (
    SELECT receipt.id
    FROM integration_event_receipts receipt
    WHERE receipt.state IN ('pending', 'processing')
    ORDER BY receipt.id DESC
    LIMIT 1
  ) upper_bound ON cursor.cycle_end_id IS NULL
), candidates AS MATERIALIZED (
  SELECT candidate.id
  FROM cycle
  CROSS JOIN LATERAL (
    SELECT receipt.id
    FROM integration_event_receipts receipt
    WHERE receipt.state IN ('pending', 'processing')
      AND receipt.id > cycle.last_item_id
      AND receipt.id <= cycle.cycle_end_id
    ORDER BY receipt.id
    LIMIT sqlc.arg(row_limit)
  ) candidate
), progress AS MATERIALIZED (
  SELECT count(*) AS candidate_count,
         (SELECT id FROM candidates ORDER BY id DESC LIMIT 1) AS last_candidate_id
  FROM candidates
), advance_cursor AS (
  UPDATE integration_sweep_cursors cursor
  SET last_item_id = CASE
        WHEN progress.candidate_count < sqlc.arg(row_limit)::integer
          OR progress.last_candidate_id = cycle.cycle_end_id
          THEN '00000000-0000-0000-0000-000000000000'::uuid
        ELSE progress.last_candidate_id
      END,
      cycle_end_id = CASE
        WHEN progress.candidate_count < sqlc.arg(row_limit)::integer
          OR progress.last_candidate_id = cycle.cycle_end_id
          THEN NULL
        ELSE cycle.cycle_end_id
      END,
      updated_at = statement_timestamp()
  FROM cycle CROSS JOIN progress
  WHERE cursor.sweep_kind = 'event_unprocessable'
    AND cycle.cycle_end_id IS NOT NULL
  RETURNING cursor.sweep_kind
), unprocessable AS MATERIALIZED (
  SELECT receipt.id
  FROM candidates candidate
  JOIN integration_event_receipts receipt ON receipt.id = candidate.id
  WHERE receipt.state IN ('pending', 'processing')
    AND (receipt.lease_expires_at IS NULL OR receipt.lease_expires_at <= statement_timestamp())
    AND (
      receipt.attempt_count >= sqlc.arg(max_attempts)::integer
      OR NOT EXISTS (
        SELECT 1
        FROM integration_installs install
        JOIN integration_apps app
          ON app.org_id = install.org_id AND app.id = install.integration_app_id
         AND app.state = 'active' AND app.deleted_at IS NULL
        JOIN projects project
          ON project.id = install.project_id AND project.org_id = install.org_id
         AND project.deleted_at IS NULL
        JOIN orgs organization
          ON organization.id = project.org_id AND organization.deleted_at IS NULL
        WHERE install.project_id = receipt.project_id
          AND install.id = receipt.integration_install_id
          AND install.integration_app_id = receipt.integration_app_id
          AND install.integration_kind = 'managed'
   AND install.state = 'active' AND install.deleted_at IS NULL
      )
    )
  FOR UPDATE OF receipt SKIP LOCKED
)
UPDATE integration_event_receipts receipt
SET state = 'failed', lease_token = NULL, lease_expires_at = NULL,
    last_error = CASE WHEN receipt.attempt_count >= sqlc.arg(max_attempts)::integer
      THEN jsonb_build_object('code', 'retry_budget_exhausted', 'message', 'incoming event exhausted its retry budget')
      ELSE jsonb_build_object('code', 'owner_unavailable', 'message', 'incoming event owner is disabled or deleted') END,
    completed_at = statement_timestamp(), updated_at = statement_timestamp()
FROM unprocessable
WHERE receipt.id = unprocessable.id
  AND EXISTS (SELECT 1 FROM advance_cursor);

-- Completion time starts retention, not receipt creation or its latest retry.
-- Pending/processing rows are never purged, and parallel sweepers skip locks.
-- name: DeleteRetainedIntegrationEvents :execrows
WITH expired AS MATERIALIZED (
  SELECT receipt.id
  FROM integration_event_receipts receipt
  WHERE receipt.state IN ('completed', 'failed')
    AND receipt.completed_at < statement_timestamp() -
      (sqlc.arg(retention_microseconds)::bigint * interval '1 microsecond')
  ORDER BY receipt.completed_at, receipt.id
  FOR UPDATE OF receipt SKIP LOCKED
  LIMIT sqlc.arg(row_limit)
)
DELETE FROM integration_event_receipts receipt
USING expired
WHERE receipt.id = expired.id;

-- name: FinishIntegrationEventReceipt :one
UPDATE integration_event_receipts receipt
SET state = sqlc.arg(next_state)::text,
    available_at = CASE WHEN sqlc.arg(next_state)::text = 'pending'
      THEN statement_timestamp() + greatest(make_interval(secs =>
        least(300.0, power(2.0, least(receipt.attempt_count, 9))) *
        (0.8 + 0.4 * ((hashtextextended(receipt.id::text, receipt.attempt_count) & 2147483647)::double precision / 2147483647.0))),
        sqlc.arg(retry_after_microseconds)::bigint * interval '1 microsecond')
      ELSE receipt.available_at END,
    lease_token = NULL, lease_expires_at = NULL,
    last_error = sqlc.arg(last_error),
    completed_at = CASE WHEN sqlc.arg(next_state)::text IN ('completed', 'failed') THEN statement_timestamp() ELSE NULL END,
    updated_at = statement_timestamp()
WHERE receipt.project_id = sqlc.arg(project_id)
  AND receipt.integration_install_id = sqlc.arg(integration_install_id)
  AND receipt.id = sqlc.arg(id)
  AND receipt.state = 'processing'
  AND receipt.lease_token = sqlc.arg(lease_token)::uuid
  AND receipt.lease_generation = sqlc.arg(lease_generation)
  AND receipt.lease_expires_at > statement_timestamp()
RETURNING receipt.id, receipt.project_id, receipt.integration_install_id,
  receipt.integration_app_id, receipt.connector_key, receipt.provider, receipt.event_id,
  receipt.payload, receipt.state, receipt.attempt_count, receipt.available_at,
  receipt.lease_token, receipt.lease_generation, receipt.lease_expires_at,
  receipt.last_error, receipt.completed_at, receipt.created_at, receipt.updated_at;

-- Stable receipt replay is independent of mutable channel/binding authority.
-- Callers authorize the receipt scope and hold its existing execution fence.
-- name: GetIntegrationEventOutcome :one
SELECT outcome.agent_id, outcome.agent_input_id
FROM integration_event_outcomes outcome
JOIN integration_event_receipts receipt ON receipt.project_id = outcome.project_id AND receipt.id = outcome.receipt_id
WHERE outcome.project_id = sqlc.arg(project_id) AND outcome.receipt_id = sqlc.arg(receipt_id)
  AND receipt.integration_install_id = sqlc.arg(integration_install_id)
  AND outcome.delivery_key = sqlc.arg(delivery_key);

-- name: InsertIntegrationEventOutcome :exec
INSERT INTO integration_event_outcomes (project_id, receipt_id, delivery_key, agent_id, agent_input_id)
SELECT receipt.project_id, receipt.id, sqlc.arg(delivery_key), sqlc.arg(agent_id), sqlc.arg(agent_input_id)
FROM integration_event_receipts receipt
WHERE receipt.project_id = sqlc.arg(project_id) AND receipt.id = sqlc.arg(receipt_id)
  AND receipt.integration_install_id = sqlc.arg(integration_install_id)
ON CONFLICT (project_id, receipt_id, delivery_key) DO NOTHING;

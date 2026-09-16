-- Bound candidates BEFORE parent eligibility checks and receipt locks. Healthy
-- or locked rows still advance the durable sweep; later cycles revisit them.
-- No transient retry count terminalizes control work. Unexpired leases survive.
-- name: FailUnprocessableIntegrationControls :execrows
WITH sweep_cursor AS MATERIALIZED (
  SELECT last_item_id, cycle_end_id FROM integration_sweep_cursors
  WHERE sweep_kind = 'control_unprocessable' FOR UPDATE SKIP LOCKED
), cycle AS MATERIALIZED (
  SELECT cursor.last_item_id, coalesce(cursor.cycle_end_id, upper_bound.id) AS cycle_end_id
  FROM sweep_cursor cursor
  LEFT JOIN LATERAL (
    SELECT receipt.id FROM integration_control_receipts receipt
    WHERE receipt.state IN ('pending', 'processing') ORDER BY receipt.id DESC LIMIT 1
  ) upper_bound ON cursor.cycle_end_id IS NULL
), candidates AS MATERIALIZED (
  SELECT candidate.id FROM cycle
  CROSS JOIN LATERAL (
    SELECT receipt.id FROM integration_control_receipts receipt
    WHERE receipt.state IN ('pending', 'processing')
      AND receipt.id > cycle.last_item_id AND receipt.id <= cycle.cycle_end_id
    ORDER BY receipt.id LIMIT sqlc.arg(row_limit)
  ) candidate
), progress AS MATERIALIZED (
  SELECT count(*) AS candidate_count,
    (SELECT id FROM candidates ORDER BY id DESC LIMIT 1) AS last_candidate_id FROM candidates
), advance_cursor AS (
  UPDATE integration_sweep_cursors cursor
  SET last_item_id = CASE
        WHEN progress.candidate_count < sqlc.arg(row_limit)::integer OR progress.last_candidate_id = cycle.cycle_end_id
        THEN '00000000-0000-0000-0000-000000000000'::uuid ELSE progress.last_candidate_id END,
      cycle_end_id = CASE
        WHEN progress.candidate_count < sqlc.arg(row_limit)::integer OR progress.last_candidate_id = cycle.cycle_end_id
        THEN NULL ELSE cycle.cycle_end_id END,
      updated_at = statement_timestamp()
  FROM cycle CROSS JOIN progress
  WHERE cursor.sweep_kind = 'control_unprocessable' AND cycle.cycle_end_id IS NOT NULL
  RETURNING cursor.sweep_kind
), unprocessable AS MATERIALIZED (
  SELECT receipt.id FROM candidates candidate
  JOIN integration_control_receipts receipt ON receipt.id = candidate.id
  WHERE receipt.state IN ('pending', 'processing')
    AND (receipt.lease_expires_at IS NULL OR receipt.lease_expires_at <= statement_timestamp())
    AND NOT EXISTS (
      SELECT 1 FROM integration_apps app
      JOIN orgs organization ON organization.id = app.org_id AND organization.deleted_at IS NULL
      LEFT JOIN projects owner ON owner.id = app.owner_project_id AND owner.org_id = app.org_id
      WHERE app.org_id = receipt.org_id AND app.id = receipt.integration_app_id
        AND app.connector_key = receipt.connector_key AND app.provider = receipt.provider
        AND app.state = 'active' AND app.deleted_at IS NULL
        AND (app.owner_project_id IS NULL OR (owner.id IS NOT NULL AND owner.deleted_at IS NULL))
    )
  FOR UPDATE OF receipt SKIP LOCKED
)
UPDATE integration_control_receipts receipt
SET state = 'failed', lease_token = NULL, lease_expires_at = NULL,
    last_error = jsonb_build_object('code', 'owner_unavailable', 'message', 'control receipt owner is disabled or deleted'),
    completed_at = statement_timestamp(), updated_at = statement_timestamp()
FROM unprocessable WHERE receipt.id = unprocessable.id AND EXISTS (SELECT 1 FROM advance_cursor);

-- Only terminal completion starts retention. Pending/processing work is not
-- expired by age; its live-owner reconciliation may recover after an outage.
-- name: DeleteRetainedIntegrationControls :execrows
WITH expired AS MATERIALIZED (
  SELECT receipt.id FROM integration_control_receipts receipt
  WHERE receipt.state IN ('completed', 'failed')
    AND receipt.completed_at < statement_timestamp() -
      sqlc.arg(retention_microseconds)::bigint * interval '1 microsecond'
  ORDER BY receipt.completed_at, receipt.id
  FOR UPDATE OF receipt SKIP LOCKED LIMIT sqlc.arg(row_limit)
)
DELETE FROM integration_control_receipts receipt USING expired WHERE receipt.id = expired.id;

-- Unscoped internal observability only; do not fetch provider payloads or join
-- child owners. Pending age includes retry delays and currently leased work.
-- name: OldestPendingIntegrationControl :one
SELECT created_at FROM integration_control_receipts
WHERE state IN ('pending', 'processing') ORDER BY created_at LIMIT 1;

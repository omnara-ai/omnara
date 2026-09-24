-- name: InsertIntegrationInboxReceipt :one
INSERT INTO integration_inbox (project_id, integration_id, receipt_key, payload)
VALUES (sqlc.arg(project_id), sqlc.arg(integration_id), sqlc.arg(receipt_key), sqlc.arg(payload))
ON CONFLICT (project_id, integration_id, receipt_key) DO NOTHING
RETURNING id, project_id, integration_id, receipt_key, payload, source, events, plan, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at;

-- name: GetIntegrationInboxReceiptByKey :one
SELECT id, project_id, integration_id, receipt_key, payload, source, events, plan, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at
FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND receipt_key = sqlc.arg(receipt_key);

-- name: GetIntegrationInboxReceipt :one
SELECT id, project_id, integration_id, receipt_key, payload, source, events, plan, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at
FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id);

-- name: OldestReadyIntegrationInboxLag :one
SELECT EXTRACT(EPOCH FROM statement_timestamp() - available_at)::double precision AS lag_seconds
FROM integration_inbox
WHERE state = 'pending' AND available_at <= statement_timestamp()
ORDER BY available_at, id
LIMIT 1;

-- name: ListReadyIntegrationInboxIntegrations :many
-- Skip each integration's backlog using the pending index prefix: https://wiki.postgresql.org/wiki/Loose_indexscan
WITH RECURSIVE frontier AS (
  (SELECT inbox.project_id, inbox.integration_id, 1 AS ordinal
   FROM integration_inbox inbox
   WHERE inbox.state = 'pending'
     AND (inbox.project_id, inbox.integration_id) > (sqlc.arg(after_project_id)::uuid, sqlc.arg(after_integration_id)::uuid)
   ORDER BY inbox.project_id, inbox.integration_id
   LIMIT 1)
  UNION ALL
  SELECT next_integration.project_id, next_integration.integration_id, previous.ordinal + 1
  FROM frontier previous
  CROSS JOIN LATERAL (
    SELECT inbox.project_id, inbox.integration_id
    FROM integration_inbox inbox
    WHERE inbox.state = 'pending'
      AND (inbox.project_id, inbox.integration_id) > (previous.project_id, previous.integration_id)
    ORDER BY inbox.project_id, inbox.integration_id
    LIMIT 1
  ) next_integration
  WHERE previous.ordinal < sqlc.arg(row_limit)::integer
)
SELECT frontier.project_id, frontier.integration_id,
    CASE WHEN EXISTS (
        SELECT 1 FROM project_integrations integration
        JOIN projects project ON project.id = integration.project_id
        JOIN orgs org ON org.id = project.org_id
        WHERE integration.project_id = frontier.project_id AND integration.id = frontier.integration_id
          AND integration.state = 'active' AND integration.deleted_at IS NULL
          AND project.deleted_at IS NULL AND org.deleted_at IS NULL
    ) THEN COALESCE((
        SELECT true FROM integration_inbox inbox
        WHERE inbox.project_id = frontier.project_id AND inbox.integration_id = frontier.integration_id
          AND inbox.state = 'pending' AND inbox.available_at <= statement_timestamp() AND inbox.attempt_count < 8
        ORDER BY inbox.available_at, inbox.id
        LIMIT 1
    ), false) ELSE false END::boolean AS ready
FROM frontier
ORDER BY frontier.project_id, frontier.integration_id;

-- name: ClaimIntegrationInboxReceipt :one
WITH candidate AS (
  SELECT ready.id FROM integration_inbox ready
  WHERE ready.project_id = sqlc.arg(project_id) AND ready.integration_id = sqlc.arg(integration_id)
    AND ready.state = 'pending' AND ready.available_at <= statement_timestamp() AND ready.attempt_count < 8
  ORDER BY ready.available_at, ready.id
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
UPDATE integration_inbox inbox
SET state = 'processing', attempt_count = inbox.attempt_count + 1,
    claim_token = sqlc.arg(claim_token)::uuid,
    claim_expires_at = statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond',
    updated_at = statement_timestamp()
FROM candidate WHERE inbox.id = candidate.id
RETURNING inbox.id, inbox.project_id, inbox.integration_id, inbox.receipt_key, inbox.payload, inbox.source, inbox.events, inbox.plan, inbox.state, inbox.attempt_count, inbox.available_at, inbox.claim_token, inbox.claim_expires_at, inbox.last_error, inbox.created_at, inbox.updated_at, inbox.completed_at;

-- Use a fresh statement after locking: statement_timestamp() does not advance during lock waits.
-- name: LockIntegrationInboxReceipt :one
SELECT id FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ReadIntegrationInboxLease :one
SELECT id, project_id, integration_id, receipt_key, payload, source, events, plan, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- name: FreezeIntegrationInboxPlan :execrows
UPDATE integration_inbox
SET plan = sqlc.arg(plan)::jsonb, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp()
  AND plan IS NULL;

-- name: CompleteIntegrationInboxReceipt :execrows
UPDATE integration_inbox
SET state = 'completed', claim_token = NULL, claim_expires_at = NULL,
    completed_at = statement_timestamp(), last_error = NULL, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp()
  AND plan IS NOT NULL;

-- name: RetryIntegrationInboxReceipt :execrows
UPDATE integration_inbox
SET state = CASE WHEN attempt_count >= 8 THEN 'failed' ELSE 'pending' END,
    completed_at = CASE WHEN attempt_count >= 8 THEN statement_timestamp() ELSE NULL END,
    claim_token = NULL, claim_expires_at = NULL,
    available_at = statement_timestamp() + sqlc.arg(delay_milliseconds)::bigint * interval '1 millisecond',
    last_error = sqlc.arg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- name: FailIntegrationInboxReceipt :execrows
UPDATE integration_inbox
SET state = 'failed', claim_token = NULL, claim_expires_at = NULL,
    completed_at = statement_timestamp(),
    last_error = sqlc.arg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- name: RecoverExpiredIntegrationInboxReceipts :execrows
WITH candidates AS MATERIALIZED (
  SELECT expired.id, expired.project_id, expired.integration_id
  FROM integration_inbox expired
  WHERE expired.state = 'processing' AND expired.claim_expires_at <= statement_timestamp()
  ORDER BY expired.claim_expires_at, expired.id
  LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
), scoped AS (
  SELECT candidates.id,
    (integration.state = 'active' AND integration.deleted_at IS NULL
     AND project.deleted_at IS NULL AND org.deleted_at IS NULL) AS active
  FROM candidates
  JOIN project_integrations integration ON integration.project_id = candidates.project_id AND integration.id = candidates.integration_id
  JOIN projects project ON project.id = candidates.project_id
  JOIN orgs org ON org.id = project.org_id
)
UPDATE integration_inbox inbox
SET state = CASE WHEN NOT scoped.active OR inbox.attempt_count >= 8 THEN 'failed' ELSE 'pending' END,
    completed_at = CASE WHEN NOT scoped.active OR inbox.attempt_count >= 8 THEN statement_timestamp() ELSE NULL END,
    claim_token = NULL, claim_expires_at = NULL, available_at = statement_timestamp(),
    last_error = CASE WHEN NOT scoped.active THEN 'integration scope inactive'
                      WHEN inbox.attempt_count >= 8 THEN 'inbox attempt budget exhausted'
                      ELSE 'inbox lease expired' END,
    updated_at = statement_timestamp()
FROM scoped WHERE inbox.id = scoped.id;

-- Scan inactive integrations first so recovery does not sort or scan healthy inbox history.
-- name: FailInactiveIntegrationInboxReceipts :execrows
WITH inactive_integrations AS MATERIALIZED (
  SELECT integration.project_id, array_agg(integration.id) AS integration_ids
  FROM project_integrations integration
  JOIN projects project ON project.id = integration.project_id
  JOIN orgs org ON org.id = project.org_id
  WHERE integration.state <> 'active' OR integration.deleted_at IS NOT NULL
     OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
  GROUP BY integration.project_id
), candidates AS MATERIALIZED (
  SELECT unsettled.id
  FROM inactive_integrations integration
  CROSS JOIN LATERAL (
    SELECT inbox.id FROM integration_inbox inbox
    WHERE inbox.project_id = integration.project_id AND inbox.integration_id = ANY(integration.integration_ids)
      AND inbox.state = 'pending'
    ORDER BY inbox.integration_id, inbox.available_at, inbox.id
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
  ) unsettled
  LIMIT sqlc.arg(row_limit)
)
UPDATE integration_inbox inbox
SET state = 'failed', claim_token = NULL, claim_expires_at = NULL,
    completed_at = statement_timestamp(),
    last_error = 'integration scope inactive', updated_at = statement_timestamp()
FROM candidates WHERE inbox.id = candidates.id;

-- Deleting a receipt ends transport deduplication; later provider replays may be accepted.
-- name: CleanupTerminalIntegrationInboxReceipts :execrows
WITH candidates AS (
  SELECT finished.id FROM integration_inbox finished
  WHERE finished.state IN ('completed', 'failed')
    AND finished.completed_at < statement_timestamp() - sqlc.arg(retention_milliseconds)::bigint * interval '1 millisecond'
  ORDER BY finished.completed_at, finished.id LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
)
DELETE FROM integration_inbox inbox USING candidates WHERE inbox.id = candidates.id;

-- name: CleanupDeletedIntegrationInboxReceipts :execrows
WITH deleted_integrations AS MATERIALIZED (
  SELECT integration.project_id, integration.id
  FROM project_integrations integration
  JOIN projects project ON project.id = integration.project_id
  JOIN orgs org ON org.id = project.org_id
  WHERE integration.deleted_at IS NOT NULL OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
), candidates AS MATERIALIZED (
  SELECT obsolete.id
  FROM deleted_integrations integration
  CROSS JOIN LATERAL (
    SELECT inbox.id FROM integration_inbox inbox
    WHERE inbox.project_id = integration.project_id AND inbox.integration_id = integration.id
    ORDER BY inbox.receipt_key
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
  ) obsolete
  LIMIT sqlc.arg(row_limit)
)
DELETE FROM integration_inbox inbox USING candidates WHERE inbox.id = candidates.id;

-- name: InsertScheduledIntegrationEventReceipt :one
INSERT INTO integration_inbox (project_id, integration_id, receipt_key, payload, source)
VALUES (sqlc.arg(project_id), sqlc.arg(integration_id), sqlc.arg(receipt_key), sqlc.arg(payload), 'scheduled')
ON CONFLICT (project_id, integration_id, receipt_key) DO NOTHING
RETURNING id, project_id, integration_id, receipt_key, payload, source, events, plan, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at;

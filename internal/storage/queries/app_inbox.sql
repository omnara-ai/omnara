-- name: InsertAppInboxReceipt :one
INSERT INTO app_inbox (project_id, app_id, receipt_key, payload)
VALUES (sqlc.arg(project_id), sqlc.arg(app_id), sqlc.arg(receipt_key), sqlc.arg(payload))
ON CONFLICT (project_id, app_id, receipt_key) DO NOTHING
RETURNING id, project_id, app_id, receipt_key, payload, source, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at;

-- name: GetAppInboxReceiptByKey :one
SELECT id, project_id, app_id, receipt_key, payload, source, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at
FROM app_inbox
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND receipt_key = sqlc.arg(receipt_key);

-- name: GetAppInboxReceipt :one
SELECT id, project_id, app_id, receipt_key, payload, source, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at
FROM app_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id);

-- name: OldestReadyAppInboxLag :one
SELECT EXTRACT(EPOCH FROM statement_timestamp() - available_at)::double precision AS lag_seconds
FROM app_inbox
WHERE state = 'pending' AND available_at <= statement_timestamp()
ORDER BY available_at, id
LIMIT 1;

-- name: ListReadyAppInboxApps :many
-- Skip each app's backlog using the pending index prefix: https://wiki.postgresql.org/wiki/Loose_indexscan
WITH RECURSIVE frontier AS (
  (SELECT inbox.project_id, inbox.app_id, 1 AS ordinal
   FROM app_inbox inbox
   WHERE inbox.state = 'pending'
     AND (inbox.project_id, inbox.app_id) > (sqlc.arg(after_project_id)::uuid, sqlc.arg(after_app_id)::uuid)
   ORDER BY inbox.project_id, inbox.app_id
   LIMIT 1)
  UNION ALL
  SELECT next_app.project_id, next_app.app_id, previous.ordinal + 1
  FROM frontier previous
  CROSS JOIN LATERAL (
    SELECT inbox.project_id, inbox.app_id
    FROM app_inbox inbox
    WHERE inbox.state = 'pending'
      AND (inbox.project_id, inbox.app_id) > (previous.project_id, previous.app_id)
    ORDER BY inbox.project_id, inbox.app_id
    LIMIT 1
  ) next_app
  WHERE previous.ordinal < sqlc.arg(row_limit)::integer
)
SELECT frontier.project_id, frontier.app_id,
    CASE WHEN EXISTS (
        SELECT 1 FROM project_apps app
        JOIN projects project ON project.id = app.project_id
        JOIN orgs org ON org.id = project.org_id
        WHERE app.project_id = frontier.project_id AND app.id = frontier.app_id
          AND app.state = 'active' AND app.deleted_at IS NULL
          AND project.deleted_at IS NULL AND org.deleted_at IS NULL
    ) THEN COALESCE((
        SELECT true FROM app_inbox inbox
        WHERE inbox.project_id = frontier.project_id AND inbox.app_id = frontier.app_id
          AND inbox.state = 'pending' AND inbox.available_at <= statement_timestamp() AND inbox.attempt_count < 8
        ORDER BY inbox.available_at, inbox.id
        LIMIT 1
    ), false) ELSE false END::boolean AS ready
FROM frontier
ORDER BY frontier.project_id, frontier.app_id;

-- name: ClaimAppInboxReceipt :one
WITH candidate AS (
  SELECT ready.id FROM app_inbox ready
  WHERE ready.project_id = sqlc.arg(project_id) AND ready.app_id = sqlc.arg(app_id)
    AND ready.state = 'pending' AND ready.available_at <= statement_timestamp() AND ready.attempt_count < 8
  ORDER BY ready.available_at, ready.id
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
UPDATE app_inbox inbox
SET state = 'processing', attempt_count = inbox.attempt_count + 1,
    claim_token = sqlc.arg(claim_token)::uuid,
    claim_expires_at = statement_timestamp() + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond',
    updated_at = statement_timestamp()
FROM candidate WHERE inbox.id = candidate.id
RETURNING inbox.id, inbox.project_id, inbox.app_id, inbox.receipt_key, inbox.payload, inbox.source, inbox.events, inbox.plan, inbox.progress, inbox.state, inbox.attempt_count, inbox.available_at, inbox.claim_token, inbox.claim_expires_at, inbox.last_error, inbox.created_at, inbox.updated_at, inbox.completed_at;

-- Use a fresh statement after locking: statement_timestamp() does not advance during lock waits.
-- name: LockAppInboxReceipt :one
SELECT id FROM app_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ReadAppInboxLease :one
SELECT id, project_id, app_id, receipt_key, payload, source, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at FROM app_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- name: FreezeAppInboxPlan :execrows
UPDATE app_inbox
SET plan = sqlc.arg(plan)::jsonb, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp()
  AND plan IS NULL;

-- name: UpdateAppInboxProgress :execrows
UPDATE app_inbox
SET progress = sqlc.arg(progress)::jsonb, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp()
  AND plan IS NOT NULL;

-- name: CompleteAppInboxReceipt :execrows
UPDATE app_inbox
SET state = 'completed', claim_token = NULL, claim_expires_at = NULL,
    completed_at = statement_timestamp(), last_error = NULL, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp()
  AND plan IS NOT NULL;

-- name: RetryAppInboxReceipt :execrows
UPDATE app_inbox
SET state = CASE WHEN attempt_count >= 8 THEN 'failed' ELSE 'pending' END,
    completed_at = CASE WHEN attempt_count >= 8 THEN statement_timestamp() ELSE NULL END,
    claim_token = NULL, claim_expires_at = NULL,
    available_at = statement_timestamp() + sqlc.arg(delay_milliseconds)::bigint * interval '1 millisecond',
    last_error = sqlc.arg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- name: FailAppInboxReceipt :execrows
UPDATE app_inbox
SET state = 'failed', claim_token = NULL, claim_expires_at = NULL,
    completed_at = statement_timestamp(),
    last_error = sqlc.arg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- name: RecoverExpiredAppInboxReceipts :execrows
WITH candidates AS MATERIALIZED (
  SELECT expired.id, expired.project_id, expired.app_id
  FROM app_inbox expired
  WHERE expired.state = 'processing' AND expired.claim_expires_at <= statement_timestamp()
  ORDER BY expired.claim_expires_at, expired.id
  LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
), scoped AS (
  SELECT candidates.id,
    (app.state = 'active' AND app.deleted_at IS NULL
     AND project.deleted_at IS NULL AND org.deleted_at IS NULL) AS active
  FROM candidates
  JOIN project_apps app ON app.project_id = candidates.project_id AND app.id = candidates.app_id
  JOIN projects project ON project.id = candidates.project_id
  JOIN orgs org ON org.id = project.org_id
)
UPDATE app_inbox inbox
SET state = CASE WHEN NOT scoped.active OR inbox.attempt_count >= 8 THEN 'failed' ELSE 'pending' END,
    completed_at = CASE WHEN NOT scoped.active OR inbox.attempt_count >= 8 THEN statement_timestamp() ELSE NULL END,
    claim_token = NULL, claim_expires_at = NULL, available_at = statement_timestamp(),
    last_error = CASE WHEN NOT scoped.active THEN 'app scope inactive'
                      WHEN inbox.attempt_count >= 8 THEN 'inbox attempt budget exhausted'
                      ELSE 'inbox lease expired' END,
    updated_at = statement_timestamp()
FROM scoped WHERE inbox.id = scoped.id;

-- Scan inactive apps first so recovery does not sort or scan healthy inbox history.
-- name: FailInactiveAppInboxReceipts :execrows
WITH inactive_apps AS MATERIALIZED (
  SELECT app.project_id, array_agg(app.id) AS app_ids
  FROM project_apps app
  JOIN projects project ON project.id = app.project_id
  JOIN orgs org ON org.id = project.org_id
  WHERE app.state <> 'active' OR app.deleted_at IS NOT NULL
     OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
  GROUP BY app.project_id
), candidates AS MATERIALIZED (
  SELECT unsettled.id
  FROM inactive_apps app
  CROSS JOIN LATERAL (
    SELECT inbox.id FROM app_inbox inbox
    WHERE inbox.project_id = app.project_id AND inbox.app_id = ANY(app.app_ids)
      AND inbox.state = 'pending'
    ORDER BY inbox.app_id, inbox.available_at, inbox.id
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
  ) unsettled
  LIMIT sqlc.arg(row_limit)
)
UPDATE app_inbox inbox
SET state = 'failed', claim_token = NULL, claim_expires_at = NULL,
    completed_at = statement_timestamp(),
    last_error = 'app scope inactive', updated_at = statement_timestamp()
FROM candidates WHERE inbox.id = candidates.id;

-- Deleting a receipt ends transport deduplication; later provider replays may be accepted.
-- name: CleanupTerminalAppInboxReceipts :execrows
WITH candidates AS (
  SELECT finished.id FROM app_inbox finished
  WHERE finished.state IN ('completed', 'failed')
    AND finished.completed_at < statement_timestamp() - sqlc.arg(retention_milliseconds)::bigint * interval '1 millisecond'
  ORDER BY finished.completed_at, finished.id LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
)
DELETE FROM app_inbox inbox USING candidates WHERE inbox.id = candidates.id;

-- name: CleanupDeletedAppInboxReceipts :execrows
WITH deleted_apps AS MATERIALIZED (
  SELECT app.project_id, app.id
  FROM project_apps app
  JOIN projects project ON project.id = app.project_id
  JOIN orgs org ON org.id = project.org_id
  WHERE app.deleted_at IS NOT NULL OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
), candidates AS MATERIALIZED (
  SELECT obsolete.id
  FROM deleted_apps app
  CROSS JOIN LATERAL (
    SELECT inbox.id FROM app_inbox inbox
    WHERE inbox.project_id = app.project_id AND inbox.app_id = app.id
    ORDER BY inbox.receipt_key
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
  ) obsolete
  LIMIT sqlc.arg(row_limit)
)
DELETE FROM app_inbox inbox USING candidates WHERE inbox.id = candidates.id;

-- name: InsertScheduledAppEventReceipt :one
INSERT INTO app_inbox (project_id, app_id, receipt_key, payload, source)
VALUES (sqlc.arg(project_id), sqlc.arg(app_id), sqlc.arg(receipt_key), sqlc.arg(payload), 'scheduled')
ON CONFLICT (project_id, app_id, receipt_key) DO NOTHING
RETURNING id, project_id, app_id, receipt_key, payload, source, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at;

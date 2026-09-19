-- Verified bytes and receipt identity are immutable. Provider verification happens
-- before this query; the owner holds active project/connection lifecycle gates.
-- name: InsertIntegrationInboxReceipt :one
INSERT INTO integration_inbox (project_id, connection_id, receipt_key, payload)
VALUES (sqlc.arg(project_id), sqlc.arg(connection_id), sqlc.arg(receipt_key), sqlc.arg(payload))
ON CONFLICT (project_id, connection_id, receipt_key) DO NOTHING
RETURNING id, project_id, connection_id, receipt_key, payload, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at;

-- name: GetIntegrationInboxReceiptByKey :one
SELECT id, project_id, connection_id, receipt_key, payload, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at
FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id)
  AND receipt_key = sqlc.arg(receipt_key);

-- name: GetIntegrationInboxReceipt :one
SELECT id, project_id, connection_id, receipt_key, payload, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at
FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id);

-- name: ListIntegrationInboxReceipts :many
SELECT id, project_id, connection_id, receipt_key, state, attempt_count,
       available_at, claim_expires_at, last_error, created_at, updated_at, completed_at
FROM integration_inbox
WHERE project_id = sqlc.arg(project_id)
  AND (sqlc.narg(connection_id)::uuid IS NULL OR connection_id = sqlc.narg(connection_id)::uuid)
  AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state)::text)
  AND (NOT sqlc.arg(cursor_set)::boolean
       OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListReadyIntegrationInboxConnections :many
-- Bound pending receipt inspection before checking scope. Recovery drains an
-- inactive connection that occupies this frontier; history is never inspected.
WITH frontier AS MATERIALIZED (
  SELECT inbox.project_id, inbox.connection_id
  FROM integration_inbox inbox
  WHERE inbox.state = 'pending' AND inbox.available_at <= statement_timestamp()
    AND inbox.attempt_count < 8
  ORDER BY inbox.available_at, inbox.id
  LIMIT sqlc.arg(row_limit)
)
SELECT DISTINCT frontier.project_id, frontier.connection_id
FROM frontier
JOIN integration_connections connection ON connection.project_id = frontier.project_id AND connection.id = frontier.connection_id
JOIN projects project ON project.id = frontier.project_id
JOIN orgs org ON org.id = project.org_id
WHERE connection.state = 'active' AND connection.deleted_at IS NULL
  AND project.deleted_at IS NULL AND org.deleted_at IS NULL;

-- name: ClaimIntegrationInboxReceipt :one
WITH candidate AS (
  SELECT ready.id FROM integration_inbox ready
  WHERE ready.project_id = sqlc.arg(project_id) AND ready.connection_id = sqlc.arg(connection_id)
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
RETURNING inbox.id, inbox.project_id, inbox.connection_id, inbox.receipt_key, inbox.payload, inbox.events, inbox.plan, inbox.progress, inbox.state, inbox.attempt_count, inbox.available_at, inbox.claim_token, inbox.claim_expires_at, inbox.last_error, inbox.created_at, inbox.updated_at, inbox.completed_at;

-- Locking and checking are separate statements in Go: time advances while waiting.
-- name: LockIntegrationInboxReceipt :one
SELECT id FROM integration_inbox
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ReadIntegrationInboxLease :one
SELECT id, project_id, connection_id, receipt_key, payload, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at FROM integration_inbox
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

-- Progress cannot overwrite the plan or receipt. The semantic store only adds
-- prepared/committed stages of a frozen slot, preserving prior stage results.
-- name: UpdateIntegrationInboxProgress :execrows
UPDATE integration_inbox
SET progress = sqlc.arg(progress)::jsonb, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp()
  AND plan IS NOT NULL;

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
    claim_token = NULL, claim_expires_at = NULL,
    available_at = statement_timestamp() + sqlc.arg(delay_milliseconds)::bigint * interval '1 millisecond',
    last_error = sqlc.arg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- name: FailIntegrationInboxReceipt :execrows
UPDATE integration_inbox
SET state = 'failed', claim_token = NULL, claim_expires_at = NULL,
    last_error = sqlc.arg(last_error), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND state = 'processing' AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_expires_at > statement_timestamp();

-- Explicit operator recovery grants a fresh bounded attempt budget; it preserves
-- both frozen selections and completed slots, so it cannot relaunch recipients.
-- name: RetryFailedIntegrationInboxReceipt :execrows
UPDATE integration_inbox
SET state = 'pending', attempt_count = 0, available_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND state = 'failed';

-- Preserve deduplication and diagnosis while releasing an uncommitted selection.
-- The semantic store verifies retained targets and progress under lifecycle gates.
-- name: DiscardFailedIntegrationInboxReceipt :execrows
UPDATE integration_inbox
SET state = 'discarded', completed_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND state = 'failed';

-- The expired frontier uses the lease-expiry partial index. Scope checks occur
-- only after the bounded SKIP LOCKED selection; healthy pending work is untouched.
-- name: RecoverExpiredIntegrationInboxReceipts :execrows
WITH candidates AS MATERIALIZED (
  SELECT expired.id, expired.project_id, expired.connection_id
  FROM integration_inbox expired
  WHERE expired.state = 'processing' AND expired.claim_expires_at <= statement_timestamp()
  ORDER BY expired.claim_expires_at, expired.id
  LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
), scoped AS (
  SELECT candidates.id,
    (connection.state = 'active' AND connection.deleted_at IS NULL
     AND project.deleted_at IS NULL AND org.deleted_at IS NULL) AS active
  FROM candidates
  JOIN integration_connections connection ON connection.project_id = candidates.project_id AND connection.id = candidates.connection_id
  JOIN projects project ON project.id = candidates.project_id
  JOIN orgs org ON org.id = project.org_id
)
UPDATE integration_inbox inbox
SET state = CASE WHEN NOT scoped.active OR inbox.attempt_count >= 8 THEN 'failed' ELSE 'pending' END,
    claim_token = NULL, claim_expires_at = NULL, available_at = statement_timestamp(),
    last_error = CASE WHEN NOT scoped.active THEN 'integration scope inactive'
                      WHEN inbox.attempt_count >= 8 THEN 'inbox attempt budget exhausted'
                      ELSE 'inbox lease expired' END,
    updated_at = statement_timestamp()
FROM scoped WHERE inbox.id = scoped.id;

-- Start from inactive scopes, not the inbox. The materialized scope set and
-- per-project connection batches keep the planner off healthy pending/history rows.
-- There is deliberately no global inbox sort before LIMIT. Each matching scope
-- probes the pending-ready partial index; processing receipts are fenced from
-- use immediately and failed by expired-lease recovery after their lease ends.
-- name: FailInactiveIntegrationInboxReceipts :execrows
WITH inactive_connections AS MATERIALIZED (
  SELECT connection.project_id, array_agg(connection.id) AS connection_ids
  FROM integration_connections connection
  JOIN projects project ON project.id = connection.project_id
  JOIN orgs org ON org.id = project.org_id
  WHERE connection.state <> 'active' OR connection.deleted_at IS NOT NULL
     OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
  GROUP BY connection.project_id
), candidates AS MATERIALIZED (
  SELECT unsettled.id
  FROM inactive_connections connection
  CROSS JOIN LATERAL (
    SELECT inbox.id FROM integration_inbox inbox
    WHERE inbox.project_id = connection.project_id AND inbox.connection_id = ANY(connection.connection_ids)
      AND inbox.state = 'pending'
    ORDER BY inbox.connection_id, inbox.available_at, inbox.id
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
  ) unsettled
  LIMIT sqlc.arg(row_limit)
)
UPDATE integration_inbox inbox
SET state = 'failed', claim_token = NULL, claim_expires_at = NULL,
    last_error = 'integration scope inactive', updated_at = statement_timestamp()
FROM candidates WHERE inbox.id = candidates.id;

-- Completed or discarded receipt identity is retained only for the configured retention
-- window. After deletion a sufficiently late provider replay can be accepted.
-- name: CleanupTerminalIntegrationInboxReceipts :execrows
WITH candidates AS (
  SELECT finished.id FROM integration_inbox finished
  WHERE finished.state IN ('completed', 'discarded')
    AND finished.completed_at < statement_timestamp() - sqlc.arg(retention_milliseconds)::bigint * interval '1 millisecond'
  ORDER BY finished.completed_at, finished.id LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
)
DELETE FROM integration_inbox inbox USING candidates WHERE inbox.id = candidates.id;

-- Soft-deleted scopes no longer need raw payloads, including failed receipts.
-- Disabled live connections retain failed selection facts for operator recovery.
-- Resolve deleted scopes first, then use the unique receipt identity index.
-- An empty cleanup poll does not inspect retained history in live connections.
-- name: CleanupDeletedIntegrationInboxReceipts :execrows
WITH deleted_connections AS MATERIALIZED (
  SELECT connection.project_id, connection.id
  FROM integration_connections connection
  JOIN projects project ON project.id = connection.project_id
  JOIN orgs org ON org.id = project.org_id
  WHERE connection.deleted_at IS NOT NULL OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
), candidates AS MATERIALIZED (
  SELECT obsolete.id
  FROM deleted_connections connection
  CROSS JOIN LATERAL (
    SELECT inbox.id FROM integration_inbox inbox
    WHERE inbox.project_id = connection.project_id AND inbox.connection_id = connection.id
    ORDER BY inbox.receipt_key
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
  ) obsolete
  LIMIT sqlc.arg(row_limit)
)
DELETE FROM integration_inbox inbox USING candidates WHERE inbox.id = candidates.id;

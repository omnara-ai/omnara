-- name: InsertCronTrigger :one
INSERT INTO cron_triggers(
    id, project_id, name, agent_profile_id, agent_id,
    app_id, app_settings,
    cron_expression, timezone, message_template, delivery_mode, enabled,
    next_fire_after, idempotency_key, created_at, updated_at
)
VALUES (
    uuidv7(), sqlc.arg(project_id), sqlc.arg(name),
    sqlc.narg(agent_profile_id), sqlc.narg(agent_id),
    sqlc.narg(app_id), sqlc.narg(app_settings),
    sqlc.arg(cron_expression), sqlc.arg(timezone), NULLIF(sqlc.arg(message_template)::text, ''),
    sqlc.arg(delivery_mode), sqlc.arg(enabled), sqlc.narg(next_fire_after),
    sqlc.narg(idempotency_key), transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT (project_id, idempotency_key) DO NOTHING
RETURNING id, project_id, name, agent_profile_id, agent_id,
          app_id, app_settings,
          cron_expression, timezone, coalesce(message_template, '')::text AS message_template, delivery_mode, enabled,
          last_fired_at, next_fire_after, failure_report,
          coalesce(idempotency_key, '') AS idempotency_key,
          created_at, updated_at;

-- name: GetCronTrigger :one
SELECT trigger.id, project.org_id, trigger.project_id, trigger.name,
       trigger.agent_profile_id, trigger.agent_id,
       trigger.app_id, trigger.app_settings,
       trigger.cron_expression, trigger.timezone, coalesce(trigger.message_template, '')::text AS message_template,
       trigger.delivery_mode, trigger.enabled, trigger.last_fired_at, trigger.next_fire_after,
       trigger.failure_report,
       CASE WHEN receipt.id IS NOT NULL THEN jsonb_build_object(
           'state', CASE receipt.state WHEN 'pending' THEN 'queued' ELSE receipt.state END,
           'created_at', receipt.created_at, 'updated_at', receipt.updated_at,
           'failure_message', CASE WHEN receipt.state = 'failed' THEN 'Scheduled app action failed.' ELSE NULL END
       ) END::jsonb AS last_run,
       coalesce(trigger.idempotency_key, '') AS idempotency_key,
       trigger.created_at, trigger.updated_at
FROM cron_triggers trigger
JOIN projects project ON project.id = trigger.project_id
LEFT JOIN integration_inbox receipt ON receipt.id = trigger.last_app_receipt_id
    AND receipt.project_id = trigger.project_id AND receipt.app_id = trigger.app_id
    AND receipt.source = 'scheduled'
WHERE trigger.project_id = sqlc.arg(project_id)
  AND trigger.id = sqlc.arg(id)
  AND trigger.deleted_at IS NULL;

-- name: GetCronTriggerByIdempotencyKey :one
SELECT trigger.id, project.org_id, trigger.project_id, trigger.name,
       trigger.agent_profile_id, trigger.agent_id,
       trigger.app_id, trigger.app_settings,
       trigger.cron_expression, trigger.timezone, coalesce(trigger.message_template, '')::text AS message_template,
       trigger.delivery_mode, trigger.enabled, trigger.last_fired_at, trigger.next_fire_after,
       trigger.failure_report,
       CASE WHEN receipt.id IS NOT NULL THEN jsonb_build_object(
           'state', CASE receipt.state WHEN 'pending' THEN 'queued' ELSE receipt.state END,
           'created_at', receipt.created_at, 'updated_at', receipt.updated_at,
           'failure_message', CASE WHEN receipt.state = 'failed' THEN 'Scheduled app action failed.' ELSE NULL END
       ) END::jsonb AS last_run,
       coalesce(trigger.idempotency_key, '') AS idempotency_key,
       trigger.created_at, trigger.updated_at
FROM cron_triggers trigger
JOIN projects project ON project.id = trigger.project_id
LEFT JOIN integration_inbox receipt ON receipt.id = trigger.last_app_receipt_id
    AND receipt.project_id = trigger.project_id AND receipt.app_id = trigger.app_id
    AND receipt.source = 'scheduled'
WHERE trigger.project_id = sqlc.arg(project_id)
  AND trigger.idempotency_key = sqlc.arg(idempotency_key)::text
  AND trigger.deleted_at IS NULL;

-- name: ListCronTriggersForProject :many
WITH listed AS (
SELECT trigger.id, project.org_id, trigger.project_id, trigger.name,
       trigger.agent_profile_id, trigger.agent_id,
       trigger.app_id, trigger.app_settings,
       trigger.cron_expression, trigger.timezone, coalesce(trigger.message_template, '')::text AS message_template,
       trigger.delivery_mode, trigger.enabled, trigger.last_fired_at, trigger.next_fire_after,
       trigger.failure_report,
       CASE WHEN receipt.id IS NOT NULL THEN jsonb_build_object(
           'state', CASE receipt.state WHEN 'pending' THEN 'queued' ELSE receipt.state END,
           'created_at', receipt.created_at, 'updated_at', receipt.updated_at,
           'failure_message', CASE WHEN receipt.state = 'failed' THEN 'Scheduled app action failed.' ELSE NULL END
       ) END::jsonb AS last_run,
       coalesce(trigger.idempotency_key, '') AS idempotency_key,
       trigger.created_at, trigger.updated_at,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(trigger.name)
         WHEN 'created_at' THEN to_char(trigger.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(trigger.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
       END::text AS sort_key,
       false AS sort_is_null
FROM cron_triggers trigger
JOIN projects project ON project.id = trigger.project_id
LEFT JOIN integration_inbox receipt ON receipt.id = trigger.last_app_receipt_id
    AND receipt.project_id = trigger.project_id AND receipt.app_id = trigger.app_id
    AND receipt.source = 'scheduled'
WHERE trigger.project_id = sqlc.arg(project_id)
  AND trigger.deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR trigger.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.narg(agent_profile_id)::uuid IS NULL OR trigger.agent_profile_id = sqlc.narg(agent_profile_id)::uuid)
  AND (sqlc.narg(agent_id)::uuid IS NULL OR trigger.agent_id = sqlc.narg(agent_id)::uuid)
  AND (sqlc.narg(app_id)::uuid IS NULL OR trigger.app_id = sqlc.narg(app_id)::uuid)
)
SELECT id, org_id, project_id, name, agent_profile_id, agent_id,
       app_id, app_settings, last_run,
       cron_expression, timezone, message_template, delivery_mode, enabled,
       last_fired_at, next_fire_after, failure_report, idempotency_key,
       created_at, updated_at, sort_key, sort_is_null
FROM listed
WHERE sqlc.arg(cursor_set)::boolean = false
   OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
   OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN sort_key END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN sort_key END DESC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = false THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean = true THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: GetCronTriggerForUpdate :one
SELECT trigger.id, project.org_id, trigger.project_id, trigger.name,
       trigger.agent_profile_id, trigger.agent_id,
       trigger.app_id, trigger.app_settings,
       trigger.cron_expression, trigger.timezone, coalesce(trigger.message_template, '')::text AS message_template,
       trigger.delivery_mode, trigger.enabled, trigger.last_fired_at, trigger.next_fire_after,
       trigger.failure_report,
       CASE WHEN receipt.id IS NOT NULL THEN jsonb_build_object(
           'state', CASE receipt.state WHEN 'pending' THEN 'queued' ELSE receipt.state END,
           'created_at', receipt.created_at, 'updated_at', receipt.updated_at,
           'failure_message', CASE WHEN receipt.state = 'failed' THEN 'Scheduled app action failed.' ELSE NULL END
       ) END::jsonb AS last_run,
       coalesce(trigger.idempotency_key, '') AS idempotency_key,
       trigger.created_at, trigger.updated_at
FROM cron_triggers trigger
JOIN projects project ON project.id = trigger.project_id
LEFT JOIN integration_inbox receipt ON receipt.id = trigger.last_app_receipt_id
    AND receipt.project_id = trigger.project_id AND receipt.app_id = trigger.app_id
    AND receipt.source = 'scheduled'
WHERE trigger.project_id = sqlc.arg(project_id)
  AND trigger.id = sqlc.arg(id)
  AND trigger.deleted_at IS NULL
FOR UPDATE OF trigger;

-- name: UpdateCronTrigger :one
UPDATE cron_triggers
SET name = sqlc.arg(name),
    cron_expression = sqlc.arg(cron_expression),
    timezone = sqlc.arg(timezone),
    message_template = NULLIF(sqlc.arg(message_template)::text, ''),
    app_settings = sqlc.narg(app_settings),
    delivery_mode = sqlc.arg(delivery_mode),
    enabled = sqlc.arg(enabled),
    next_fire_after = sqlc.narg(next_fire_after),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
RETURNING id, project_id, name, agent_profile_id, agent_id,
          app_id, app_settings,
          cron_expression, timezone, coalesce(message_template, '')::text AS message_template, delivery_mode, enabled,
          last_fired_at, next_fire_after, failure_report,
          coalesce(idempotency_key, '') AS idempotency_key,
          created_at, updated_at;

-- name: DeleteCronTrigger :execrows
UPDATE cron_triggers
SET deleted_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: CountActiveCronTriggersForProject :one
SELECT count(*) AS active_count
FROM cron_triggers trigger
WHERE trigger.project_id = sqlc.arg(project_id)
  AND trigger.deleted_at IS NULL;

-- name: DeleteCronTriggersForAgentProfile :execrows
UPDATE cron_triggers
SET deleted_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_profile_id = sqlc.arg(agent_profile_id)
  AND deleted_at IS NULL;

-- name: DeleteCronTriggersForAgent :execrows
UPDATE cron_triggers
SET deleted_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND deleted_at IS NULL;

-- name: SelectDueCronTriggers :many
SELECT trigger.id, project.org_id, trigger.project_id, trigger.name,
       trigger.agent_profile_id, trigger.agent_id,
       trigger.app_id, trigger.app_settings,
       trigger.cron_expression, trigger.timezone, coalesce(trigger.message_template, '')::text AS message_template,
       trigger.delivery_mode, trigger.last_fired_at, trigger.next_fire_after
FROM cron_triggers trigger
JOIN projects project ON project.id = trigger.project_id AND project.deleted_at IS NULL
WHERE trigger.enabled
  AND trigger.deleted_at IS NULL
  AND trigger.next_fire_after <= transaction_timestamp()
  AND (trigger.claimed_until IS NULL OR trigger.claimed_until < transaction_timestamp())
ORDER BY trigger.next_fire_after ASC, trigger.id ASC
LIMIT sqlc.arg(row_limit)::bigint
FOR UPDATE OF trigger SKIP LOCKED;

-- name: ClaimCronTrigger :execrows
UPDATE cron_triggers
SET claimed_until = sqlc.arg(claimed_until),
    claim_token = sqlc.arg(claim_token),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: CompleteCronTriggerFiring :execrows
UPDATE cron_triggers
SET last_fired_at = CASE WHEN sqlc.arg(fired)::boolean THEN transaction_timestamp() ELSE last_fired_at END,
    failure_report = CASE WHEN sqlc.arg(fired)::boolean THEN NULL ELSE failure_report END,
    next_fire_after = sqlc.narg(next_fire_after),
    claimed_until = NULL,
    claim_token = NULL,
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND claim_token = sqlc.arg(claim_token)
  AND deleted_at IS NULL;

-- name: DisableCronTrigger :execrows
UPDATE cron_triggers
SET enabled = false,
    next_fire_after = NULL,
    claimed_until = NULL,
    claim_token = NULL,
    failure_report = jsonb_build_object(
        'message', sqlc.arg(failure_message)::text,
        'will_retry', false,
        'failed_at', to_char(statement_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    ),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: RecordCronTriggerFailure :execrows
UPDATE cron_triggers
SET failure_report = jsonb_build_object(
        'message', sqlc.arg(failure_message)::text,
        'will_retry', sqlc.arg(will_retry)::boolean,
        'failed_at', to_char(statement_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    ),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND claim_token = sqlc.arg(claim_token)
  AND deleted_at IS NULL;

-- name: DeleteCronTriggersForApp :execrows
UPDATE cron_triggers
SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND deleted_at IS NULL;

-- Use a fresh statement after locking: statement_timestamp() does not advance during lock waits.
-- name: CronTriggerClaimIsLive :one
SELECT EXISTS (
    SELECT 1 FROM cron_triggers
    WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
      AND claim_token = sqlc.arg(claim_token) AND claimed_until > statement_timestamp()
      AND deleted_at IS NULL
)::boolean AS live;

-- name: ReleaseCronTriggerClaim :execrows
UPDATE cron_triggers
SET claimed_until = NULL, claim_token = NULL, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND claim_token = sqlc.arg(claim_token) AND deleted_at IS NULL;

-- name: SetCronTriggerAppReceipt :execrows
UPDATE cron_triggers
SET last_app_receipt_id = sqlc.arg(receipt_id), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND claim_token = sqlc.arg(claim_token) AND claimed_until > statement_timestamp() AND deleted_at IS NULL;

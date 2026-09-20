-- Callers hold the active project/app gates and conversation lock. The
-- shared app row fences edits without taking a profile lock in reverse order.
-- name: GetAppProfileChoiceAppForShare :one
SELECT id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR SHARE;

-- name: GetAppProfileChoice :one
SELECT id, project_id, app_id, owner_receipt_id, address_kind, address_ref, source_key, event, payload, options, selected_key, selected_by, message_channel_id, message_id, expires_at, created_at, updated_at
FROM app_profile_choices
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND id = sqlc.arg(id);

-- Source lookup precedes pending lookup: expired or selected sources never revive.
-- name: GetAppProfileChoiceBySource :one
SELECT id, project_id, app_id, owner_receipt_id, address_kind, address_ref, source_key, event, payload, options, selected_key, selected_by, message_channel_id, message_id, expires_at, created_at, updated_at
FROM app_profile_choices
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND source_key = sqlc.arg(source_key);

-- A selected request owns the menu until its decided work settles, even after
-- expiry. Failed frozen work retains its reservation; failed unplanned work does
-- not. Unpublished menus wait only while their owner can still publish them;
-- abandoned sources remain exact-replay barriers. Both branches yield at most one ID.
-- name: FindPendingAppProfileChoice :one
WITH candidates AS (
    (SELECT choice.id, 0 AS priority
     FROM app_profile_choices choice
     JOIN integration_inbox inbox ON inbox.project_id = choice.project_id AND inbox.app_id = choice.app_id
       AND inbox.receipt_key = 'choice:' || choice.id::text
     WHERE choice.project_id = sqlc.arg(project_id) AND choice.app_id = sqlc.arg(app_id)
       AND choice.address_kind = sqlc.arg(address_kind)
       AND choice.address_ref = sqlc.arg(address_ref) AND choice.selected_key IS NOT NULL
       AND (inbox.state IN ('pending', 'processing') OR (inbox.state = 'failed' AND inbox.plan IS NOT NULL))
     LIMIT 1)
    UNION ALL
    (SELECT choice.id, 1 AS priority
     FROM app_profile_choices choice
     WHERE choice.project_id = sqlc.arg(project_id) AND choice.app_id = sqlc.arg(app_id)
       AND choice.address_kind = sqlc.arg(address_kind)
       AND choice.address_ref = sqlc.arg(address_ref)
       AND choice.selected_key IS NULL AND choice.expires_at > statement_timestamp()
       AND (choice.message_id IS NOT NULL OR EXISTS (
           SELECT 1 FROM integration_inbox owner
           WHERE owner.id = choice.owner_receipt_id AND owner.project_id = choice.project_id
             AND owner.app_id = choice.app_id AND owner.state IN ('pending', 'processing')
       ))
     ORDER BY choice.expires_at, choice.id
     LIMIT 1)
)
SELECT choice.id, choice.project_id, choice.app_id, choice.owner_receipt_id, choice.address_kind, choice.address_ref, choice.source_key, choice.event, choice.payload, choice.options, choice.selected_key, choice.selected_by, choice.message_channel_id, choice.message_id, choice.expires_at, choice.created_at, choice.updated_at
FROM candidates JOIN app_profile_choices choice ON choice.id = candidates.id
ORDER BY candidates.priority
LIMIT 1;

-- Read without locking another receipt: the caller already holds its own inbox
-- lease then the conversation gate. Every receipt, including a plain reply,
-- belongs to one app; another app's chooser cannot reserve its conversation.
-- Failed unplanned work has no frozen launch reservation and permits a new request.
-- name: FindUnplannedAppProfileChoiceReservation :one
SELECT inbox.id, inbox.state
FROM app_profile_choices choice
JOIN integration_inbox inbox ON inbox.project_id = choice.project_id AND inbox.app_id = choice.app_id
  AND inbox.receipt_key = 'choice:' || choice.id::text
WHERE choice.project_id = sqlc.arg(project_id) AND choice.app_id = sqlc.arg(app_id)
  AND choice.address_kind = sqlc.arg(address_kind) AND choice.address_ref = sqlc.arg(address_ref)
  AND choice.selected_key IS NOT NULL AND inbox.id <> sqlc.arg(receipt_id)
  AND inbox.plan IS NULL AND inbox.state IN ('pending', 'processing')
LIMIT 1;

-- Unusable menus release the pending conversation without erasing replay facts.
-- Accepted selections and their durable handoff never expire through this path.
-- name: ExpireAppProfileChoice :exec
UPDATE app_profile_choices
SET expires_at = LEAST(expires_at, statement_timestamp()), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND id = sqlc.arg(id)
  AND selected_key IS NULL AND expires_at > statement_timestamp();

-- name: InsertAppProfileChoice :one
INSERT INTO app_profile_choices(project_id, app_id, owner_receipt_id, address_kind, address_ref, source_key, event, payload, options)
VALUES (sqlc.arg(project_id), sqlc.arg(app_id), sqlc.arg(owner_receipt_id), sqlc.arg(address_kind), sqlc.arg(address_ref),
        sqlc.arg(source_key), sqlc.arg(event), sqlc.arg(payload), sqlc.arg(options))
RETURNING id, project_id, app_id, owner_receipt_id, address_kind, address_ref, source_key, event, payload, options, selected_key, selected_by, message_channel_id, message_id, expires_at, created_at, updated_at;

-- Source and verified body change together, and only for a fenced attachment sibling.
-- name: UpdateAppProfileChoiceSource :one
UPDATE app_profile_choices
SET event = sqlc.arg(event), payload = sqlc.arg(payload), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND id = sqlc.arg(id)
  AND source_key = sqlc.arg(source_key) AND selected_key IS NULL AND expires_at > statement_timestamp()
RETURNING id, project_id, app_id, owner_receipt_id, address_kind, address_ref, source_key, event, payload, options, selected_key, selected_by, message_channel_id, message_id, expires_at, created_at, updated_at;

-- name: RecordAppProfileChoiceMessage :execrows
UPDATE app_profile_choices
SET message_channel_id = sqlc.arg(message_channel_id), message_id = sqlc.arg(message_id), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND id = sqlc.arg(id)
  AND message_id IS NULL;

-- The separate post-lock timestamp check fences expiry and the caller's source revision.
-- name: SelectAppProfileChoice :one
UPDATE app_profile_choices
SET selected_key = sqlc.arg(selected_key), selected_by = sqlc.arg(selected_by), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND id = sqlc.arg(id)
  AND selected_key IS NULL AND expires_at > statement_timestamp()
  AND updated_at = sqlc.arg(expected_updated_at)
RETURNING id, project_id, app_id, owner_receipt_id, address_kind, address_ref, source_key, event, payload, options, selected_key, selected_by, message_channel_id, message_id, expires_at, created_at, updated_at;

-- This is the sole trusted normalized-event handoff. Ordinary verified receipts
-- use InsertIntegrationInboxReceipt, which cannot populate events.
-- name: InsertAppProfileChoiceInboxReceipt :one
INSERT INTO integration_inbox(project_id, app_id, receipt_key, payload, events)
VALUES (sqlc.arg(project_id), sqlc.arg(app_id), sqlc.arg(receipt_key), sqlc.arg(payload), sqlc.arg(events))
ON CONFLICT (project_id, app_id, receipt_key) DO NOTHING
RETURNING id, project_id, app_id, receipt_key, payload, source, events, plan, progress, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at;

-- Retain live source bookkeeping beyond expiry, including work awaiting recovery.
-- Deleted app/project/org scopes no longer need payloads or replay facts;
-- disconnected live apps still retain failed work for operator recovery.
-- Bound each indexed path separately so empty sweeps do not scan recent history.
-- name: CleanupAppProfileChoices :execrows
WITH expired AS MATERIALIZED (
    SELECT choice.id FROM app_profile_choices choice
    WHERE choice.expires_at < statement_timestamp() - sqlc.arg(retention_milliseconds)::bigint * interval '1 millisecond'
      AND NOT EXISTS (
          SELECT 1 FROM integration_inbox inbox
          WHERE inbox.project_id = choice.project_id AND inbox.app_id = choice.app_id
            AND inbox.receipt_key = 'choice:' || choice.id::text
            AND inbox.state IN ('pending', 'processing', 'failed')
      )
    ORDER BY choice.expires_at, choice.id
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
), deleted_apps AS MATERIALIZED (
    SELECT app.project_id, app.id
    FROM project_apps app
    JOIN projects project ON project.id = app.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE app.deleted_at IS NOT NULL OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
), deleted AS MATERIALIZED (
    SELECT obsolete.id
    FROM deleted_apps app
    CROSS JOIN LATERAL (
        SELECT choice.id FROM app_profile_choices choice
        WHERE choice.project_id = app.project_id AND choice.app_id = app.id
        ORDER BY choice.source_key
        LIMIT sqlc.arg(row_limit)
        FOR UPDATE SKIP LOCKED
    ) obsolete
    LIMIT sqlc.arg(row_limit)
), candidates AS (
    SELECT id FROM expired UNION SELECT id FROM deleted
    LIMIT sqlc.arg(row_limit)
)
DELETE FROM app_profile_choices choice USING candidates WHERE choice.id = candidates.id;

-- name: GetAppProfileChoiceAppID :one
-- Private callback routing only; provider verification and choice authorization follow.
SELECT app_id FROM app_profile_choices WHERE id = $1;

-- name: GetIntegrationProfileChoiceIntegrationForShare :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision
FROM project_integrations
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR SHARE;

-- name: FindPendingIntegrationProfileChoice :one
WITH candidates AS (
    (SELECT choice.id, 0 AS priority
     FROM integration_states choice
     JOIN integration_inbox inbox ON inbox.project_id = choice.project_id AND inbox.integration_id = choice.integration_id
       AND inbox.receipt_key = 'choice:' || choice.id::text
     WHERE choice.kind = 'profile_choice' AND choice.project_id = sqlc.arg(project_id) AND choice.integration_id = sqlc.arg(integration_id)
       AND choice.scope_kind = sqlc.arg(address_kind)::text
       AND choice.scope_ref = sqlc.arg(address_ref)::text AND COALESCE(choice.data->>'selected_key', '') <> ''
       AND inbox.state IN ('pending', 'processing')
     LIMIT 1)
    UNION ALL
    (SELECT choice.id, 1 AS priority
     FROM integration_states choice
     WHERE choice.kind = 'profile_choice' AND choice.project_id = sqlc.arg(project_id) AND choice.integration_id = sqlc.arg(integration_id)
       AND choice.scope_kind = sqlc.arg(address_kind)::text
       AND choice.scope_ref = sqlc.arg(address_ref)::text
       AND COALESCE(choice.data->>'selected_key', '') = '' AND choice.expires_at > statement_timestamp()
       AND (choice.data->>'message_id' <> '' OR EXISTS (
           SELECT 1 FROM integration_inbox owner
           WHERE owner.id = CASE WHEN choice.kind = 'profile_choice'
                                THEN (choice.data->>'owner_receipt_id')::uuid END AND owner.project_id = choice.project_id
             AND owner.integration_id = choice.integration_id AND owner.state IN ('pending', 'processing')
       ))
     ORDER BY choice.expires_at, choice.id
     LIMIT 1)
)
SELECT choice.id, choice.project_id, choice.integration_id, choice.kind, choice.key, choice.scope_kind, choice.scope_ref, choice.data, choice.revision, choice.expires_at, choice.created_at, choice.updated_at
FROM candidates JOIN integration_states choice ON choice.id = candidates.id
ORDER BY candidates.priority
LIMIT 1;

-- name: FindUnplannedIntegrationProfileChoiceReservation :one
SELECT inbox.id, inbox.state
FROM integration_states choice
JOIN integration_inbox inbox ON inbox.project_id = choice.project_id AND inbox.integration_id = choice.integration_id
  AND inbox.receipt_key = 'choice:' || choice.id::text
WHERE choice.kind = 'profile_choice' AND choice.project_id = sqlc.arg(project_id) AND choice.integration_id = sqlc.arg(integration_id)
  AND choice.scope_kind = sqlc.arg(address_kind)::text AND choice.scope_ref = sqlc.arg(address_ref)::text
  AND COALESCE(choice.data->>'selected_key', '') <> '' AND inbox.id <> sqlc.arg(receipt_id)
  AND inbox.plan IS NULL AND inbox.state IN ('pending', 'processing')
LIMIT 1;

-- name: InsertIntegrationProfileChoiceInboxReceipt :one
INSERT INTO integration_inbox(project_id, integration_id, receipt_key, payload, events)
VALUES (sqlc.arg(project_id), sqlc.arg(integration_id), sqlc.arg(receipt_key), sqlc.arg(payload), sqlc.arg(events))
ON CONFLICT (project_id, integration_id, receipt_key) DO NOTHING
RETURNING id, project_id, integration_id, receipt_key, payload, source, events, plan, state, attempt_count, available_at, claim_token, claim_expires_at, last_error, created_at, updated_at, completed_at;

-- Menu expiry must not remove the source replay barrier before its inbox receipt expires.
-- name: CleanupExpiredIntegrationProfileChoices :execrows
WITH candidates AS (
    SELECT choice.id FROM integration_states choice
    WHERE choice.kind = 'profile_choice' AND choice.expires_at < statement_timestamp() - sqlc.arg(retention_milliseconds)::bigint * interval '1 millisecond'
      AND NOT EXISTS (
          SELECT 1 FROM integration_inbox inbox
          WHERE inbox.project_id = choice.project_id AND inbox.integration_id = choice.integration_id
            AND inbox.receipt_key = 'choice:' || choice.id::text
      )
    ORDER BY choice.expires_at, choice.id
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM integration_states choice USING candidates WHERE choice.id = candidates.id;

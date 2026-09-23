-- Lock order: project/app gates, conversation gate, then agent locks.
-- name: LockAppConversation :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    'app_conversation:' || jsonb_build_array(sqlc.arg(project_id)::uuid, sqlc.arg(app_id)::uuid,
        sqlc.arg(kind)::text, sqlc.arg(ref)::text)::text, 0));

-- Keep retired selections so a later comment cannot launch a replacement agent.
-- name: ListConversationSelections :many
SELECT id, project_id, agent_id, app_id, provider_ref,
       provider_ref_kind, display_name, provider_metadata, selection_slot,
       deleted_at, created_at, updated_at
FROM app_targets target
WHERE target.project_id = sqlc.arg(project_id) AND target.app_id = sqlc.arg(app_id)
  AND target.provider_ref_kind = sqlc.arg(kind) AND target.provider_ref = sqlc.arg(ref)
  AND target.selection_slot IS NOT NULL
ORDER BY target.id;

-- name: GetAppSelectionTarget :one
SELECT id, project_id, agent_id, app_id, provider_ref,
       provider_ref_kind, display_name, provider_metadata, selection_slot,
       deleted_at, created_at, updated_at
FROM app_targets
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND provider_ref_kind = sqlc.arg(kind) AND provider_ref = sqlc.arg(ref)
  AND selection_slot = sqlc.arg(slot);

-- name: GetAgentConversationTarget :one
SELECT id, project_id, agent_id, app_id, provider_ref,
       provider_ref_kind, display_name, provider_metadata, selection_slot,
       deleted_at, created_at, updated_at
FROM app_targets
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id)
  AND app_id = sqlc.arg(app_id)
  AND provider_ref_kind = sqlc.arg(kind) AND provider_ref = sqlc.arg(ref)
  AND deleted_at IS NULL;

-- name: GetConversationDisplayName :one
SELECT display_name
FROM app_targets
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND provider_ref_kind = sqlc.arg(kind) AND provider_ref = sqlc.arg(ref)
  AND deleted_at IS NULL AND display_name <> ''
ORDER BY updated_at DESC, id DESC
LIMIT 1;

-- name: InsertAppConversationTarget :one
INSERT INTO app_targets(project_id, agent_id, app_id,
    provider_ref_kind, provider_ref, display_name, selection_slot, created_at, updated_at)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(app_id),
    sqlc.arg(kind), sqlc.arg(ref), sqlc.arg(display_name), sqlc.narg(slot),
    transaction_timestamp(), transaction_timestamp())
ON CONFLICT DO NOTHING
RETURNING id, project_id, agent_id, app_id, provider_ref,
          provider_ref_kind, display_name, provider_metadata, selection_slot,
          deleted_at, created_at, updated_at;

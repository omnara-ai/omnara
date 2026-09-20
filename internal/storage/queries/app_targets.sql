-- The conversation gate serializes launcher selection with confirmed follows.
-- Acquire it after project/app gates and before agent locks.
-- name: LockAppConversation :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    'app_conversation:' || jsonb_build_array(sqlc.arg(project_id)::uuid, sqlc.arg(app_id)::uuid,
        sqlc.arg(kind)::text, sqlc.arg(ref)::text)::text, 0));

-- Retired selections intentionally remain visible: stopping a selected agent
-- must not cause the next comment to launch a replacement.
-- name: ListConversationSelections :many
SELECT id, project_id, agent_id, app_id, target_ref, provider_ref,
       provider_ref_kind, display_name, provider_metadata, routing_role, selection_slot,
       deleted_at, created_at, updated_at
FROM integration_targets target
WHERE target.project_id = sqlc.arg(project_id) AND target.app_id = sqlc.arg(app_id)
  AND target.provider_ref_kind = sqlc.arg(kind) AND target.provider_ref = sqlc.arg(ref)
  AND (target.routing_role = 'selected' OR (target.routing_role = 'followed' AND target.deleted_at IS NULL AND EXISTS (
    SELECT 1 FROM agent_listeners listener
    JOIN agents agent ON agent.project_id = listener.project_id AND agent.id = listener.agent_id
    WHERE listener.project_id = target.project_id AND listener.agent_id = target.agent_id
      AND listener.app_id = target.app_id
      AND listener.scope_kind = target.provider_ref_kind AND listener.scope_ref = target.provider_ref
      AND listener.active AND listener.origin = 'runtime'
      AND listener.source_config_id = agent.current_config_id AND agent.state = 'active'
  )))
ORDER BY target.id;

-- name: GetAppSelectionTarget :one
SELECT id, project_id, agent_id, app_id, target_ref, provider_ref,
       provider_ref_kind, display_name, provider_metadata, routing_role, selection_slot,
       deleted_at, created_at, updated_at
FROM integration_targets
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND provider_ref_kind = sqlc.arg(kind) AND provider_ref = sqlc.arg(ref)
  AND routing_role = 'selected' AND selection_slot = sqlc.arg(slot);

-- name: GetAgentConversationTarget :one
SELECT id, project_id, agent_id, app_id, target_ref, provider_ref,
       provider_ref_kind, display_name, provider_metadata, routing_role, selection_slot,
       deleted_at, created_at, updated_at
FROM integration_targets
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id)
  AND app_id = sqlc.arg(app_id)
  AND provider_ref_kind = sqlc.arg(kind) AND provider_ref = sqlc.arg(ref)
  AND deleted_at IS NULL;

-- Presentation only: several agents may have targets at this exact address.
-- Reuse the latest known nonempty label without selecting a routing authority.
-- name: GetConversationDisplayName :one
SELECT display_name
FROM integration_targets
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND provider_ref_kind = sqlc.arg(kind) AND provider_ref = sqlc.arg(ref)
  AND deleted_at IS NULL AND display_name <> ''
ORDER BY updated_at DESC, id DESC
LIMIT 1;

-- name: InsertAppConversationTarget :one
INSERT INTO integration_targets(project_id, agent_id, app_id, target_ref,
    provider_ref_kind, provider_ref, display_name, routing_role, selection_slot, created_at, updated_at)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(app_id), sqlc.arg(target_ref),
    sqlc.arg(kind), sqlc.arg(ref), sqlc.arg(display_name), sqlc.arg(routing_role), sqlc.narg(slot),
    transaction_timestamp(), transaction_timestamp())
ON CONFLICT DO NOTHING
RETURNING id, project_id, agent_id, app_id, target_ref, provider_ref,
          provider_ref_kind, display_name, provider_metadata, routing_role, selection_slot,
          deleted_at, created_at, updated_at;

-- name: MarkConversationTargetFollowed :exec
UPDATE integration_targets SET routing_role = 'followed', updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND id = sqlc.arg(id)
  AND routing_role = 'attribution' AND deleted_at IS NULL;

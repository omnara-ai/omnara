-- name: DeleteAppTargets :exec
UPDATE app_targets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND deleted_at IS NULL;

-- name: ListProjectAppAgentIDsForLifecycle :many
-- @sqlc-vet-disable app-targets-deleted-at
-- Include historical targets whose agents may still hold references to clear.
SELECT target.agent_id FROM app_targets target
WHERE target.project_id = sqlc.arg(project_id) AND target.app_id = sqlc.arg(app_id)
UNION
SELECT subscription.agent_id FROM app_subscriptions subscription
WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.app_id = sqlc.arg(app_id)
ORDER BY agent_id;

-- name: ClearDeletedAppTargetsFromAgents :exec
-- @sqlc-vet-disable app-targets-deleted-at
-- Clears agent references before soft deleting the app's targets.
UPDATE agents agent SET app_target_id = NULL, interaction_handler_key = NULL, interaction_handler_args = NULL, updated_at = statement_timestamp()
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.app_target_id IN (
    SELECT target.id FROM app_targets target
    WHERE target.project_id = sqlc.arg(project_id)
      AND target.app_id = sqlc.arg(app_id)
  );

-- name: GetAppTarget :one
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.app_id, target.provider_ref,
  target.provider_ref_kind, target.display_name, target.provider_metadata, target.selection_slot, target.deleted_at, target.created_at, target.updated_at
FROM app_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(id)
  AND target.deleted_at IS NULL;

-- name: UpdateAppTargetDisplayNamesByProviderRefPrefix :execrows
UPDATE app_targets
SET display_name = sqlc.arg(display_name),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND app_id = sqlc.arg(app_id)
  AND deleted_at IS NULL
  AND split_part(provider_ref, ':', 1) = sqlc.arg(provider_ref_prefix)
  AND display_name IS DISTINCT FROM sqlc.arg(display_name);

-- name: DeleteIntegrationTargets :exec
UPDATE integration_targets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND deleted_at IS NULL;

-- name: ListProjectAppAgentIDsForLifecycle :many
-- @sqlc-vet-disable integration-targets-deleted-at
-- Include historical targets whose agents may still hold references to clear.
SELECT target.agent_id FROM integration_targets target
WHERE target.project_id = sqlc.arg(project_id) AND target.app_id = sqlc.arg(app_id)
UNION
SELECT subscription.agent_id FROM app_subscriptions subscription
WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.app_id = sqlc.arg(app_id)
ORDER BY agent_id;

-- name: ClearDeletedIntegrationTargetsFromAgents :exec
-- @sqlc-vet-disable integration-targets-deleted-at
-- Clears agent references before soft deleting the app's targets.
UPDATE agents agent SET integration_target_id = NULL, interaction_handler_key = NULL, interaction_handler_args = NULL, updated_at = statement_timestamp()
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.integration_target_id IN (
    SELECT target.id FROM integration_targets target
    WHERE target.project_id = sqlc.arg(project_id)
      AND target.app_id = sqlc.arg(app_id)
  );

-- name: GetIntegrationTarget :one
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.app_id, target.provider_ref,
  target.provider_ref_kind, target.display_name, target.provider_metadata, target.selection_slot, target.is_tool_context, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(id)
  AND target.deleted_at IS NULL;

-- A retired sending context still confines the agent; it must not mean unrestricted access.
-- name: GetAgentAppToolContext :one
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.app_id, target.provider_ref,
  target.provider_ref_kind, target.display_name, target.provider_metadata, target.selection_slot, target.is_tool_context, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.agent_id = sqlc.arg(agent_id)
  AND target.app_id = sqlc.arg(app_id)
  AND target.is_tool_context;

-- name: UpdateIntegrationTargetDisplayNamesByProviderRefPrefix :execrows
UPDATE integration_targets
SET display_name = sqlc.arg(display_name),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND app_id = sqlc.arg(app_id)
  AND deleted_at IS NULL
  AND split_part(provider_ref, ':', 1) = sqlc.arg(provider_ref_prefix)
  AND display_name IS DISTINCT FROM sqlc.arg(display_name);

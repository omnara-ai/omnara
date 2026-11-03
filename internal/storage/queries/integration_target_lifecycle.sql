-- name: DeleteIntegrationTargets :exec
UPDATE integration_targets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND deleted_at IS NULL;

-- name: ListIntegrationAgentIDsForLifecycle :many
-- @sqlc-vet-disable integration-targets-deleted-at
SELECT target.agent_id FROM integration_targets target
WHERE target.project_id = sqlc.arg(project_id) AND target.integration_id = sqlc.arg(integration_id)
UNION
SELECT subscription.agent_id FROM integration_subscriptions subscription
WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.integration_id = sqlc.arg(integration_id)
ORDER BY agent_id;

-- name: GetIntegrationTarget :one
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.integration_id, target.scope_ref,
  target.scope_kind, target.display_name, target.launch_key, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN projects project ON project.id = target.project_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(id)
  AND target.deleted_at IS NULL;

-- name: UpdateIntegrationTargetDisplayNamesByScopeRefPrefix :execrows
UPDATE integration_targets
SET display_name = sqlc.arg(display_name),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_id = sqlc.arg(integration_id)
  AND deleted_at IS NULL
  AND split_part(scope_ref, ':', 1) = sqlc.arg(scope_ref_prefix)
  AND display_name IS DISTINCT FROM sqlc.arg(display_name);

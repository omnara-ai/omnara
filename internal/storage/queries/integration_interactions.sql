-- name: GetInteractionSelection :one
SELECT current_config_id, interaction_target_id,
       coalesce(interaction_handler_key, '') AS handler_key, interaction_auto_select
FROM agents
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(agent_id);

-- name: GetInteractionCallbackAgent :one
SELECT agent_id
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(interaction_id)
  AND destination ->> 'integration_id' = sqlc.arg(integration_id)::text;

-- name: GetInteractionDestinationTarget :one
SELECT target.id, target.integration_id AS integration_id,
       target.scope_kind, target.scope_ref, target.display_name,
       integration.state AS integration_state
FROM integration_targets target
JOIN integrations integration
  ON integration.project_id = target.project_id AND integration.id = target.integration_id
WHERE target.project_id = sqlc.arg(project_id) AND target.agent_id = sqlc.arg(agent_id)
  AND target.id = sqlc.arg(target_id) AND target.deleted_at IS NULL
  AND integration.deleted_at IS NULL;

-- name: GetInteractionCallbackIntegrationID :one
SELECT (destination ->> 'integration_id')::uuid AS integration_id
FROM agent_interactions
WHERE id = $1 AND destination ->> 'integration_id' IS NOT NULL;

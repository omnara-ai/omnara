-- name: GetInteractionSelection :one
SELECT current_config_id, integration_target_id,
       coalesce(interaction_resource_key, '') AS resource_key
FROM agents
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(agent_id);

-- name: GetInteractionCallbackAgent :one
SELECT agent_id
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(interaction_id)
  AND destination ->> 'connection_id' = sqlc.arg(connection_id)::text;

-- name: SetInteractionSelection :execrows
UPDATE agents
SET integration_target_id = sqlc.narg(target_id)::uuid,
    interaction_resource_key = sqlc.narg(resource_key)::text,
    updated_at = statement_timestamp()
WHERE agents.project_id = sqlc.arg(project_id) AND agents.id = sqlc.arg(agent_id)
  AND ((sqlc.narg(target_id)::uuid IS NULL AND sqlc.narg(resource_key)::text IS NULL)
    OR (sqlc.narg(resource_key)::text <> '' AND EXISTS (
      SELECT 1 FROM integration_targets target
      JOIN integration_connections connection
        ON connection.project_id = target.project_id
       AND connection.id = target.integration_connection_id
      WHERE target.project_id = agents.project_id AND target.agent_id = agents.id
        AND target.id = sqlc.narg(target_id)::uuid AND target.deleted_at IS NULL
        AND connection.deleted_at IS NULL AND connection.state = 'active'
    )));

-- name: GetInteractionDestinationTarget :one
SELECT target.id, target.integration_connection_id AS connection_id,
       target.provider_ref_kind, target.provider_ref, target.target_ref, target.display_name,
       connection.provider, connection.state AS connection_state
FROM integration_targets target
JOIN integration_connections connection
  ON connection.project_id = target.project_id AND connection.id = target.integration_connection_id
WHERE target.project_id = sqlc.arg(project_id) AND target.agent_id = sqlc.arg(agent_id)
  AND target.id = sqlc.arg(target_id) AND target.deleted_at IS NULL
  AND connection.deleted_at IS NULL;

-- name: ListInteractionDestinationTargets :many
SELECT target.id, target.integration_connection_id AS connection_id,
       target.provider_ref_kind, target.provider_ref, target.target_ref, target.display_name,
       connection.provider, connection.state AS connection_state
FROM integration_targets target
JOIN integration_connections connection
  ON connection.project_id = target.project_id AND connection.id = target.integration_connection_id
WHERE target.project_id = sqlc.arg(project_id) AND target.agent_id = sqlc.arg(agent_id)
  AND target.deleted_at IS NULL AND connection.deleted_at IS NULL
ORDER BY target.created_at, target.id;

-- name: RecordAgentInteractionPresentationReceipt :execrows
UPDATE agent_interactions interaction
SET presentation_receipt = sqlc.arg(receipt)::jsonb
WHERE interaction.agent_id = sqlc.arg(agent_id) AND interaction.id = sqlc.arg(id)
  AND interaction.destination = sqlc.arg(destination)::jsonb
  AND interaction.presentation_receipt IS NULL
  AND EXISTS (
    SELECT 1 FROM agents agent
    WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = interaction.agent_id
  );

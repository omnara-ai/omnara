-- name: GetInteractionSelection :one
SELECT current_config_id, integration_target_id,
       coalesce(interaction_handler_key, '') AS handler_key, interaction_handler_args AS handler_args
FROM agents
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(agent_id);

-- name: GetInteractionCallbackAgent :one
SELECT agent_id
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(interaction_id)
  AND destination ->> 'app_id' = sqlc.arg(app_id)::text;

-- name: SetInteractionSelection :execrows
UPDATE agents
SET integration_target_id = sqlc.narg(target_id)::uuid,
    interaction_handler_key = sqlc.narg(handler_key)::text,
    interaction_handler_args = sqlc.narg(handler_args)::jsonb,
    updated_at = statement_timestamp()
WHERE agents.project_id = sqlc.arg(project_id) AND agents.id = sqlc.arg(agent_id)
  AND ((sqlc.narg(target_id)::uuid IS NULL AND sqlc.narg(handler_key)::text IS NULL AND sqlc.narg(handler_args)::jsonb IS NULL)
    OR (sqlc.narg(handler_key)::text <> '' AND jsonb_typeof(sqlc.narg(handler_args)::jsonb) = 'object' AND EXISTS (
      SELECT 1 FROM integration_targets target
      JOIN project_apps app
        ON app.project_id = target.project_id
       AND app.id = target.app_id
      WHERE target.project_id = agents.project_id AND target.agent_id = agents.id
        AND target.id = sqlc.narg(target_id)::uuid AND target.deleted_at IS NULL
        AND app.deleted_at IS NULL AND app.state = 'active'
    )));

-- name: GetInteractionDestinationTarget :one
SELECT target.id, target.app_id AS app_id,
       target.provider_ref_kind, target.provider_ref, target.target_ref, target.display_name,
       app.provider, app.state AS app_state
FROM integration_targets target
JOIN project_apps app
  ON app.project_id = target.project_id AND app.id = target.app_id
WHERE target.project_id = sqlc.arg(project_id) AND target.agent_id = sqlc.arg(agent_id)
  AND target.id = sqlc.arg(target_id) AND target.deleted_at IS NULL
  AND app.deleted_at IS NULL;

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

-- name: GetInteractionCallbackAppID :one
-- Private callback routing only; the captured prompt is checked again during resolution.
SELECT (destination ->> 'app_id')::uuid AS app_id
FROM agent_interactions
WHERE id = $1 AND destination ->> 'app_id' IS NOT NULL;

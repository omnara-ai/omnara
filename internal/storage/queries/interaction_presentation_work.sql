-- name: ListPendingInteractionPresentations :many
WITH app_types AS (
    SELECT DISTINCT unnest(sqlc.arg(app_types)::text[]) AS app_type
)
SELECT pending.project_id, pending.agent_id, pending.id
FROM app_types
CROSS JOIN LATERAL (
    -- Each app type uses the pending index's equality prefix and ordering.
    -- Only this bounded set participates in the final oldest-first merge.
    SELECT agent.project_id, interaction.agent_id, interaction.id, interaction.created_at
    FROM agent_interactions interaction
    JOIN agents agent ON agent.id = interaction.agent_id
    JOIN projects project ON project.id = agent.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE interaction.state = 'open'
      AND interaction.destination IS NOT NULL
      AND interaction.presentation_attempted_at IS NULL
      AND interaction.presentation_receipt IS NULL
      AND interaction.destination ->> 'app_type' = app_types.app_type
      AND agent.state = 'active'
      AND project.deleted_at IS NULL AND org.deleted_at IS NULL
    ORDER BY interaction.created_at, interaction.agent_id, interaction.id
    LIMIT least(greatest(sqlc.arg(batch_limit)::integer, 1), 100)
) pending
ORDER BY pending.created_at, pending.agent_id, pending.id
LIMIT least(greatest(sqlc.arg(batch_limit)::integer, 1), 100);

-- name: ClaimInteractionPresentation :execrows
UPDATE agent_interactions interaction
SET presentation_attempted_at = statement_timestamp()
WHERE interaction.agent_id = sqlc.arg(agent_id) AND interaction.id = sqlc.arg(id)
  AND interaction.state = 'open'
  AND interaction.destination IS NOT NULL
  AND interaction.presentation_attempted_at IS NULL
  AND interaction.presentation_receipt IS NULL
  AND EXISTS (
    SELECT 1 FROM agents agent
    JOIN projects project ON project.id = agent.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE agent.id = interaction.agent_id AND agent.project_id = sqlc.arg(project_id)
      AND agent.state = 'active'
      AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  );

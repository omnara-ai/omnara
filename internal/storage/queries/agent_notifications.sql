-- name: ListAgentNotificationAncestors :many
WITH RECURSIVE ancestry AS (
  SELECT agent.id, agent.parent_agent_id, 0 AS depth
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  UNION ALL
  SELECT parent.id, parent.parent_agent_id, ancestry.depth + 1
  FROM agents parent
  JOIN ancestry ON parent.id = ancestry.parent_agent_id
  WHERE parent.project_id = sqlc.arg(project_id)
    AND ancestry.depth < sqlc.arg(max_depth)::integer
)
SELECT id::uuid
FROM ancestry
WHERE depth > 0
ORDER BY depth;

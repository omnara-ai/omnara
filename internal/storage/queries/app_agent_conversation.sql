-- name: InsertAgentAppConversation :execrows
INSERT INTO app_states(project_id, app_id, kind, key, data)
SELECT agent.project_id, app.id, sqlc.arg(kind), agent.id::text, sqlc.arg(data)
FROM agents agent
JOIN project_apps app ON app.project_id = agent.project_id AND app.id = sqlc.arg(app_id)
WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = sqlc.arg(agent_id)
  AND agent.state = 'active' AND app.state = 'active' AND app.deleted_at IS NULL;

-- name: InsertAgentIntegrationConversation :execrows
INSERT INTO integration_states(project_id, integration_id, kind, key, data)
SELECT agent.project_id, integration.id, sqlc.arg(kind), agent.id::text, sqlc.arg(data)
FROM agents agent
JOIN project_integrations integration ON integration.project_id = agent.project_id AND integration.id = sqlc.arg(integration_id)
WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = sqlc.arg(agent_id)
  AND agent.state = 'active' AND integration.state = 'active' AND integration.deleted_at IS NULL;

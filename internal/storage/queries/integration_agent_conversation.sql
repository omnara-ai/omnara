-- name: InsertAgentIntegrationConversation :execrows
INSERT INTO integration_states(project_id, integration_id, kind, key, data)
SELECT agent.project_id, integration.id, sqlc.arg(kind), agent.id::text, sqlc.arg(data)
FROM agents agent
JOIN project_integrations integration ON integration.project_id = agent.project_id AND integration.id = sqlc.arg(integration_id)
WHERE agent.project_id = sqlc.arg(project_id) AND agent.id = sqlc.arg(agent_id)
  AND agent.state = 'active' AND integration.state = 'active' AND integration.deleted_at IS NULL;

-- name: GetAssignedIntegrationConversationTarget :one
SELECT target.id
FROM integration_targets target
JOIN project_integrations integration ON integration.project_id = target.project_id AND integration.id = target.integration_id
WHERE target.project_id = sqlc.arg(project_id) AND target.agent_id = sqlc.arg(agent_id)
  AND target.integration_id = sqlc.arg(integration_id) AND target.provider_ref_kind = sqlc.arg(kind)
  AND target.provider_ref = sqlc.arg(ref) AND target.deleted_at IS NULL
  AND integration.deleted_at IS NULL AND integration.state = 'active';

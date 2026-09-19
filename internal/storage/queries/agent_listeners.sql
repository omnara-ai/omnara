-- name: UpsertAgentListener :one
INSERT INTO agent_listeners(project_id, agent_id, connection_id, resource_key, scope_kind, scope_ref, events, source_config_id, tool_call_id)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(connection_id), sqlc.arg(resource_key), sqlc.arg(scope_kind),
        sqlc.arg(scope_ref), sqlc.arg(events)::text[], sqlc.arg(source_config_id), sqlc.narg(tool_call_id))
ON CONFLICT (project_id, agent_id, resource_key, scope_kind, scope_ref, (tool_call_id IS NOT NULL))
DO UPDATE SET connection_id = EXCLUDED.connection_id, events = EXCLUDED.events,
              source_config_id = EXCLUDED.source_config_id, tool_call_id = EXCLUDED.tool_call_id, active = true, updated_at = statement_timestamp()
RETURNING id, project_id, agent_id, connection_id, resource_key, scope_kind, scope_ref, events,
          source_config_id, tool_call_id, active, created_at, updated_at;

-- name: ListActiveAgentListeners :many
SELECT id, project_id, agent_id, connection_id, resource_key, scope_kind, scope_ref, events,
       source_config_id, tool_call_id, active, created_at, updated_at
FROM agent_listeners WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND active
ORDER BY id;

-- name: DeactivateAgentListeners :exec
UPDATE agent_listeners SET active = false, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND active;

-- name: CountActiveAgentListeners :one
SELECT count(*)::bigint FROM agent_listeners
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND active;

-- Exact provider conversation and its permitted parent scopes are supplied as a
-- bounded list by the provider's event decoder. These are addresses, not a
-- customer-authored filtering language.
-- name: ListMatchingAgentListeners :many
SELECT listener.id, listener.project_id, listener.agent_id, listener.connection_id,
       listener.resource_key, listener.scope_kind, listener.scope_ref, listener.events,
       listener.source_config_id, listener.tool_call_id, listener.active, listener.created_at, listener.updated_at
FROM jsonb_to_recordset(sqlc.arg(scopes)::jsonb) AS scope(kind text, ref text)
JOIN agent_listeners listener ON listener.scope_kind = scope.kind AND listener.scope_ref = scope.ref
JOIN integration_connections connection ON connection.project_id = listener.project_id AND connection.id = listener.connection_id
JOIN agents agent ON agent.project_id = listener.project_id AND agent.id = listener.agent_id
WHERE listener.project_id = sqlc.arg(project_id) AND listener.connection_id = sqlc.arg(connection_id)
  AND listener.active AND sqlc.arg(event)::text = ANY(listener.events)
  AND connection.state = 'active' AND connection.deleted_at IS NULL AND agent.state = 'active'
  AND listener.source_config_id = agent.current_config_id
ORDER BY listener.agent_id, listener.id;

-- name: DeactivateProjectListeners :exec
UPDATE agent_listeners SET active = false, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND active;

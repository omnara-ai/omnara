-- name: UpsertAgentListener :one
INSERT INTO agent_listeners(project_id, agent_id, app_id, listener_key, scope_kind, scope_ref, events, source_config_id, origin, tool_call_id)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(app_id), sqlc.arg(listener_key), sqlc.arg(scope_kind),
        sqlc.arg(scope_ref), sqlc.arg(events)::text[], sqlc.arg(source_config_id), sqlc.arg(origin), sqlc.narg(tool_call_id))
ON CONFLICT (project_id, agent_id, app_id, listener_key, scope_kind, scope_ref, origin)
DO UPDATE SET events = EXCLUDED.events,
              source_config_id = EXCLUDED.source_config_id, tool_call_id = EXCLUDED.tool_call_id, active = true, updated_at = statement_timestamp()
RETURNING id, project_id, agent_id, app_id, listener_key, scope_kind, scope_ref, events,
          source_config_id, origin, tool_call_id, active, created_at, updated_at;

-- name: ListActiveAgentListeners :many
SELECT id, project_id, agent_id, app_id, listener_key, scope_kind, scope_ref, events,
       source_config_id, origin, tool_call_id, active, created_at, updated_at
FROM agent_listeners WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND active
ORDER BY id;

-- name: DeactivateAgentListeners :exec
UPDATE agent_listeners SET active = false, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND active;

-- Recheck one frozen receive address at admission. Origin and tool-call ID are
-- provenance; either a configured or runtime subscription can authorize it.
-- name: HasActiveAgentListener :one
SELECT EXISTS (
    SELECT 1
    FROM agent_listeners listener
    JOIN agents agent ON agent.project_id = listener.project_id AND agent.id = listener.agent_id
    JOIN project_apps app ON app.project_id = listener.project_id AND app.id = listener.app_id
    WHERE listener.project_id = sqlc.arg(project_id) AND listener.agent_id = sqlc.arg(agent_id)
      AND listener.app_id = sqlc.arg(app_id) AND listener.listener_key = sqlc.arg(listener_key)
      AND listener.scope_kind = sqlc.arg(scope_kind) AND listener.scope_ref = sqlc.arg(scope_ref)
      AND listener.active AND sqlc.arg(event)::text = ANY(listener.events)
      AND listener.source_config_id = agent.current_config_id AND agent.state = 'active'
      AND app.state = 'active' AND app.deleted_at IS NULL
);

-- name: CountActiveAgentListeners :one
SELECT count(*)::bigint FROM agent_listeners
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND active;

-- Exact provider conversation and its permitted parent scopes are supplied as a
-- bounded list by the provider's event decoder. These are addresses, not a
-- customer-authored filtering language.
-- name: ListMatchingAgentListeners :many
SELECT listener.id, listener.project_id, listener.agent_id, listener.app_id,
       listener.listener_key, listener.scope_kind, listener.scope_ref, listener.events,
       listener.source_config_id, listener.origin, listener.tool_call_id, listener.active, listener.created_at, listener.updated_at
FROM jsonb_to_recordset(sqlc.arg(scopes)::jsonb) AS scope(kind text, ref text)
JOIN agent_listeners listener ON listener.scope_kind = scope.kind AND listener.scope_ref = scope.ref
JOIN project_apps app ON app.project_id = listener.project_id AND app.id = listener.app_id
JOIN agents agent ON agent.project_id = listener.project_id AND agent.id = listener.agent_id
WHERE listener.project_id = sqlc.arg(project_id) AND listener.app_id = sqlc.arg(app_id)
  AND listener.active AND sqlc.arg(event)::text = ANY(listener.events)
  AND app.state = 'active' AND app.deleted_at IS NULL AND agent.state = 'active'
  AND listener.source_config_id = agent.current_config_id
ORDER BY listener.agent_id, listener.id;

-- name: DeactivateProjectListeners :exec
UPDATE agent_listeners SET active = false, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND active;

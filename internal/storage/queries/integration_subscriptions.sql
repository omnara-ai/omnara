-- name: InsertIntegrationSubscription :one
INSERT INTO integration_subscriptions(project_id, agent_id, integration_id, scope_kind, scope_ref)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(integration_id), sqlc.arg(scope_kind), sqlc.arg(scope_ref))
RETURNING id, project_id, agent_id, integration_id, scope_kind, scope_ref, created_at;

-- name: GetIntegrationSubscriptionForConversation :one
SELECT id, project_id, agent_id, integration_id, scope_kind, scope_ref, created_at
FROM integration_subscriptions
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND integration_id = sqlc.arg(integration_id)
  AND scope_kind = sqlc.arg(scope_kind) AND scope_ref = sqlc.arg(scope_ref);

-- name: GetIntegrationSubscription :one
SELECT id, project_id, agent_id, integration_id, scope_kind, scope_ref, created_at
FROM integration_subscriptions
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND id = sqlc.arg(id);

-- name: ListIntegrationSubscriptions :many
SELECT subscription.id, subscription.project_id, subscription.agent_id, subscription.integration_id,
       subscription.scope_kind, subscription.scope_ref,
       subscription.created_at, agent.name AS agent_name
FROM integration_subscriptions subscription
JOIN agents agent ON agent.project_id = subscription.project_id AND agent.id = subscription.agent_id
WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.integration_id = sqlc.arg(integration_id)
  AND (NOT sqlc.arg(cursor_set)::boolean OR (subscription.created_at, subscription.id) <
       (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY subscription.created_at DESC, subscription.id DESC
LIMIT sqlc.arg(row_limit);

-- name: HasIntegrationSubscription :one
SELECT EXISTS (
    SELECT 1
    FROM integration_subscriptions subscription
    JOIN agents agent ON agent.project_id = subscription.project_id AND agent.id = subscription.agent_id
    JOIN project_integrations integration ON integration.project_id = subscription.project_id AND integration.id = subscription.integration_id
    WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.agent_id = sqlc.arg(agent_id)
      AND subscription.integration_id = sqlc.arg(integration_id)
      AND subscription.scope_kind = sqlc.arg(scope_kind) AND subscription.scope_ref = sqlc.arg(scope_ref)
      AND agent.state = 'active'
      AND integration.state = 'active' AND integration.deleted_at IS NULL
);

-- name: CountAgentIntegrationSubscriptions :one
SELECT count(*)::bigint FROM integration_subscriptions
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id);

-- name: ListMatchingIntegrationSubscriptions :many
SELECT subscription.id, subscription.project_id, subscription.agent_id, subscription.integration_id,
       subscription.scope_kind, subscription.scope_ref,
       subscription.created_at
FROM jsonb_to_recordset(sqlc.arg(scopes)::jsonb) AS scope(kind text, ref text)
JOIN integration_subscriptions subscription ON subscription.scope_kind = scope.kind AND subscription.scope_ref = scope.ref
JOIN project_integrations integration ON integration.project_id = subscription.project_id AND integration.id = subscription.integration_id
JOIN agents agent ON agent.project_id = subscription.project_id AND agent.id = subscription.agent_id
WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.integration_id = sqlc.arg(integration_id)
  AND integration.state = 'active' AND integration.deleted_at IS NULL AND agent.state = 'active'
ORDER BY subscription.agent_id, subscription.id;

-- name: DeleteIntegrationSubscription :exec
DELETE FROM integration_subscriptions
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id) AND id = sqlc.arg(id);

-- name: DeleteAgentIntegrationSubscriptions :exec
DELETE FROM integration_subscriptions WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id);

-- name: DeleteProjectIntegrationSubscriptions :exec
DELETE FROM integration_subscriptions WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id);

-- name: DeleteProjectSubscriptions :exec
DELETE FROM integration_subscriptions WHERE project_id = sqlc.arg(project_id);

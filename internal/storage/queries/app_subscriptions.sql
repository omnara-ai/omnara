-- name: InsertAppSubscription :one
INSERT INTO app_subscriptions(project_id, agent_id, app_id, subscription_type, scope_kind, scope_ref, events)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(app_id), sqlc.arg(subscription_type),
        sqlc.arg(scope_kind), sqlc.arg(scope_ref), sqlc.arg(events)::text[])
RETURNING id, project_id, agent_id, app_id, subscription_type, scope_kind, scope_ref, events, created_at;

-- name: GetAppSubscriptionForConversation :one
SELECT id, project_id, agent_id, app_id, subscription_type, scope_kind, scope_ref, events, created_at
FROM app_subscriptions
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id) AND app_id = sqlc.arg(app_id)
  AND subscription_type = sqlc.arg(subscription_type) AND scope_kind = sqlc.arg(scope_kind) AND scope_ref = sqlc.arg(scope_ref);

-- name: GetAppSubscription :one
SELECT id, project_id, agent_id, app_id, subscription_type, scope_kind, scope_ref, events, created_at
FROM app_subscriptions
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND id = sqlc.arg(id);

-- name: ListAppSubscriptions :many
SELECT subscription.id, subscription.project_id, subscription.agent_id, subscription.app_id,
       subscription.subscription_type, subscription.scope_kind, subscription.scope_ref, subscription.events,
       subscription.created_at, agent.name AS agent_name
FROM app_subscriptions subscription
JOIN agents agent ON agent.project_id = subscription.project_id AND agent.id = subscription.agent_id
WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.app_id = sqlc.arg(app_id)
  AND (NOT sqlc.arg(cursor_set)::boolean OR (subscription.created_at, subscription.id) <
       (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY subscription.created_at DESC, subscription.id DESC
LIMIT sqlc.arg(row_limit);

-- Recheck receive authority after taking the agent lifecycle gate. Subscription
-- IDs fence deletion retries; a new matching subscription can authorize an input.
-- name: HasAppSubscription :one
SELECT EXISTS (
    SELECT 1
    FROM app_subscriptions subscription
    JOIN agents agent ON agent.project_id = subscription.project_id AND agent.id = subscription.agent_id
    JOIN project_apps app ON app.project_id = subscription.project_id AND app.id = subscription.app_id
    WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.agent_id = sqlc.arg(agent_id)
      AND subscription.app_id = sqlc.arg(app_id) AND subscription.subscription_type = sqlc.arg(subscription_type)
      AND subscription.scope_kind = sqlc.arg(scope_kind) AND subscription.scope_ref = sqlc.arg(scope_ref)
      AND sqlc.arg(event)::text = ANY(subscription.events) AND agent.state = 'active'
      AND app.state = 'active' AND app.deleted_at IS NULL
);

-- name: CountAgentAppSubscriptions :one
SELECT count(*)::bigint FROM app_subscriptions
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id);

-- Provider decoding supplies exact addresses and a bounded set of parent scopes.
-- name: ListMatchingAppSubscriptions :many
SELECT subscription.id, subscription.project_id, subscription.agent_id, subscription.app_id,
       subscription.subscription_type, subscription.scope_kind, subscription.scope_ref, subscription.events,
       subscription.created_at
FROM jsonb_to_recordset(sqlc.arg(scopes)::jsonb) AS scope(kind text, ref text)
JOIN app_subscriptions subscription ON subscription.scope_kind = scope.kind AND subscription.scope_ref = scope.ref
JOIN project_apps app ON app.project_id = subscription.project_id AND app.id = subscription.app_id
JOIN agents agent ON agent.project_id = subscription.project_id AND agent.id = subscription.agent_id
WHERE subscription.project_id = sqlc.arg(project_id) AND subscription.app_id = sqlc.arg(app_id)
  AND sqlc.arg(event)::text = ANY(subscription.events)
  AND app.state = 'active' AND app.deleted_at IS NULL AND agent.state = 'active'
ORDER BY subscription.agent_id, subscription.id;

-- name: DeleteAppSubscription :exec
DELETE FROM app_subscriptions
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id) AND id = sqlc.arg(id);

-- name: DeleteAgentAppSubscriptions :exec
DELETE FROM app_subscriptions WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id);

-- name: DeleteProjectAppSubscriptions :exec
DELETE FROM app_subscriptions WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id);

-- name: DeleteProjectSubscriptions :exec
DELETE FROM app_subscriptions WHERE project_id = sqlc.arg(project_id);

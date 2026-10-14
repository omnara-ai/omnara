-- name: LockIntegrationConversation :exec
SELECT pg_advisory_xact_lock(hashtextextended(
    'integration_conversation:' || jsonb_build_array(sqlc.arg(project_id)::uuid, sqlc.arg(integration_id)::uuid,
        sqlc.arg(kind)::text, sqlc.arg(ref)::text)::text, 0));

-- Keep retired launch owners so a later comment cannot launch a replacement agent.
-- name: ListConversationLaunchOwners :many
SELECT id, project_id, agent_id, integration_id, scope_ref,
       scope_kind, display_name, launch_key,
       deleted_at, created_at, updated_at
FROM integration_targets target
WHERE target.project_id = sqlc.arg(project_id) AND target.integration_id = sqlc.arg(integration_id)
  AND target.scope_kind = sqlc.arg(kind) AND target.scope_ref = sqlc.arg(ref)
  AND target.launch_key IS NOT NULL
ORDER BY target.id;

-- name: GetIntegrationLaunchOwner :one
SELECT id, project_id, agent_id, integration_id, scope_ref,
       scope_kind, display_name, launch_key,
       deleted_at, created_at, updated_at
FROM integration_targets
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND scope_kind = sqlc.arg(kind) AND scope_ref = sqlc.arg(ref)
  AND launch_key = sqlc.arg(launch_key);

-- name: GetAgentConversationTarget :one
SELECT id, project_id, agent_id, integration_id, scope_ref,
       scope_kind, display_name, launch_key,
       deleted_at, created_at, updated_at
FROM integration_targets
WHERE project_id = sqlc.arg(project_id) AND agent_id = sqlc.arg(agent_id)
  AND integration_id = sqlc.arg(integration_id)
  AND scope_kind = sqlc.arg(kind) AND scope_ref = sqlc.arg(ref)
  AND deleted_at IS NULL;

-- name: GetConversationDisplayName :one
SELECT display_name
FROM integration_targets
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND scope_kind = sqlc.arg(kind) AND scope_ref = sqlc.arg(ref)
  AND deleted_at IS NULL AND display_name <> ''
ORDER BY updated_at DESC, id DESC
LIMIT 1;

-- name: GetAgentIntegrationLaunchOwners :many
SELECT target.id, project.org_id, target.project_id, target.agent_id, target.integration_id, target.scope_ref,
       target.scope_kind, target.display_name, target.launch_key, target.deleted_at, target.created_at, target.updated_at
FROM integration_targets target
JOIN agents agent ON agent.project_id = target.project_id AND agent.id = target.agent_id AND agent.state = 'active'
JOIN projects project ON project.id = target.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
JOIN integrations integration ON integration.project_id = target.project_id AND integration.id = target.integration_id
    AND integration.deleted_at IS NULL AND integration.state = 'active'
WHERE target.project_id = sqlc.arg(project_id) AND target.agent_id = sqlc.arg(agent_id)
  AND target.deleted_at IS NULL AND target.launch_key IS NOT NULL
ORDER BY target.id
LIMIT 2;

-- name: InsertIntegrationConversationTarget :one
INSERT INTO integration_targets(project_id, agent_id, integration_id,
    scope_kind, scope_ref, display_name, launch_key, created_at, updated_at)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(integration_id),
    sqlc.arg(kind), sqlc.arg(ref), sqlc.arg(display_name), sqlc.narg(launch_key),
    transaction_timestamp(), transaction_timestamp())
ON CONFLICT DO NOTHING
RETURNING id, project_id, agent_id, integration_id, scope_ref,
          scope_kind, display_name, launch_key,
          deleted_at, created_at, updated_at;

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

-- name: ListTurnInputLaunchOwners :many
WITH RECURSIVE work_turns(turn_id) AS (
    SELECT sqlc.arg(turn_id)::uuid
    UNION
    SELECT origin.turn_id
    FROM work_turns
    JOIN model_call_contexts context ON context.project_id = sqlc.arg(project_id)
        AND context.agent_id = sqlc.arg(agent_id) AND context.turn_id = work_turns.turn_id
    JOIN agent_inputs input ON input.project_id = context.project_id AND input.agent_id = context.agent_id
        AND input.id = ANY(context.opening_input_ids)
    JOIN processes process ON process.project_id = input.project_id AND process.agent_id = input.agent_id
    JOIN tool_call_read_projection origin ON origin.agent_id = process.agent_id AND origin.id = process.tool_call_id
    WHERE (input.idempotency_scope = 'process_notification' AND input.input_idempotency_key = 'exit:' || process.id::text)
        OR EXISTS (
            SELECT 1
            FROM process_watch_reports report
            JOIN process_actions action ON action.id = report.watch_action_id
            WHERE report.agent_input_id = input.id AND action.process_id = process.id
        )
)
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
  AND EXISTS (
    SELECT 1
    FROM work_turns
    JOIN model_call_contexts context ON context.agent_id = target.agent_id AND context.project_id = target.project_id
        AND context.turn_id = work_turns.turn_id
    JOIN agent_inputs input ON input.project_id = context.project_id AND input.agent_id = context.agent_id
        AND input.id = ANY(context.opening_input_ids)
    WHERE input.integration_target_id = target.id
  )
ORDER BY target.id;

-- name: PromoteIntegrationConversationTarget :exec
UPDATE integration_targets SET launch_key = sqlc.arg(launch_key)::text, updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND launch_key IS NULL AND deleted_at IS NULL;

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

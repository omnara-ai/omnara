-- name: ListChildAgents :many
SELECT agent.id,
       agent.name,
       agent.state,
       agent.subagent_handle,
       agent.created_at,
       agent.archived_at,
       coalesce((
         SELECT max(event.created_at)
         FROM agent_events event
         WHERE event.agent_id = agent.id
       ), agent.created_at) AS last_activity_at,
       EXISTS (
         SELECT 1
         FROM agent_interactions interaction
         WHERE interaction.agent_id = agent.id
           AND interaction.interaction_kind = 'question'
           AND interaction.state = 'open'
       ) AS has_open_question,
       EXISTS (
         SELECT 1
         FROM agent_interactions interaction
         WHERE interaction.agent_id = agent.id
           AND interaction.interaction_kind = 'permission'
           AND interaction.state = 'open'
       ) AS has_open_permission,
       (
         EXISTS (
           SELECT 1
           FROM agent_runtime_locks runtime_lock
           WHERE runtime_lock.agent_id = agent.id
         )
         OR EXISTS (
           SELECT 1
           FROM agent_wakeups wake
           WHERE wake.agent_id = agent.id
         )
         OR agent_next_wakeup_ready_at(agent.project_id, agent.id) IS NOT NULL
       )::boolean AS is_running,
       EXISTS (
         SELECT 1
         FROM model_outputs output
         WHERE output.agent_id = agent.id
       ) AS has_model_output
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.parent_agent_id = sqlc.arg(parent_agent_id)
  AND (sqlc.arg(include_archived)::boolean OR agent.state = 'active')
  AND (sqlc.narg(agent_id)::uuid IS NULL OR agent.id = sqlc.narg(agent_id)::uuid)
  AND (sqlc.arg(name)::text = '' OR agent.name = sqlc.arg(name)::text)
ORDER BY agent.created_at, agent.id;

-- name: CountActiveChildAgents :one
SELECT count(*)::integer
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.parent_agent_id = sqlc.arg(parent_agent_id)
  AND agent.state = 'active'
  AND (sqlc.arg(subagent_handle)::text = '' OR agent.subagent_handle = sqlc.arg(subagent_handle)::text);

-- name: ActiveChildAgentNameExists :one
SELECT EXISTS (
  SELECT 1
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.parent_agent_id = sqlc.arg(parent_agent_id)
    AND agent.state = 'active'
    AND agent.name = sqlc.arg(name)::text
);

-- name: ListActiveChildAgentIDs :many
SELECT agent.id
FROM agents agent
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.parent_agent_id = sqlc.arg(parent_agent_id)
  AND agent.state = 'active'
ORDER BY agent.created_at, agent.id;

-- name: CountAgentAncestors :one
WITH RECURSIVE ancestors AS (
  SELECT agent.id, agent.parent_agent_id, 1 AS depth
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  UNION ALL
  SELECT parent.id, parent.parent_agent_id, ancestors.depth + 1
  FROM agents parent
  JOIN ancestors ON parent.id = ancestors.parent_agent_id
  WHERE parent.project_id = sqlc.arg(project_id)
    AND ancestors.depth < 64
)
SELECT count(*)::integer
FROM ancestors
WHERE ancestors.parent_agent_id IS NOT NULL;

-- name: ListAgentDescendantIDs :many
WITH RECURSIVE descendants AS (
  SELECT agent.id, 1 AS depth
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.parent_agent_id = sqlc.arg(agent_id)
  UNION ALL
  SELECT child.id, descendants.depth + 1
  FROM agents child
  JOIN descendants ON child.parent_agent_id = descendants.id
  WHERE child.project_id = sqlc.arg(project_id)
    AND descendants.depth < 64
)
SELECT descendants.id
FROM descendants
ORDER BY descendants.depth, descendants.id;

-- name: LatestModelOutputTextForAgent :one
WITH latest AS (
  SELECT output.id
  FROM model_outputs output
  JOIN agents agent ON agent.id = output.agent_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND output.agent_id = sqlc.arg(agent_id)
    AND output.stop_reason <> 'tool_use'
  ORDER BY output.created_at DESC, output.id DESC
  LIMIT 1
)
SELECT coalesce(string_agg(block.text_content, E'\n' ORDER BY block.ordinal), '')::text AS result_text,
       count(latest.id)::integer AS output_count
FROM latest
LEFT JOIN content_blocks block ON block.owner_model_output_id = latest.id
  AND block.owner_kind = 'model_output'
  AND block.block_kind = 'text';

-- name: ListParentMachineBindingsForSharing :many
SELECT binding.machine_id,
       pmgrant.id AS project_machine_grant_id,
       binding.cwd,
       binding.env_overlay,
       binding.secret_env_overlay,
       binding.description
FROM agent_machine_bindings binding
JOIN project_machine_grants pmgrant ON pmgrant.project_id = binding.project_id
  AND pmgrant.machine_id = binding.machine_id
JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.state = 'attached'
ORDER BY binding.created_at, binding.id;

-- name: GetOpenQuestionInteractionForAgent :one
SELECT interaction.id, interaction.tool_call_id, interaction.request
FROM agent_interactions interaction
JOIN agents agent ON agent.id = interaction.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND interaction.agent_id = sqlc.arg(agent_id)
  AND interaction.interaction_kind = 'question'
  AND interaction.state = 'open'
ORDER BY interaction.created_at DESC, interaction.id DESC
LIMIT 1;

-- name: GetOpenPermissionInteractionForAgent :one
SELECT interaction.id, interaction.tool_call_id, interaction.request
FROM agent_interactions interaction
JOIN agents agent ON agent.id = interaction.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND interaction.agent_id = sqlc.arg(agent_id)
  AND interaction.interaction_kind = 'permission'
  AND interaction.state = 'open'
ORDER BY interaction.created_at DESC, interaction.id DESC
LIMIT 1;

-- name: ListAgentInteractionsForAgents :many
SELECT interaction.id, interaction.project_id, interaction.agent_id, interaction.turn_id,
       interaction.model_call_context_id, interaction.tool_call_id, interaction.provider_call_id,
       interaction.interaction_kind, interaction.state, interaction.request, interaction.resolution,
       interaction.resolved_by_input_id, interaction.created_at, interaction.resolved_at,
       agent.name AS agent_name, agent.subagent_handle
FROM agent_interaction_read_projection interaction
JOIN agents agent ON agent.project_id = interaction.project_id
  AND agent.id = interaction.agent_id
WHERE interaction.project_id = sqlc.arg(project_id)
  AND interaction.agent_id = ANY(sqlc.arg(agent_ids)::uuid[])
  AND (sqlc.arg(state)::text = '' OR interaction.state = sqlc.arg(state))
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (interaction.created_at, interaction.id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY interaction.created_at ASC, interaction.id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: InsertAgentWaitTarget :exec
INSERT INTO agent_wait_targets(project_id, agent_id, tool_call_id, target_agent_id, state)
VALUES (sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(tool_call_id), sqlc.arg(target_agent_id), 'pending');

-- name: CountAgentWaitTargets :one
SELECT count(*)::integer
FROM agent_wait_targets
WHERE agent_id = sqlc.arg(agent_id)
  AND tool_call_id = sqlc.arg(tool_call_id);

-- name: ListOpenAgentWaitsForTarget :many
SELECT target.project_id, target.agent_id, target.tool_call_id,
       coalesce(call.input->>'mode', 'all')::text AS mode
FROM agent_wait_targets target
JOIN tool_calls call ON call.agent_id = target.agent_id
  AND call.id = target.tool_call_id
JOIN agents waiting_agent ON waiting_agent.project_id = target.project_id
  AND waiting_agent.id = target.agent_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.target_agent_id = sqlc.arg(target_agent_id)
  AND target.state = 'pending'
  AND call.state = 'waiting'
  AND waiting_agent.state <> 'archived'
ORDER BY call.created_at, call.id;

-- name: MarkAgentWaitTargetDone :execrows
UPDATE agent_wait_targets
SET state = 'done',
    result_kind = sqlc.arg(result_kind),
    result_text = sqlc.arg(result_text),
    completed_at = statement_timestamp()
WHERE agent_id = sqlc.arg(agent_id)
  AND tool_call_id = sqlc.arg(tool_call_id)
  AND target_agent_id = sqlc.arg(target_agent_id)
  AND state = 'pending';

-- name: CountPendingAgentWaitTargets :one
SELECT count(*)::integer
FROM agent_wait_targets
WHERE agent_id = sqlc.arg(agent_id)
  AND tool_call_id = sqlc.arg(tool_call_id)
  AND state = 'pending';

-- name: ListAgentWaitTargets :many
SELECT target.target_agent_id,
       target.state,
       target.result_kind,
       target.result_text,
       agent.name,
       agent.subagent_handle,
       agent.state AS agent_state
FROM agent_wait_targets target
JOIN agents agent ON agent.project_id = target.project_id
  AND agent.id = target.target_agent_id
WHERE target.agent_id = sqlc.arg(agent_id)
  AND target.tool_call_id = sqlc.arg(tool_call_id)
ORDER BY agent.created_at, agent.id;

-- name: ListIdleSubagentsForArchive :many
SELECT agent.project_id, agent.id
FROM agents agent
WHERE agent.parent_agent_id IS NOT NULL
  AND agent.state = 'active'
  AND agent.archive_after_idle_minutes IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM agent_runtime_locks runtime_lock
    WHERE runtime_lock.agent_id = agent.id
  )
  AND NOT EXISTS (
    SELECT 1
    FROM agent_wakeups wake
    WHERE wake.agent_id = agent.id
  )
  AND coalesce((
    SELECT max(event.created_at)
    FROM agent_events event
    WHERE event.agent_id = agent.id
  ), agent.created_at) < statement_timestamp() - make_interval(mins => agent.archive_after_idle_minutes)
ORDER BY agent.created_at, agent.id
LIMIT sqlc.arg(row_limit)::integer;

-- name: CompleteToolCallFromAgentWait :one
WITH locked_agent AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
  FOR UPDATE
)
UPDATE tool_calls call
SET state = 'completed',
    runtime_lock_id = NULL
FROM locked_agent agent
CROSS JOIN tool_call_read_projection projection
WHERE call.agent_id = agent.id
  AND call.id = sqlc.arg(tool_call_id)
  AND call.state = 'waiting'
  AND call.type = 'built_in'
  AND EXISTS (
    SELECT 1
    FROM agent_wait_targets target
    WHERE target.agent_id = call.agent_id
      AND target.tool_call_id = call.id
  )
  AND projection.project_id = agent.project_id
  AND projection.agent_id = call.agent_id
  AND projection.id = call.id
RETURNING call.id, projection.project_id, call.agent_id,
  projection.turn_id, projection.source_event_id, projection.model_call_context_id,
  call.provider_call_id,
  call.name, call.input,
  call.type, call.state,
  sqlc.arg(outcome)::text AS outcome, call.runtime_lock_id,
  '[]'::jsonb AS result_content_parts,
  call.created_at;

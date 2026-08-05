-- name: InsertAgentInteraction :one
INSERT INTO agent_interactions(
  agent_id, tool_call_id,
  interaction_kind, state, request, created_at
)
SELECT tool_call.agent_id,
	     tool_call.id, sqlc.arg(interaction_kind), 'open',
	     sqlc.arg(request), statement_timestamp()
FROM tool_call_read_projection tool_call
WHERE tool_call.project_id = sqlc.arg(project_id)
	AND tool_call.agent_id = sqlc.arg(agent_id)
	AND tool_call.id = sqlc.arg(tool_call_id)
RETURNING id;

-- name: GetAgentInteraction :one
SELECT id, project_id, agent_id, turn_id,
       model_call_context_id, tool_call_id, provider_call_id,
       interaction_kind, state, request, resolution,
       resolved_by_input_id, created_at, resolved_at
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

-- name: ListAgentInteractionsByIDs :many
SELECT id, project_id, agent_id, turn_id,
	   model_call_context_id, tool_call_id, provider_call_id,
	   interaction_kind, state, request, resolution,
	   resolved_by_input_id, created_at, resolved_at
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id)
	AND agent_id = sqlc.arg(agent_id)
	AND id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY created_at, id;

-- name: AgentInteractionHasLaterStop :one
SELECT EXISTS (
  SELECT 1
FROM agent_interactions interaction
  JOIN tool_call_read_projection tool_call ON tool_call.agent_id = interaction.agent_id
    AND tool_call.id = interaction.tool_call_id
  JOIN agent_stop_events stop_event ON stop_event.project_id = tool_call.project_id
    AND stop_event.agent_id = tool_call.agent_id
    AND stop_event.sequence > tool_call.source_event_sequence
  WHERE tool_call.project_id = sqlc.arg(project_id)
    AND interaction.agent_id = sqlc.arg(agent_id)
    AND interaction.id = sqlc.arg(id)
) AS stopped;

-- name: ListAgentInteractionsForAgent :many
SELECT id, project_id, agent_id, turn_id,
       model_call_context_id, tool_call_id, provider_call_id,
       interaction_kind, state, request, resolution,
       resolved_by_input_id, created_at, resolved_at
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state))
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: GetAgentInteractionByToolCallKind :one
SELECT id, project_id, agent_id, turn_id,
       model_call_context_id, tool_call_id, provider_call_id,
       interaction_kind, state, request, resolution,
       resolved_by_input_id, created_at, resolved_at
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND tool_call_id = sqlc.arg(tool_call_id)
  AND interaction_kind = sqlc.arg(interaction_kind);

-- name: ResolveAgentInteraction :one
UPDATE agent_interactions interaction
SET state = 'resolved',
	  resolution = sqlc.arg(resolution),
	  resolved_by_input_id = sqlc.narg(resolved_by_input_id)::uuid,
	  resolved_at = statement_timestamp()
WHERE interaction.agent_id = sqlc.arg(agent_id)
	AND interaction.id = sqlc.arg(id)
	AND interaction.state = 'open'
	AND EXISTS (
	  SELECT 1
	  FROM agents agent
	  WHERE agent.id = interaction.agent_id
	    AND agent.project_id = sqlc.arg(project_id)
	)
RETURNING interaction.id;

-- name: InsertInteractionResponseAgentInput :one
WITH target AS (
  SELECT projection.project_id, interaction.agent_id, interaction.id
  FROM agent_interactions interaction
  JOIN agent_interaction_read_projection projection
    ON projection.agent_id = interaction.agent_id
   AND projection.id = interaction.id
  WHERE projection.project_id = sqlc.arg(project_id)
    AND interaction.agent_id = sqlc.arg(agent_id)
    AND interaction.id = sqlc.arg(target_interaction_id)
)
INSERT INTO agent_inputs(
  project_id, agent_id, state,
  actor_id, input_kind, delivery_mode, target_interaction_id,
  idempotency_scope, input_idempotency_key, queued_at, metadata
)
SELECT target.project_id, target.agent_id, 'received',
       sqlc.narg(actor_id)::uuid,
       'interaction_response', 'immediate', target.id,
       sqlc.arg(idempotency_scope), sqlc.arg(input_idempotency_key),
       statement_timestamp(), sqlc.arg(metadata)
FROM target
ON CONFLICT (project_id, agent_id, idempotency_scope, input_idempotency_key) WHERE idempotency_scope IS NOT NULL AND input_idempotency_key IS NOT NULL DO NOTHING
RETURNING id, project_id, agent_id, state, input_rank, actor_id, input_kind, coalesce(idempotency_scope, '') AS idempotency_scope, coalesce(input_idempotency_key, '') AS input_idempotency_key, queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode, coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at, coalesce(rejected_reason, '') AS rejected_reason, metadata;

-- name: ResolveInteractionResponseAgentInput :one
UPDATE agent_inputs
SET state = 'resolved',
    admitted_event_id = sqlc.arg(event_id),
    admitted_at = statement_timestamp(),
    resolved_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id)
  AND input_kind = 'interaction_response'
  AND target_interaction_id = sqlc.arg(target_interaction_id)
  AND state = 'received'
RETURNING id, project_id, agent_id, state, input_rank, actor_id, input_kind, coalesce(idempotency_scope, '') AS idempotency_scope, coalesce(input_idempotency_key, '') AS input_idempotency_key, queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode, coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at, coalesce(rejected_reason, '') AS rejected_reason, metadata;

-- name: CancelOpenAgentInteractionsForAgent :many
UPDATE agent_interactions interaction
SET state = 'canceled',
	  resolution = jsonb_build_object('reason', sqlc.arg(reason)::text),
	  resolved_by_input_id = sqlc.narg(resolved_by_input_id)::uuid,
	  resolved_at = statement_timestamp()
FROM agent_interaction_read_projection projection
WHERE interaction.agent_id = sqlc.arg(agent_id)
	AND interaction.state = 'open'
	AND projection.project_id = sqlc.arg(project_id)
	AND projection.agent_id = interaction.agent_id
	AND projection.id = interaction.id
	AND projection.turn_id = sqlc.arg(turn_id)
RETURNING interaction.id;

-- name: CancelOpenAgentInteractionsForToolCall :execrows
UPDATE agent_interactions
SET state = 'canceled',
    resolution = jsonb_build_object('reason', sqlc.arg(reason)::text),
    resolved_by_input_id = NULL,
    resolved_at = statement_timestamp()
WHERE agent_interactions.agent_id = sqlc.arg(agent_id)
  AND agent_interactions.tool_call_id = sqlc.arg(tool_call_id)
  AND agent_interactions.state = 'open'
  AND EXISTS (
    SELECT 1
    FROM agents agent
    WHERE agent.id = agent_interactions.agent_id
      AND agent.project_id = sqlc.arg(project_id)
  );

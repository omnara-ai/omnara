-- name: InsertAgentInput :one
WITH generated AS (
    SELECT coalesce(sqlc.narg(id), uuidv7()) AS id
)
INSERT INTO agent_inputs(id, project_id, agent_id, state, input_rank, actor_id, input_kind, integration_target_id, integration_target_binding_id, delivery_mode, idempotency_scope, input_idempotency_key, queued_at, metadata)
SELECT generated.id, agent.project_id, agent.id, 'received',
       coalesce(
         (
           SELECT max(existing.input_rank) + sqlc.arg(rank_stride)::bigint
           FROM agent_inputs existing
           WHERE existing.project_id = agent.project_id
             AND existing.agent_id = agent.id
             AND existing.delivery_mode = sqlc.arg(delivery_mode)::text
             AND existing.state = 'received'
             AND existing.input_kind = 'content'
         ),
         sqlc.arg(rank_stride)::bigint
       ),
       sqlc.arg(actor_id), 'content',
       sqlc.narg(integration_target_id)::uuid,
       sqlc.narg(integration_target_binding_id)::uuid,
       sqlc.arg(delivery_mode)::text,
       sqlc.narg(idempotency_scope), sqlc.narg(input_idempotency_key), statement_timestamp(), sqlc.arg(metadata)
FROM agents agent
JOIN generated ON true
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
RETURNING id, project_id, agent_id, state, input_rank, actor_id, input_kind, integration_target_id, integration_target_binding_id, coalesce(idempotency_scope, '') AS idempotency_scope, coalesce(input_idempotency_key, '') AS input_idempotency_key, queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode, coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at, coalesce(rejected_reason, '') AS rejected_reason, metadata;

-- name: GetAgentInputByIdempotency :one
SELECT id, project_id, agent_id, state, input_rank, actor_id, input_kind, integration_target_id, integration_target_binding_id, coalesce(idempotency_scope, '') AS idempotency_scope, coalesce(input_idempotency_key, '') AS input_idempotency_key, queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode, coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at, coalesce(rejected_reason, '') AS rejected_reason, metadata
FROM agent_inputs
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND idempotency_scope = sqlc.arg(idempotency_scope)::text
  AND input_idempotency_key = sqlc.arg(input_idempotency_key)::text;

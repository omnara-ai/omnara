-- name: GetAgentInputByIdempotency :one
SELECT id, project_id, agent_id, state, input_rank, actor_id, input_kind, integration_target_id, coalesce(idempotency_scope, '') AS idempotency_scope, coalesce(input_idempotency_key, '') AS input_idempotency_key, queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode, coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at, coalesce(rejected_reason, '') AS rejected_reason, metadata
FROM agent_inputs
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND idempotency_scope = sqlc.arg(idempotency_scope)::text
  AND input_idempotency_key = sqlc.arg(input_idempotency_key)::text;

-- name: GetAgentInput :one
SELECT id, project_id, agent_id, state, input_rank, actor_id, input_kind, integration_target_id, coalesce(idempotency_scope, '') AS idempotency_scope, coalesce(input_idempotency_key, '') AS input_idempotency_key, queued_at, admitted_event_id, admitted_at, canceled_at, delivery_mode, coalesce(control_type, '') AS control_type, target_interaction_id, agent_config_id, resolved_at, coalesce(rejected_reason, '') AS rejected_reason, metadata
FROM agent_inputs
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

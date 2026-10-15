-- name: GetAgentInteraction :one
SELECT id, project_id, agent_id, turn_id,
       model_call_context_id, tool_call_id, provider_call_id,
       interaction_kind, state, request, resolution,
       resolved_by_input_id, created_at, resolved_at, destination, presentation_receipt
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = sqlc.arg(id);

-- name: GetAgentInteractionByToolCallKind :one
SELECT id, project_id, agent_id, turn_id,
       model_call_context_id, tool_call_id, provider_call_id,
       interaction_kind, state, request, resolution,
       resolved_by_input_id, created_at, resolved_at, destination, presentation_receipt
FROM agent_interaction_read_projection
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND tool_call_id = sqlc.arg(tool_call_id)
  AND interaction_kind = sqlc.arg(interaction_kind);

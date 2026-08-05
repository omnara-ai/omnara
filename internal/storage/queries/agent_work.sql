-- name: NextAgentToolWork :one
SELECT frontier.turn_id::uuid AS turn_id,
       frontier.model_call_context_id::uuid AS model_call_context_id,
       frontier.model_output_id::uuid AS model_output_id,
       frontier.source_event_id::uuid AS source_event_id
FROM agent_tool_work_frontiers(
  sqlc.arg(project_id),
  sqlc.arg(agent_id)
) AS frontier(
  turn_id,
  model_call_context_id,
  model_output_id,
  source_event_id,
  ready_at,
  frontier_order_created_at,
  frontier_order_tool_call_id
)
ORDER BY frontier.ready_at,
         frontier.frontier_order_created_at,
         frontier.frontier_order_tool_call_id
LIMIT 1;

-- name: NextAgentModelWork :one
SELECT frontier.work_kind::text AS work_kind,
       coalesce(frontier.model_call_context_id, '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS model_call_context_id,
       coalesce(frontier.model_output_id, '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS model_output_id,
       frontier.turn_id::uuid AS turn_id,
       frontier.input_ids::uuid[] AS input_ids,
       frontier.opening_event_sequence::bigint AS opening_event_sequence,
       frontier.ready_at <= statement_timestamp() AS is_ready
FROM agent_next_model_work(
  sqlc.arg(project_id),
  sqlc.arg(agent_id)
) AS frontier(
  work_kind,
  model_call_context_id,
  model_output_id,
  turn_id,
  input_ids,
  opening_event_sequence,
  ready_at
)
LIMIT 1;

-- name: AgentHasIncompleteToolBatch :one
SELECT agent_has_incomplete_tool_batch(
  sqlc.arg(project_id),
  sqlc.arg(agent_id)
) AS incomplete;

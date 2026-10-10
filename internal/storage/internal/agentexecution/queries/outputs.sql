-- name: ReconstructExecutionOutputs :many
SELECT o.id,o.model_call_context_id,o.stop_reason,e.id AS event_id,e.sequence,e.created_at
FROM model_outputs o JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
WHERE o.agent_id=$1 AND e.turn_id=$2 AND e.sequence>=sqlc.arg(stop_sequence)
ORDER BY e.sequence;

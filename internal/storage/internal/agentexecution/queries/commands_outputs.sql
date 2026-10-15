-- name: CreateExecutionOutput :one
INSERT INTO model_outputs(agent_id,model_call_context_id,served_provider_model_slug,stop_reason,provider_replay,created_at)
VALUES(sqlc.arg(agent_id),sqlc.arg(context_id),sqlc.arg(served_model),sqlc.arg(stop_reason),sqlc.narg(replay)::jsonb,statement_timestamp())
RETURNING id,created_at;

-- name: FindExecutionOutput :one
SELECT o.id,o.served_provider_model_slug,o.stop_reason,o.provider_replay,e.id AS event_id,e.turn_id,e.sequence,e.created_at
FROM model_outputs o JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
WHERE o.agent_id=sqlc.arg(agent_id) AND o.model_call_context_id=sqlc.arg(context_id);

-- name: CreateExecutionToolProposal :exec
INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
SELECT sqlc.arg(id),sqlc.arg(agent_id),o.id,sqlc.arg(provider_call_id),sqlc.arg(name),sqlc.arg(input)::jsonb,
 sqlc.arg(type),'awaiting_authorization',o.created_at FROM model_outputs o
 WHERE o.agent_id=sqlc.arg(agent_id) AND o.id=sqlc.arg(output_id);

-- name: ReadExecutionProposals :many
SELECT id,provider_call_id,name,input,type FROM tool_calls
WHERE agent_id=sqlc.arg(agent_id) AND model_output_id=sqlc.arg(output_id) ORDER BY provider_call_id;

-- name: ValidateExecutionCheckpoint :one
SELECT (SELECT count(*) FROM agent_events e WHERE e.agent_id=sqlc.arg(agent_id)
 AND e.sequence BETWEEN sqlc.arg(start_sequence) AND sqlc.arg(end_sequence))::bigint AS events,
 EXISTS(SELECT 1 FROM agent_events e JOIN tool_calls c ON c.agent_id=e.agent_id AND c.model_output_id=e.model_output_id
 LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
 LEFT JOIN agent_events re ON re.agent_id=r.agent_id AND re.tool_call_result_id=r.id
 WHERE e.agent_id=sqlc.arg(agent_id) AND e.sequence BETWEEN sqlc.arg(start_sequence) AND sqlc.arg(end_sequence)
 AND (re.id IS NULL OR re.sequence>sqlc.arg(end_sequence))) AS open_authorities;

-- name: CreateExecutionCheckpoint :one
INSERT INTO context_checkpoints(agent_id,summarized_through_event_sequence,producer_model_call_context_id,summary,
 opening_input_ids,opening_event_sequence,created_at)
VALUES(sqlc.arg(agent_id),sqlc.arg(source_end),sqlc.arg(context_id),sqlc.arg(summary),
 sqlc.arg(opening_ids)::uuid[],sqlc.narg(opening_sequence),statement_timestamp())
RETURNING id,created_at;

-- name: FindExecutionCheckpoint :one
SELECT c.id,c.summary,c.summarized_through_event_sequence,e.id AS event_id,e.turn_id,e.sequence,e.created_at
FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.producer_model_call_context_id=sqlc.arg(context_id);

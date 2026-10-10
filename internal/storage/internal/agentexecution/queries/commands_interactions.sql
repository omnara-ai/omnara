-- name: ReadExecutionInteraction :one
SELECT i.id,i.tool_call_id,i.interaction_kind,i.state,i.request,i.resolution,i.resolved_by_input_id,
 i.destination,i.presentation_receipt
FROM agent_interactions i WHERE i.agent_id=sqlc.arg(agent_id) AND
 (i.id=sqlc.arg(id) OR (i.tool_call_id=sqlc.narg(tool_id) AND i.interaction_kind=sqlc.narg(kind)));

-- name: InsertExecutionInteraction :one
INSERT INTO agent_interactions(agent_id,tool_call_id,interaction_kind,state,request,destination,created_at)
VALUES(sqlc.arg(agent_id),sqlc.arg(tool_id),sqlc.arg(kind),'open',sqlc.arg(request)::jsonb,
 sqlc.narg(destination)::jsonb,statement_timestamp()) RETURNING id;

-- name: ResolveExecutionInteraction :execrows
UPDATE agent_interactions SET state=sqlc.arg(state),resolution=sqlc.arg(resolution)::jsonb,
 resolved_by_input_id=sqlc.narg(input_id),resolved_at=statement_timestamp()
WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id) AND state='open';

-- name: ListExecutionInteractions :many
SELECT i.id FROM agent_interactions i JOIN tool_calls c ON c.agent_id=i.agent_id AND c.id=i.tool_call_id
JOIN model_outputs o ON o.agent_id=c.agent_id AND o.id=c.model_output_id
JOIN model_call_contexts m ON m.agent_id=o.agent_id AND m.id=o.model_call_context_id
WHERE i.agent_id=sqlc.arg(agent_id) AND i.state='open' AND m.turn_id=sqlc.arg(turn_id)
ORDER BY i.id;

-- name: InsertExecutionResponse :one
INSERT INTO agent_inputs(project_id,agent_id,state,actor_id,input_kind,delivery_mode,target_interaction_id,
 idempotency_scope,input_idempotency_key,queued_at,metadata)
VALUES(sqlc.arg(project_id),sqlc.arg(agent_id),'received',sqlc.narg(actor_id),'interaction_response','immediate',
 sqlc.arg(interaction_id),'agent_interaction_response',sqlc.arg(interaction_id)::uuid::text,statement_timestamp(),'{}'::jsonb)
RETURNING id;

-- name: WriteExecutionPresentation :execrows
UPDATE agent_interactions SET presentation_receipt=sqlc.arg(receipt)::jsonb
WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id) AND destination=sqlc.arg(destination)::jsonb
 AND presentation_receipt IS NULL;

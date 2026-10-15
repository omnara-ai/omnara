-- name: ReadExecutionTool :one
SELECT c.id,c.agent_id,c.model_output_id,c.provider_call_id,c.name,c.input,c.type,c.state,c.runtime_lock_id,
 m.turn_id,m.id AS context_id,e.id AS source_event_id,e.sequence AS source_sequence,
 r.id AS result_id,r.outcome,r.completed_at,re.id AS result_event_id,re.sequence AS result_sequence
FROM tool_calls c JOIN model_outputs o ON o.agent_id=c.agent_id AND o.id=c.model_output_id
JOIN model_call_contexts m ON m.agent_id=o.agent_id AND m.id=o.model_call_context_id
JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
LEFT JOIN agent_events re ON re.agent_id=r.agent_id AND re.tool_call_result_id=r.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id);

-- name: TransitionExecutionTool :execrows
UPDATE tool_calls c SET state=sqlc.arg(next_state),runtime_lock_id=sqlc.narg(next_runtime_id)
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id) AND c.state=sqlc.arg(previous_state)
 AND c.runtime_lock_id IS NOT DISTINCT FROM sqlc.narg(previous_runtime_id)::uuid
 AND (sqlc.narg(fence_id)::uuid IS NULL OR EXISTS(SELECT 1 FROM agent_runtime_locks r
 WHERE r.agent_id=c.agent_id AND r.id=sqlc.narg(fence_id) AND r.cancel_requested_at IS NULL
 AND r.lease_expires_at>statement_timestamp()));

-- name: InsertExecutionToolResult :one
INSERT INTO tool_call_results(agent_id,tool_call_id,outcome,completed_at)
SELECT c.agent_id,c.id,sqlc.arg(outcome),statement_timestamp() FROM tool_calls c
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id) AND c.state='completed'
RETURNING id;

-- name: CloseExecutionToolInteractions :many
UPDATE agent_interactions SET state='canceled',resolution=jsonb_build_object('reason','tool_call_completed'),resolved_at=statement_timestamp()
WHERE agent_id=sqlc.arg(agent_id) AND tool_call_id=sqlc.arg(tool_id) AND state='open'
RETURNING id,interaction_kind,state;

-- name: ExecutionToolOwner :one
SELECT EXISTS(SELECT 1 FROM agent_interactions i WHERE i.agent_id=sqlc.arg(agent_id) AND i.tool_call_id=sqlc.arg(tool_id)
 AND i.id=sqlc.narg(interaction_id) AND i.interaction_kind='question' AND i.state='open') AS question,
 EXISTS(SELECT 1 FROM processes p WHERE p.agent_id=sqlc.arg(agent_id) AND p.tool_call_id=sqlc.arg(tool_id)
 AND p.id=sqlc.narg(process_id) AND p.state IN ('queued','starting','running')) AS process,
 EXISTS(SELECT 1 FROM process_actions p WHERE p.agent_id=sqlc.arg(agent_id) AND p.tool_call_id=sqlc.arg(tool_id)
 AND p.id=sqlc.narg(action_id) AND p.state IN ('queued','accepted')) AS action;

-- name: ReadExecutionToolDispatch :one
SELECT state FROM tool_calls WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id);

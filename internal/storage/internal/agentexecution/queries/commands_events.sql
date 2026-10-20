-- name: AppendExecutionEvent :one
WITH allocated AS (
 UPDATE agents SET next_event_sequence=next_event_sequence+1
 WHERE id=sqlc.arg(agent_id) AND project_id=sqlc.arg(project_id)
 RETURNING next_event_sequence-1 AS sequence
)
INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,model_output_id,
 context_checkpoint_id,tool_call_result_id,is_opening_event,created_at,idempotency_key)
SELECT sqlc.arg(id),sqlc.arg(agent_id),sqlc.narg(turn_id),sequence,sqlc.arg(kind),sqlc.narg(input_id),
 sqlc.narg(output_id),sqlc.narg(checkpoint_id),sqlc.narg(result_id),sqlc.arg(opening),coalesce(sqlc.narg(created_at)::timestamptz,statement_timestamp()),
 sqlc.narg(idempotency_key) FROM allocated
RETURNING id,sequence,created_at;

-- name: OpenExecutionTurn :exec
INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
SELECT sqlc.arg(id),sqlc.arg(agent_id),coalesce((SELECT max(turn_sequence)+1 FROM agent_turns
 WHERE agent_id=sqlc.arg(agent_id)),1),sqlc.arg(event_id),sqlc.arg(event_id);

-- name: AdvanceExecutionTurn :execrows
UPDATE agent_turns SET latest_event_id=sqlc.arg(event_id),
 latest_semantic_event_id=CASE WHEN sqlc.arg(semantic)::boolean THEN sqlc.arg(event_id) ELSE latest_semantic_event_id END
WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(turn_id);

-- name: InsertExecutionContent :execrows
INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,owner_model_output_id,owner_tool_call_result_id,
 ordinal,block_kind,text_content,structured_data,artifact_id,tool_call_id,exclude_from_model_context,metadata,created_at)
SELECT sqlc.arg(agent_id),sqlc.arg(owner_kind),sqlc.narg(input_id),sqlc.narg(output_id),sqlc.narg(result_id),
 p.ordinal,p.kind,p.text,p.data::jsonb,p.artifact_id,p.tool_id,p.exclude,p.metadata,sqlc.arg(created_at)::timestamptz
FROM jsonb_to_recordset(sqlc.arg(parts)::jsonb) AS p(ordinal integer,kind text,text text,data text,
 artifact_id uuid,tool_id uuid,exclude boolean,metadata jsonb)
WHERE EXISTS(SELECT 1 FROM agents WHERE id=sqlc.arg(agent_id) AND project_id=sqlc.arg(project_id));

-- name: ReadExecutionContent :many
SELECT ordinal,block_kind,text_content,structured_data,artifact_id,tool_call_id,exclude_from_model_context,metadata
FROM content_blocks WHERE agent_id=sqlc.arg(agent_id) AND
 (owner_agent_input_id=sqlc.narg(input_id) OR owner_model_output_id=sqlc.narg(output_id)
 OR owner_tool_call_result_id=sqlc.narg(result_id)) ORDER BY ordinal;

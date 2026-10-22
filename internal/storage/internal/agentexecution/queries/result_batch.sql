-- name: QueueExecutionOutput :batchone
INSERT INTO model_outputs(id,agent_id,model_call_context_id,served_provider_model_slug,stop_reason,provider_replay,created_at)
VALUES(sqlc.arg(id),sqlc.arg(agent_id),sqlc.arg(context_id),sqlc.arg(served_model),sqlc.arg(stop_reason),sqlc.narg(replay)::jsonb,statement_timestamp())
RETURNING id,created_at;

-- name: QueueExecutionEvent :batchone
WITH payload AS MATERIALIZED (
 SELECT agent_payload_created_at(sqlc.arg(project_id),sqlc.arg(agent_id),CASE WHEN sqlc.narg(result_id)::uuid IS NOT NULL THEN 'tool_call_result' ELSE sqlc.arg(kind) END,
 coalesce(sqlc.narg(input_id)::uuid,sqlc.narg(output_id)::uuid,sqlc.narg(checkpoint_id)::uuid,sqlc.narg(result_id)::uuid),true) AS created_at
), allocated AS (
 UPDATE agents SET next_event_sequence=next_event_sequence+1 WHERE id=sqlc.arg(agent_id)
 RETURNING next_event_sequence-1 AS sequence
)
INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,model_output_id,
 context_checkpoint_id,tool_call_result_id,is_opening_event,created_at,idempotency_key)
SELECT sqlc.arg(id),sqlc.arg(agent_id),sqlc.narg(turn_id),sequence,sqlc.arg(kind),sqlc.narg(input_id),
 sqlc.narg(output_id),sqlc.narg(checkpoint_id),sqlc.narg(result_id),sqlc.arg(opening),payload.created_at,
 sqlc.narg(idempotency_key) FROM allocated CROSS JOIN payload
RETURNING id,sequence,created_at;

-- name: QueueExecutionContent :batchone
WITH payload AS MATERIALIZED (
 SELECT agent_payload_created_at(sqlc.arg(project_id),sqlc.arg(agent_id),sqlc.arg(owner_kind),
 coalesce(sqlc.narg(input_id)::uuid,sqlc.narg(output_id)::uuid,sqlc.narg(result_id)::uuid),false) AS created_at
), inserted AS (
INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,owner_model_output_id,owner_tool_call_result_id,
 ordinal,block_kind,text_content,structured_data,artifact_id,tool_call_id,exclude_from_model_context,metadata,created_at)
SELECT sqlc.arg(agent_id),sqlc.arg(owner_kind),sqlc.narg(input_id),sqlc.narg(output_id),sqlc.narg(result_id),
 p.ordinal,p.kind,p.text,p.data::jsonb,p.artifact_id,p.tool_id,p.exclude,p.metadata,payload.created_at
FROM payload CROSS JOIN jsonb_to_recordset(sqlc.arg(parts)::jsonb) AS p(ordinal integer,kind text,text text,data text,
 artifact_id uuid,tool_id uuid,exclude boolean,metadata jsonb) RETURNING 1
) SELECT count(*)::bigint FROM inserted;

-- name: QueueExecutionSuccess :batchone
UPDATE model_call_contexts c SET state=sqlc.arg(state),recovery_kind=sqlc.narg(recovery),
 retry_at=CASE WHEN sqlc.narg(retry_microseconds)::bigint IS NULL THEN NULL
 ELSE statement_timestamp()+sqlc.narg(retry_microseconds)::bigint*interval '1 microsecond' END,
 api_format=sqlc.arg(api_format),api_variant=sqlc.arg(api_variant),provider_request_id=sqlc.arg(request_id),
 provider_response_id=sqlc.arg(response_id),error_kind=sqlc.arg(error_kind),error_code=sqlc.arg(error_code),
 error_message=sqlc.arg(error_message),error_details=sqlc.arg(error_details)::jsonb,
 input_tokens_total=sqlc.narg(input_tokens)::integer,uncached_input_tokens=sqlc.narg(uncached_tokens)::integer,
 cache_read_input_tokens=sqlc.narg(cache_read_tokens)::integer,cache_write_input_tokens=sqlc.narg(cache_write_tokens)::integer,
 output_tokens_total=sqlc.narg(output_tokens)::integer,reasoning_output_tokens=sqlc.narg(reasoning_tokens)::integer,
 provider_reported_cost_usd=sqlc.narg(cost)::text::numeric,provider_metadata=sqlc.arg(provider_metadata)::jsonb,
 input_fingerprint=sqlc.narg(input_fingerprint)::text,completed_at=statement_timestamp()
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id) AND c.state='started' AND c.runtime_lock_id=sqlc.arg(runtime_id)
 AND EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.agent_id=c.agent_id
 AND r.id=sqlc.arg(runtime_id) AND r.cancel_requested_at IS NULL AND r.lease_expires_at>statement_timestamp()) RETURNING c.id;

-- name: QueueExecutionRelease :batchone
DELETE FROM agent_runtime_locks WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id) RETURNING id;

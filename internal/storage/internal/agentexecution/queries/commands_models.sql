-- name: FenceExecutionRuntime :one
SELECT r.id FROM agent_runtime_locks r JOIN agents a ON a.id=r.agent_id
WHERE a.project_id=sqlc.arg(project_id) AND a.id=sqlc.arg(agent_id) AND a.state='active'
 AND r.id=sqlc.arg(runtime_id) AND r.cancel_requested_at IS NULL AND r.lease_expires_at>statement_timestamp();

-- name: ExecutionPreparationBoundary :one
SELECT a.current_config_id,(a.next_event_sequence-1)::bigint AS watermark,statement_timestamp()::timestamptz AS database_now,
 EXISTS(SELECT 1 FROM model_call_contexts c WHERE c.agent_id=a.id AND c.state='started') AS started
FROM agents a WHERE a.id=sqlc.arg(agent_id) AND a.state='active';

-- name: ReadExecutionAttempt :one
SELECT c.id,c.agent_id,c.turn_id,c.operation_kind,c.attempt_number,c.input_event_sequence,c.source_event_sequence_end,
 c.state,c.recovery_kind,c.retry_at,c.opening_input_ids,c.opening_event_sequence,c.runtime_lock_id,
 c.agent_config_id,c.configured_model_revision_id,c.api_format,c.api_variant,c.provider_request_id,c.provider_response_id,
 c.error_kind,c.error_code,c.error_message,c.error_details,c.input_tokens_total,c.uncached_input_tokens,
 c.cache_read_input_tokens,c.cache_write_input_tokens,c.output_tokens_total,c.reasoning_output_tokens,
 coalesce(c.provider_reported_cost_usd::text,'')::text AS provider_reported_cost_usd,c.provider_metadata,
 r.provider_model_slug
FROM model_call_contexts c JOIN configured_model_revisions r ON r.org_id=c.org_id AND r.id=c.configured_model_revision_id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id);

-- name: ExecutionModelRevision :one
-- @sqlc-vet-disable configured-models-deleted-at
-- @sqlc-vet-disable model-provider-configs-deleted-at
SELECT m.current_revision_id,CASE WHEN p.management_kind='cluster' THEN coalesce(w.new_managed_work_allowed,true)
 ELSE true END::boolean AS allowed
FROM agent_configs c JOIN configured_models m ON m.org_id=c.org_id AND m.id=c.configured_model_id
JOIN model_provider_configs p ON p.org_id=m.org_id AND p.id=m.model_provider_config_id
LEFT JOIN org_managed_work_admission w ON w.org_id=c.org_id
WHERE c.project_id=sqlc.arg(project_id) AND c.id=sqlc.arg(config_id);

-- name: CreateExecutionAttempt :one
INSERT INTO model_call_contexts(org_id,project_id,agent_id,id,turn_id,operation_kind,attempt_number,
 agent_config_id,configured_model_revision_id,input_event_sequence,source_event_sequence_end,runtime_lock_id,
 opening_input_ids,opening_event_sequence,state,created_at)
SELECT a.org_id,a.project_id,a.id,sqlc.arg(id),sqlc.arg(turn_id),sqlc.arg(operation),sqlc.arg(attempt),
 sqlc.arg(config_id),sqlc.arg(revision_id),sqlc.arg(watermark),sqlc.narg(source_end),sqlc.arg(runtime_id),
 sqlc.arg(opening_ids)::uuid[],sqlc.arg(opening_sequence),'started',statement_timestamp()
FROM agents a WHERE a.id=sqlc.arg(agent_id) AND a.project_id=sqlc.arg(project_id)
 AND EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.agent_id=a.id AND r.id=sqlc.arg(runtime_id)
 AND r.cancel_requested_at IS NULL AND r.lease_expires_at>statement_timestamp())
RETURNING id;

-- name: FinishExecutionAttempt :execrows
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
 completed_at=statement_timestamp()
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id) AND c.state='started' AND c.runtime_lock_id=sqlc.arg(runtime_id)
 AND EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.agent_id=c.agent_id
 AND r.id=sqlc.arg(runtime_id) AND r.cancel_requested_at IS NULL AND r.lease_expires_at>statement_timestamp());

-- name: LatestExecutionAttempt :one
SELECT id FROM model_call_contexts WHERE agent_id=sqlc.arg(agent_id) AND operation_kind=sqlc.arg(operation)
 AND input_event_sequence=sqlc.arg(watermark)
 AND source_event_sequence_end IS NOT DISTINCT FROM sqlc.narg(source_end)::bigint
ORDER BY attempt_number DESC LIMIT 1;

-- name: ExecutionCompactionBoundary :one
SELECT coalesce((SELECT summarized_through_event_sequence FROM context_checkpoints
 WHERE agent_id=sqlc.arg(agent_id) AND summarized_through_event_sequence<=sqlc.arg(watermark)
 ORDER BY summarized_through_event_sequence DESC LIMIT 1),0)::bigint AS summarized_through;

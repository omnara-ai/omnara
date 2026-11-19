-- name: InsertExecutionBatchRequest :execrows
INSERT INTO model_batch_requests(id,agent_id,runtime_lock_id,model_call_context_id,turn_id,
 opening_input_ids,opening_event_sequence,callback_token_hash,custom_id,input_fingerprint)
SELECT sqlc.arg(id),a.id,r.id,sqlc.arg(context_id),sqlc.arg(turn_id),
 sqlc.arg(opening_input_ids)::uuid[],sqlc.arg(opening_event_sequence),sqlc.arg(callback_token_hash),
 sqlc.arg(custom_id),sqlc.arg(input_fingerprint)
FROM agent_runtime_locks r JOIN agents a ON a.id=r.agent_id
WHERE r.id=sqlc.arg(runtime_id) AND r.agent_id=sqlc.arg(agent_id) AND a.project_id=sqlc.arg(project_id);

-- name: ReadExecutionBatchRequest :one
SELECT b.id,a.project_id,b.agent_id,b.runtime_lock_id,b.model_call_context_id,b.turn_id,b.opening_input_ids,
 b.opening_event_sequence,b.custom_id,b.input_fingerprint,b.provider_batch_id,b.state,b.deadline_at,b.created_at
FROM model_batch_requests b JOIN agents a ON a.id=b.agent_id
WHERE b.id=sqlc.arg(id) AND b.agent_id=sqlc.arg(agent_id) AND b.runtime_lock_id=sqlc.arg(runtime_id);

-- name: ParkExecutionRuntime :execrows
UPDATE agent_runtime_locks r
SET lease_expires_at=greatest(r.lease_expires_at,b.deadline_at)
FROM model_batch_requests b
WHERE r.agent_id=sqlc.arg(agent_id) AND r.id=sqlc.arg(id)
 AND b.runtime_lock_id=r.id AND b.id=sqlc.arg(batch_request_id) AND b.state='parked';

-- name: ParkExecutionBatchRequest :one
UPDATE model_batch_requests b
SET state='parked',
 provider_batch_id=CASE WHEN sqlc.arg(provider_batch_id)::text='' THEN b.provider_batch_id ELSE sqlc.arg(provider_batch_id)::text END,
 deadline_at=coalesce(b.deadline_at,
  statement_timestamp()+sqlc.arg(hold_microseconds)::bigint*interval '1 microsecond'),
 resume_at=CASE WHEN b.wake_requested_at IS NOT NULL OR EXISTS(
   SELECT 1 FROM agent_runtime_locks r WHERE r.id=b.runtime_lock_id AND r.cancel_requested_at IS NOT NULL)
  THEN statement_timestamp()
  ELSE statement_timestamp()+sqlc.arg(poll_microseconds)::bigint*interval '1 microsecond' END,
 updated_at=statement_timestamp()
WHERE b.id=sqlc.arg(id) AND b.agent_id=sqlc.arg(agent_id) AND b.runtime_lock_id=sqlc.arg(runtime_id)
 AND b.state IN ('submitting','collecting')
RETURNING b.resume_at;

-- name: LockExecutionParkedBatchRuntime :one
SELECT b.id,r.id AS runtime_lock_id
FROM model_batch_requests b JOIN agent_runtime_locks r ON r.id=b.runtime_lock_id
WHERE b.state='parked' AND b.resume_at<=statement_timestamp()
ORDER BY b.resume_at,b.id LIMIT 1
FOR UPDATE OF r SKIP LOCKED;

-- name: ResumeExecutionBatchRequest :one
UPDATE model_batch_requests b
SET state='collecting',wake_requested_at=NULL,updated_at=statement_timestamp()
FROM agents a
WHERE b.id=sqlc.arg(id) AND b.runtime_lock_id=sqlc.arg(runtime_id) AND b.state='parked'
 AND b.resume_at<=statement_timestamp() AND a.id=b.agent_id
RETURNING b.id,a.project_id,b.agent_id,b.runtime_lock_id,b.model_call_context_id,b.turn_id,b.opening_input_ids,
 b.opening_event_sequence,b.custom_id,b.input_fingerprint,b.provider_batch_id,b.state,b.deadline_at;

-- name: UnparkExecutionRuntime :one
UPDATE agent_runtime_locks r
SET worker_process_id=sqlc.arg(worker_id),renewed_at=greatest(r.renewed_at,statement_timestamp()),
 lease_expires_at=greatest(r.renewed_at,statement_timestamp())+sqlc.arg(lease_microseconds)::bigint*interval '1 microsecond'
WHERE r.id=sqlc.arg(id)
RETURNING r.lease_expires_at;

-- name: WakeExecutionBatchRequest :execrows
UPDATE model_batch_requests
SET wake_requested_at=statement_timestamp(),
 resume_at=CASE WHEN state='parked' THEN least(resume_at,statement_timestamp()) ELSE resume_at END,
 updated_at=statement_timestamp()
WHERE id=sqlc.arg(id) AND callback_token_hash=sqlc.arg(callback_token_hash);

-- name: WakeExecutionRuntimeBatchRequests :exec
UPDATE model_batch_requests
SET wake_requested_at=statement_timestamp(),
 resume_at=CASE WHEN state='parked' THEN least(resume_at,statement_timestamp()) ELSE resume_at END,
 updated_at=statement_timestamp()
WHERE runtime_lock_id=sqlc.arg(runtime_id);

-- name: DeleteExecutionBatchRequest :exec
DELETE FROM model_batch_requests WHERE id=sqlc.arg(id) AND runtime_lock_id=sqlc.arg(runtime_id);

-- name: DeleteExecutionOrphanBatchRequests :execrows
DELETE FROM model_batch_requests b
WHERE NOT EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.id=b.runtime_lock_id);

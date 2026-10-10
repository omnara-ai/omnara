-- name: LockExecutionRuntime :one
SELECT r.id FROM agent_runtime_locks r WHERE r.agent_id=sqlc.arg(agent_id) AND r.id=sqlc.arg(id) FOR UPDATE;

-- name: TryLockExecutionRuntime :one
SELECT r.id FROM agent_runtime_locks r WHERE r.agent_id=sqlc.arg(agent_id) AND r.id=sqlc.arg(id) FOR UPDATE SKIP LOCKED;

-- name: ReadExecutionRuntime :one
SELECT id,worker_process_id,cancel_requested_at,lease_expires_at,statement_timestamp()::timestamptz AS database_now
FROM agent_runtime_locks WHERE agent_id=sqlc.arg(agent_id) AND (sqlc.narg(id)::uuid IS NULL OR id=sqlc.narg(id));

-- name: AcquireExecutionRuntime :one
INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
VALUES(sqlc.arg(id),sqlc.arg(agent_id),sqlc.arg(worker_id),statement_timestamp(),statement_timestamp(),
 statement_timestamp()+sqlc.arg(lease_microseconds)::bigint*interval '1 microsecond') RETURNING id;

-- name: DeleteExecutionRuntime :execrows
DELETE FROM agent_runtime_locks WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id);

-- name: RenewExecutionRuntime :one
UPDATE agent_runtime_locks r SET renewed_at=greatest(r.renewed_at,statement_timestamp()),
 lease_expires_at=greatest(r.lease_expires_at,greatest(r.renewed_at,statement_timestamp())+sqlc.arg(lease_microseconds)::bigint*interval '1 microsecond')
WHERE r.agent_id=sqlc.arg(agent_id) AND r.id=sqlc.arg(id)
 AND EXISTS(SELECT 1 FROM agents a WHERE a.id=r.agent_id AND a.project_id=sqlc.arg(project_id))
RETURNING r.lease_expires_at;

-- name: RequestExecutionCancel :one
UPDATE agent_runtime_locks SET cancel_requested_at=coalesce(cancel_requested_at,statement_timestamp())
WHERE agent_id=sqlc.arg(agent_id) RETURNING id,worker_process_id;

-- name: ListExecutionLiveContexts :many
SELECT id,runtime_lock_id FROM model_call_contexts WHERE agent_id=sqlc.arg(agent_id) AND state='started'
 AND (sqlc.narg(runtime_id)::uuid IS NULL OR runtime_lock_id=sqlc.narg(runtime_id)) ORDER BY id;

-- name: TeardownExecutionContext :execrows
UPDATE model_call_contexts SET state=sqlc.arg(state),recovery_kind=sqlc.narg(recovery),
 retry_at=CASE WHEN sqlc.narg(retry_microseconds)::bigint IS NULL THEN NULL
 ELSE statement_timestamp()+sqlc.narg(retry_microseconds)::bigint*interval '1 microsecond' END,
 error_kind=sqlc.arg(error_kind),error_code=sqlc.arg(error_code),error_message=sqlc.arg(error_message),
 error_details=sqlc.arg(error_details)::jsonb,completed_at=statement_timestamp()
WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id) AND runtime_lock_id=sqlc.arg(runtime_id) AND state='started';

-- name: ListExecutionUnfinishedTools :many
SELECT c.id FROM tool_calls c JOIN model_outputs o ON o.agent_id=c.agent_id AND o.id=c.model_output_id
JOIN model_call_contexts m ON m.agent_id=o.agent_id AND m.id=o.model_call_context_id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.state<>'completed'
 AND (sqlc.narg(turn_id)::uuid IS NULL OR m.turn_id=sqlc.narg(turn_id))
 AND (sqlc.narg(runtime_id)::uuid IS NULL OR (c.state='running' AND c.runtime_lock_id=sqlc.narg(runtime_id))) ORDER BY c.id;

-- name: InsertExecutionStop :one
INSERT INTO agent_inputs(project_id,agent_id,state,actor_id,input_kind,delivery_mode,control_type,
 idempotency_scope,input_idempotency_key,queued_at,metadata)
SELECT a.project_id,a.id,'received',sqlc.narg(actor_id),'control','immediate','cancel_current','agent_control',
 'agent-cancel:'||a.id::text||':after:'||(a.next_event_sequence-1)::text,statement_timestamp(),'{}'::jsonb
FROM agents a WHERE a.id=sqlc.arg(agent_id) RETURNING id;

-- name: CancelExecutionInputs :execrows
UPDATE agent_inputs SET state='canceled',canceled_at=statement_timestamp()
WHERE agent_id=sqlc.arg(agent_id) AND state='received' AND input_kind='content'
 AND (delivery_mode='steering' OR sqlc.arg(include_queued)::boolean);

-- name: ConsumeExecutionWakeup :exec
DELETE FROM agent_wakeups WHERE agent_id=sqlc.arg(agent_id);

-- name: ExecutionRetainedRuntimeWork :one
SELECT EXISTS(SELECT 1 FROM model_call_contexts c WHERE c.agent_id=sqlc.arg(agent_id)
 AND c.runtime_lock_id=sqlc.arg(runtime_id) AND c.state='started') OR
 EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.runtime_lock_id=sqlc.arg(runtime_id)
 AND c.state='running') AS retained;

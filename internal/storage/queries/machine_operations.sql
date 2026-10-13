-- name: InsertMachineOperation :one
INSERT INTO machine_operations (id, org_id, machine_id, execution_spec, timeout_seconds)
VALUES (
  sqlc.arg(id),
  sqlc.arg(org_id),
  sqlc.arg(machine_id),
  sqlc.arg(execution_spec),
  sqlc.arg(timeout_seconds)
)
RETURNING id, org_id, machine_id, execution_spec, timeout_seconds, state, daemon_runtime_id, exit_code, exit_signal, state_reason_code, state_reason_message, result, created_at, updated_at;

-- name: GetMachineOperation :one
SELECT operation.id, operation.org_id, operation.machine_id, operation.execution_spec, operation.timeout_seconds, operation.state, operation.daemon_runtime_id, operation.exit_code, operation.exit_signal, operation.state_reason_code, operation.state_reason_message, operation.result, operation.created_at, operation.updated_at
FROM machine_operations operation
WHERE operation.org_id = sqlc.arg(org_id)
  AND operation.machine_id = sqlc.arg(machine_id)
  AND operation.id = sqlc.arg(id);

-- name: AcceptMachineOperation :one
UPDATE machine_operations operation
SET state = 'starting',
    daemon_runtime_id = sqlc.arg(daemon_runtime_id)::uuid,
    updated_at = statement_timestamp()
WHERE operation.org_id = sqlc.arg(org_id)
  AND operation.machine_id = sqlc.arg(machine_id)
  AND operation.id = sqlc.arg(id)
  AND operation.state = 'queued'
  AND EXISTS (
    SELECT 1
    FROM online_daemon_runtimes runtime
    WHERE runtime.id = sqlc.arg(daemon_runtime_id)::uuid
      AND runtime.org_id = operation.org_id
      AND runtime.machine_id = operation.machine_id
      AND runtime.daemon_token_id = sqlc.arg(daemon_token_id)::uuid
  )
RETURNING operation.id, operation.org_id, operation.machine_id, operation.execution_spec, operation.timeout_seconds, operation.state, operation.daemon_runtime_id, operation.exit_code, operation.exit_signal, operation.state_reason_code, operation.state_reason_message, operation.result, operation.created_at, operation.updated_at;

-- name: MarkMachineOperationRunning :execrows
UPDATE machine_operations operation
SET state = 'running',
    updated_at = statement_timestamp()
WHERE operation.org_id = sqlc.arg(org_id)
  AND operation.machine_id = sqlc.arg(machine_id)
  AND operation.id = sqlc.arg(id)
  AND operation.state = 'starting';

-- name: CompleteMachineOperation :execrows
UPDATE machine_operations operation
SET state = sqlc.arg(state),
    exit_code = sqlc.narg(exit_code),
    exit_signal = sqlc.arg(exit_signal),
    state_reason_code = sqlc.arg(state_reason_code),
    state_reason_message = sqlc.arg(state_reason_message),
    result = sqlc.narg(result),
    daemon_runtime_id = coalesce(operation.daemon_runtime_id, sqlc.narg(daemon_runtime_id)::uuid),
    updated_at = statement_timestamp()
WHERE operation.org_id = sqlc.arg(org_id)
  AND operation.machine_id = sqlc.arg(machine_id)
  AND operation.id = sqlc.arg(id)
  AND operation.state IN ('queued', 'starting', 'running');

-- name: MarkUnclaimedMachineOperationsUnknown :execrows
UPDATE machine_operations operation
SET state = 'unknown',
    state_reason_code = sqlc.arg(state_reason_code),
    updated_at = statement_timestamp()
WHERE operation.org_id = sqlc.arg(org_id)
  AND operation.machine_id = sqlc.arg(machine_id)
  AND operation.state IN ('starting', 'running')
  AND NOT (operation.id = ANY(sqlc.arg(claimed_ids)::uuid[]));

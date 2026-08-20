-- name: GetProcessSupervisorIdentity :one
SELECT supervisor_instance_id, supervisor_token
FROM processes
WHERE process_id = sqlc.arg(process_id);

-- name: InsertProcess :exec
INSERT INTO processes(
    process_id,
    supervisor_instance_id,
    supervisor_token,
    phase
)
VALUES(
    sqlc.arg(process_id),
    sqlc.arg(supervisor_instance_id),
    sqlc.arg(supervisor_token),
    'preparing'
);

-- name: MarkProcessPrepared :execrows
UPDATE processes
SET phase = 'prepared'
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id)
  AND phase IN ('preparing', 'prepared')
  AND local_closed = 0;

-- name: MarkProcessAccepted :execrows
UPDATE processes
SET phase = 'accepted'
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id)
  AND phase IN ('prepared', 'accepted')
  AND local_closed = 0
  AND server_released = 0;

-- name: DeleteProcess :exec
DELETE FROM processes
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id);

-- name: AuthorizeProcessSpawn :execrows
UPDATE processes
SET exec_committed = 1
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id)
  AND phase = 'accepted'
  AND exec_committed = 0
  AND local_closed = 0;

-- name: RecordProcessSpawned :execrows
UPDATE processes
SET containment_kind = sqlc.arg(containment_kind),
    containment_id = sqlc.arg(containment_id)
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id)
  AND phase IN ('accepted', 'terminal')
  AND exec_committed = 1
  AND local_closed = 0
  AND (
    containment_kind = ''
    OR containment_kind = sqlc.arg(containment_kind)
  )
  AND (
    containment_id = ''
    OR containment_id = sqlc.arg(containment_id)
  );

-- name: CloseProcessActionAdmission :execrows
UPDATE processes
SET action_admission_closed = 1
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id)
  AND phase IN ('accepted', 'terminal');

-- name: MarkProcessContainmentEmpty :execrows
UPDATE processes
SET containment_empty = 1
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id)
  AND phase IN ('accepted', 'terminal');

-- name: GetProcess :one
SELECT process_id,
       supervisor_instance_id,
       supervisor_token,
       phase,
       resolved_action_seq,
       exec_committed,
       containment_kind,
       containment_id,
       containment_empty,
       action_admission_closed,
       local_closed,
       server_released
FROM processes
WHERE process_id = sqlc.arg(process_id);

-- name: ListProcesses :many
SELECT process_id,
       supervisor_instance_id,
       supervisor_token,
       phase,
       resolved_action_seq,
       exec_committed,
       containment_kind,
       containment_id,
       containment_empty,
       action_admission_closed,
       local_closed,
       server_released
FROM processes
ORDER BY process_id;

-- name: CountTerminalProcessReports :one
SELECT count(*)
FROM frozen_reports
WHERE process_id = sqlc.arg(process_id)
  AND action_id IS NULL
  AND report_kind = 'process_terminal';

-- name: CountUnresolvedProcessActions :one
SELECT count(*)
FROM process_actions action
WHERE action.process_id = sqlc.arg(process_id)
  AND NOT EXISTS (
    SELECT 1
    FROM frozen_reports report
    WHERE report.action_id = action.action_id
  );

-- name: MarkProcessLocalClosed :exec
UPDATE processes
SET local_closed = 1
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id);

-- name: MarkProcessServerReleased :execrows
UPDATE processes
SET server_released = 1
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id);

-- name: MarkTerminalProcessServerReleased :exec
UPDATE processes
SET server_released = 1
WHERE process_id = sqlc.arg(process_id);

-- name: CountProcessActions :one
SELECT count(*)
FROM process_actions
WHERE process_id = sqlc.arg(process_id);

-- name: CountUnsettledProcessReports :one
SELECT count(*)
FROM frozen_reports
WHERE process_id = sqlc.arg(process_id)
  AND state <> 'acknowledged';

-- name: SetProcessTerminal :exec
UPDATE processes
SET phase = 'terminal',
    action_admission_closed = 1
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id);

-- name: AdvanceResolvedActionSequence :exec
UPDATE processes
SET resolved_action_seq = sqlc.arg(resolved_action_seq)
WHERE process_id = sqlc.arg(process_id);

-- name: AdvanceReleasedActionSequence :exec
UPDATE processes
SET resolved_action_seq = sqlc.arg(resolved_action_seq)
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id);

-- name: CompactMissingActionPrefix :exec
UPDATE processes
SET resolved_action_seq = sqlc.arg(next_resolved_action_seq)
WHERE process_id = sqlc.arg(process_id)
  AND supervisor_instance_id = sqlc.arg(supervisor_instance_id)
  AND resolved_action_seq = sqlc.arg(previous_resolved_action_seq);

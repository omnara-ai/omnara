-- name: InsertFrozenReport :exec
INSERT INTO frozen_reports(
    report_id,
    process_id,
    action_id,
    report_kind,
    body
)
VALUES(
    sqlc.arg(report_id),
    sqlc.arg(process_id),
    sqlc.narg(action_id),
    sqlc.arg(report_kind),
    sqlc.arg(body)
);

-- name: GetFrozenProcessReportBySlot :one
SELECT report_id,
       process_id,
       action_id,
       report_kind,
       body,
       state,
       error_code,
       error
FROM frozen_reports
WHERE process_id = sqlc.arg(process_id)
  AND action_id IS NULL
  AND report_kind = sqlc.arg(report_kind);

-- name: GetFrozenActionReportBySlot :one
SELECT report_id,
       process_id,
       action_id,
       report_kind,
       body,
       state,
       error_code,
       error
FROM frozen_reports
WHERE process_id = sqlc.arg(process_id)
  AND action_id = sqlc.arg(action_id)
  AND report_kind = sqlc.arg(report_kind);

-- name: GetFrozenReportByAction :one
SELECT report_id,
       process_id,
       action_id,
       report_kind,
       body,
       state,
       error_code,
       error
FROM frozen_reports
WHERE action_id = sqlc.arg(action_id);

-- name: GetFrozenReportByID :one
SELECT report_id,
       process_id,
       action_id,
       report_kind,
       body,
       state,
       error_code,
       error
FROM frozen_reports
WHERE report_id = sqlc.arg(report_id);

-- name: ListFrozenReportDeliveryCandidates :many
SELECT report.report_id,
       report.process_id,
       report.action_id,
       report.report_kind,
       report.body,
       report.state,
       report.error_code,
       report.error
FROM frozen_reports report
LEFT JOIN process_actions action
  ON action.action_id = report.action_id
WHERE report.state IN ('pending', 'rejected')
  AND (
    report.report_kind <> 'process_terminal'
    OR NOT EXISTS (
      SELECT 1
      FROM process_actions pending_action
      WHERE pending_action.process_id = report.process_id
        AND NOT EXISTS (
          SELECT 1
          FROM frozen_reports rejected_action
          WHERE rejected_action.action_id = pending_action.action_id
            AND rejected_action.report_kind = 'action_terminal'
            AND rejected_action.state = 'rejected'
        )
    )
  )
ORDER BY report.process_id,
         CASE report.report_kind
           WHEN 'process_started' THEN 0
           WHEN 'action_terminal' THEN 1
           ELSE 2
         END,
         coalesce(action.seq, 0),
         report.report_id;

-- name: ListFrozenReportsForProcess :many
SELECT report.report_id,
       report.process_id,
       report.action_id,
       report.report_kind,
       report.body,
       report.state,
       report.error_code,
       report.error
FROM frozen_reports report
LEFT JOIN process_actions action
  ON action.action_id = report.action_id
WHERE report.process_id = sqlc.arg(process_id)
ORDER BY CASE report.report_kind
           WHEN 'process_started' THEN 0
           WHEN 'action_terminal' THEN 1
           ELSE 2
         END,
         coalesce(action.seq, 0),
         report.report_id;

-- name: ListFrozenReports :many
SELECT report_id,
       process_id,
       action_id,
       report_kind,
       body,
       state,
       error_code,
       error
FROM frozen_reports;

-- name: AcknowledgeProcessReport :exec
UPDATE frozen_reports
SET state = 'acknowledged',
    error_code = '',
    error = ''
WHERE report_id = sqlc.arg(report_id)
  AND state IN ('pending', 'rejected');

-- name: RejectFrozenReport :exec
UPDATE frozen_reports
SET state = 'rejected',
    error_code = sqlc.arg(error_code),
    error = sqlc.arg(error)
WHERE report_id = sqlc.arg(report_id)
  AND state = 'pending';

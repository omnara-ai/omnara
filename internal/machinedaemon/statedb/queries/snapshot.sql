-- name: ListProcessActionSnapshots :many
SELECT action.action_id,
       action.process_id,
       action.action_kind,
       action.seq,
       action.effect_committed,
       report.report_id IS NOT NULL AS reported,
       coalesce(report.report_id, '') AS report_id,
       coalesce(report.state, '') AS report_state
FROM process_actions action
LEFT JOIN frozen_reports report
  ON report.action_id = action.action_id
ORDER BY action.process_id, action.seq;

-- name: ListRejectedProcessReportIDs :many
SELECT process_id, report_id
FROM frozen_reports
WHERE action_id IS NULL
  AND state = 'rejected'
ORDER BY process_id, report_id;

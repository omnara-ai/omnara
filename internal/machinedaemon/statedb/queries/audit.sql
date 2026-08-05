-- name: GetInvariantViolations :one
SELECT EXISTS (
         SELECT 1
         FROM processes process
         WHERE process.phase = 'terminal'
           AND NOT EXISTS (
             SELECT 1
             FROM frozen_reports report
             WHERE report.process_id = process.process_id
               AND report.action_id IS NULL
               AND report.report_kind = 'process_terminal'
           )
       ) AS terminal_process_missing_report,
       EXISTS (
         SELECT 1
         FROM frozen_reports report
         JOIN processes process
           ON process.process_id = report.process_id
         WHERE report.report_kind = 'process_terminal'
           AND process.phase <> 'terminal'
       ) AS terminal_report_on_nonterminal_process,
       EXISTS (
         SELECT 1
         FROM processes
         WHERE local_closed = 1
           AND phase <> 'terminal'
       ) AS locally_closed_process_not_terminal,
       EXISTS (
         SELECT 1
         FROM processes process
         JOIN process_actions action
           ON action.process_id = process.process_id
         LEFT JOIN frozen_reports report
           ON report.action_id = action.action_id
         WHERE process.local_closed = 1
           AND report.report_id IS NULL
       ) AS locally_closed_process_action_missing_report,
       EXISTS (
         SELECT 1
         FROM process_actions action
         LEFT JOIN frozen_reports report
           ON report.action_id = action.action_id
         WHERE action.effect_committed = 0
           AND report.report_id IS NULL
       ) AS no_effect_action_missing_report,
       EXISTS (
         SELECT 1
         FROM process_actions action
         JOIN processes process
           ON process.process_id = action.process_id
         WHERE action.seq <= process.resolved_action_seq
       ) AS action_behind_resolved_frontier;

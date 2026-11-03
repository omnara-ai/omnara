-- name: ReadExecutionProcessOwner :one
SELECT p.id,p.org_id,p.machine_id,p.tool_call_id,p.state,p.state_reason_code,p.state_reason_message,p.exit_code,
 p.exit_signal,p.execution_spec,p.execution_granted_at,p.source_started_at,p.source_ended_at,p.default_output_cursor,p.exit_notice_decided,
 EXISTS(SELECT 1 FROM process_actions a WHERE a.agent_id=p.agent_id AND a.process_id=p.id
 AND a.action_kind='terminate' AND a.state IN ('queued','accepted','applied')) AS stop_requested,
 EXISTS(SELECT 1 FROM process_actions a JOIN tool_calls t ON t.agent_id=a.agent_id AND t.id=a.tool_call_id
 WHERE a.agent_id=p.agent_id AND a.process_id=p.id AND a.action_kind='read' AND t.state<>'completed') AS read_pending
FROM processes p WHERE p.agent_id=sqlc.arg(agent_id) AND p.id=sqlc.arg(id) FOR UPDATE;

-- name: MarkProcessExitNoticeDecided :exec
UPDATE processes SET exit_notice_decided=true WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id);

-- name: ReadExecutionActionOwner :one
SELECT a.id,a.process_id,a.tool_call_id,a.action_kind,a.seq,a.payload,a.state,a.state_reason_code,a.state_reason_message,
 EXISTS(SELECT 1 FROM process_actions earlier WHERE earlier.agent_id=a.agent_id AND earlier.process_id=a.process_id
 AND earlier.seq<a.seq AND earlier.state IN ('queued','accepted')) AS earlier_pending
FROM process_actions a WHERE a.agent_id=sqlc.arg(agent_id) AND a.id=sqlc.arg(id) FOR UPDATE;

-- name: ReadProcessWatchAllowance :one
SELECT coalesce(a.action_kind='watch' AND a.state IN ('accepted','applied')
 AND a.payload->>'pattern'=sqlc.arg(pattern)::text AND a.payload ? 'timeout_seconds',false)::boolean AS eligible,
 coalesce((a.payload->>'timeout_seconds')::int,0)::integer AS timeout_seconds,
 (SELECT count(*) FROM process_watch_reports r WHERE r.watch_action_id=a.id)::integer AS report_count,
 EXISTS(SELECT 1 FROM process_watch_reports r WHERE r.watch_action_id=a.id AND r.report_seq=sqlc.arg(seq)::bigint) AS replay
FROM process_actions a WHERE a.agent_id=sqlc.arg(agent_id) AND a.process_id=sqlc.arg(process_id) AND a.id=sqlc.arg(id);

-- name: RecordProcessWatchReport :exec
INSERT INTO process_watch_reports(watch_action_id,report_seq,agent_input_id,matched)
VALUES (sqlc.arg(watch_action_id),sqlc.arg(report_seq),sqlc.narg(agent_input_id),sqlc.arg(matched)::jsonb);

-- name: ReadPendingProcessWatchReport :one
SELECT i.id AS agent_input_id,r.matched FROM process_watch_reports r
JOIN agent_inputs i ON i.id=r.agent_input_id
WHERE r.watch_action_id=sqlc.arg(watch_action_id) AND i.agent_id=sqlc.arg(agent_id)
 AND i.state='received' AND i.input_kind='content'
ORDER BY r.report_seq DESC LIMIT 1;

-- name: CancelPendingExecutionInput :execrows
UPDATE agent_inputs SET state='canceled',canceled_at=statement_timestamp()
WHERE agent_id=sqlc.arg(agent_id) AND id=sqlc.arg(id) AND state='received' AND input_kind='content';

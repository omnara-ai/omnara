-- name: ReadExecutionProcessOwner :one
SELECT p.id,p.org_id,p.machine_id,p.tool_call_id,p.state,p.state_reason_code,p.state_reason_message,p.exit_code,
 p.exit_signal,p.execution_spec,p.execution_granted_at,p.source_started_at,p.source_ended_at,p.default_output_cursor,p.exit_notice_decided,
 EXISTS(SELECT 1 FROM process_actions a WHERE a.agent_id=p.agent_id AND a.process_id=p.id
 AND a.action_kind IN ('interrupt','terminate') AND (a.state IN ('queued','accepted')
 OR a.state='applied' AND (a.action_kind='terminate' OR a.updated_at>statement_timestamp()-interval '30 seconds'))) AS stop_requested,
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

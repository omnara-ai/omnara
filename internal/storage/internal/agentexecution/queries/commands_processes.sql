-- name: ReadExecutionProcessOwner :one
SELECT p.id,p.org_id,p.machine_id,p.tool_call_id,p.state,p.state_reason_code,p.state_reason_message,p.exit_code,
 p.exit_signal,p.execution_spec,p.execution_granted_at,p.source_started_at,p.source_ended_at,p.default_output_cursor
FROM processes p WHERE p.agent_id=sqlc.arg(agent_id) AND p.id=sqlc.arg(id) FOR UPDATE;

-- name: ReadExecutionActionOwner :one
SELECT a.id,a.process_id,a.tool_call_id,a.action_kind,a.seq,a.payload,a.state,a.state_reason_code,a.state_reason_message,
 EXISTS(SELECT 1 FROM process_actions earlier WHERE earlier.agent_id=a.agent_id AND earlier.process_id=a.process_id
 AND earlier.seq<a.seq AND earlier.state IN ('queued','accepted')) AS earlier_pending
FROM process_actions a WHERE a.agent_id=sqlc.arg(agent_id) AND a.id=sqlc.arg(id) FOR UPDATE;

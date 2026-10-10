-- name: PublishExecution :batchone
WITH scope AS MATERIALIZED (
 SELECT a.id AS agent_id,sqlc.narg(logical_ready_at)::timestamptz AS logical_ready_at
 FROM agents a WHERE a.id=sqlc.arg(agent_id) AND (sqlc.arg(insert_head)::boolean OR
 EXISTS(SELECT 1 FROM agent_execution_state h WHERE h.agent_id=a.id))
), updated AS (
INSERT INTO agent_execution_state AS h(agent_id,current_turn_id,stop_sequence,answered_through_sequence,max_normal_input_sequence,max_context_input_sequence,normal_context_id,compaction_context_id,pending_tool_output_id,pending_output_limit_id,pending_config_input_id,pending_checkpoint_id,turn_continuable,incomplete_tools,logical_ready_at)
SELECT scope.agent_id,sqlc.narg(current_turn_id),sqlc.arg(stop_sequence),sqlc.arg(answered_through_sequence),sqlc.arg(max_normal_input_sequence),sqlc.arg(max_context_input_sequence),sqlc.narg(normal_context_id),sqlc.narg(compaction_context_id),sqlc.narg(pending_tool_output_id),sqlc.narg(pending_output_limit_id),sqlc.narg(pending_config_input_id),sqlc.narg(pending_checkpoint_id),sqlc.arg(turn_continuable),sqlc.arg(incomplete_tools),scope.logical_ready_at FROM scope
ON CONFLICT(agent_id) DO UPDATE SET
current_turn_id=excluded.current_turn_id,
stop_sequence=excluded.stop_sequence,
answered_through_sequence=excluded.answered_through_sequence,
max_normal_input_sequence=excluded.max_normal_input_sequence,
max_context_input_sequence=excluded.max_context_input_sequence,
normal_context_id=excluded.normal_context_id,
compaction_context_id=excluded.compaction_context_id,
pending_tool_output_id=excluded.pending_tool_output_id,
pending_output_limit_id=excluded.pending_output_limit_id,
pending_config_input_id=excluded.pending_config_input_id,
pending_checkpoint_id=excluded.pending_checkpoint_id,
turn_continuable=excluded.turn_continuable,
incomplete_tools=excluded.incomplete_tools,
logical_ready_at=excluded.logical_ready_at
WHERE (h.current_turn_id,h.stop_sequence,h.answered_through_sequence,h.max_normal_input_sequence,h.max_context_input_sequence,h.normal_context_id,h.compaction_context_id,h.pending_tool_output_id,h.pending_output_limit_id,h.pending_config_input_id,h.pending_checkpoint_id,h.turn_continuable,h.incomplete_tools,h.logical_ready_at) IS DISTINCT FROM
(excluded.current_turn_id,excluded.stop_sequence,excluded.answered_through_sequence,excluded.max_normal_input_sequence,excluded.max_context_input_sequence,excluded.normal_context_id,excluded.compaction_context_id,excluded.pending_tool_output_id,excluded.pending_output_limit_id,excluded.pending_config_input_id,excluded.pending_checkpoint_id,excluded.turn_continuable,excluded.incomplete_tools,excluded.logical_ready_at)
RETURNING h.agent_id), desired AS MATERIALIZED (
 SELECT a.id,h.logical_ready_at AS ready_at FROM scope h JOIN agents a ON a.id=h.agent_id
 WHERE a.id=sqlc.arg(agent_id) AND a.state='active' AND h.logical_ready_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM agent_runtime_locks r WHERE r.agent_id=a.id)
), removed AS (
 DELETE FROM agent_wakeups WHERE agent_id=sqlc.arg(agent_id) AND NOT EXISTS(SELECT 1 FROM desired)
), published AS (
INSERT INTO agent_wakeups(agent_id,ready_at,updated_at)
SELECT id,ready_at,statement_timestamp() FROM desired
ON CONFLICT(agent_id) DO UPDATE SET ready_at=CASE
 WHEN agent_wakeups.ready_at<=statement_timestamp() AND excluded.ready_at<=statement_timestamp()
 THEN least(agent_wakeups.ready_at,excluded.ready_at) ELSE excluded.ready_at END,
 updated_at=statement_timestamp()
WHERE agent_wakeups.ready_at IS DISTINCT FROM CASE
 WHEN agent_wakeups.ready_at<=statement_timestamp() AND excluded.ready_at<=statement_timestamp()
 THEN least(agent_wakeups.ready_at,excluded.ready_at) ELSE excluded.ready_at END
RETURNING agent_id
) SELECT agent_id FROM scope;

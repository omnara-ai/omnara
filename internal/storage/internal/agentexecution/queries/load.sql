-- name: LoadExecutionBase :one
SELECT coalesce(h.agent_id,a.id)::uuid AS agent_id,h.current_turn_id,
       coalesce(h.stop_sequence,0)::bigint AS stop_sequence,
       coalesce(h.max_normal_input_sequence,0)::bigint AS max_normal_input_sequence,
       coalesce(h.max_context_input_sequence,0)::bigint AS max_context_input_sequence,
       h.normal_context_id,h.compaction_context_id,h.pending_tool_output_id,h.pending_output_limit_id,
       h.pending_config_input_id,h.pending_checkpoint_id,coalesce(h.turn_continuable,false)::boolean AS turn_continuable,
       coalesce(h.incomplete_tools,false)::boolean AS incomplete_tools,h.logical_ready_at,
       (h.agent_id IS NOT NULL)::boolean AS head_exists,a.root_agent_id,a.state,statement_timestamp()::timestamptz AS database_now
FROM agents a LEFT JOIN agent_execution_state h ON h.agent_id=a.id
WHERE a.project_id=sqlc.arg(project_id) AND a.id=sqlc.arg(agent_id);

-- name: LoadExecutionFacts :batchone
WITH batch AS (
SELECT EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.narg(tool_output_id)) AS has_tools,
EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.narg(tool_output_id) AND c.state<>'completed') AS incomplete,
EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.narg(tool_output_id)
AND (c.state='awaiting_authorization' OR (c.state='ready' AND c.type IN ('built_in','mcp')))) AS runnable FROM (SELECT 1 WHERE sqlc.narg(tool_output_id)::uuid IS NOT NULL) present
), tool_batch AS (
SELECT b.has_tools,b.incomplete,b.runnable,coalesce(r.calls,0)::bigint AS calls,coalesce(r.results,0)::bigint AS results,
coalesce(r.events,0)::bigint AS events,coalesce(r.last_sequence,0)::bigint AS last_sequence,
coalesce(r.ready_at,'epoch'::timestamptz)::timestamptz AS ready_at
FROM batch b LEFT JOIN LATERAL (
SELECT count(c.id)::bigint AS calls,count(r.id)::bigint AS results,count(e.id)::bigint AS events,
max(e.sequence)::bigint AS last_sequence,max(r.completed_at)::timestamptz AS ready_at
FROM tool_calls c LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
LEFT JOIN agent_events e ON e.agent_id=r.agent_id AND e.tool_call_result_id=r.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.narg(tool_output_id) AND NOT b.incomplete
) r ON NOT b.incomplete
), scope AS MATERIALIZED (
 SELECT a.id,a.root_agent_id,a.state,statement_timestamp()::timestamptz AS database_now,t.id AS turn_id,
 coalesce(e.sequence,0)::bigint AS semantic_sequence,coalesce(e.created_at,'epoch'::timestamptz)::timestamptz AS semantic_time,
 EXISTS(SELECT 1 FROM agent_inputs i WHERE i.agent_id=a.id AND i.input_kind='content'
 AND i.delivery_mode='steering' AND i.state='received') AS steering,
 EXISTS(SELECT 1 FROM agent_inputs i WHERE i.agent_id=a.id AND i.input_kind='content'
 AND i.delivery_mode='queued' AND i.state='received') AS queued,
 coalesce(o.first_opening_sequence,0)::bigint AS first_opening_sequence,
 coalesce(o.last_opening_sequence,0)::bigint AS last_opening_sequence,
 coalesce(o.first_content_sequence,0)::bigint AS first_content_sequence
 FROM agents a LEFT JOIN agent_turns t ON t.agent_id=a.id AND t.id=sqlc.narg(turn_id)
 LEFT JOIN agent_events e ON e.agent_id=a.id AND e.id=t.latest_semantic_event_id
 LEFT JOIN LATERAL (
  SELECT min(x.sequence) AS first_opening_sequence,max(x.sequence) AS last_opening_sequence,
   min(x.sequence) FILTER (WHERE i.input_kind='content') AS first_content_sequence
  FROM agent_events x JOIN agent_inputs i ON i.agent_id=x.agent_id AND i.id=x.agent_input_id
  WHERE x.agent_id=a.id AND x.turn_id=t.id AND x.is_opening_event
 ) o ON true
 WHERE a.id=sqlc.arg(agent_id) AND a.project_id=sqlc.arg(project_id)
) , boundary AS MATERIALIZED (
    SELECT greatest(coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=sqlc.arg(agent_id)
                    AND e.event_kind='model_output' AND e.sequence<=(SELECT last_opening_sequence FROM scope)
                    ORDER BY e.sequence DESC LIMIT 1),0),sqlc.arg(stop_sequence)::bigint)::bigint AS sequence
    FROM scope WHERE sqlc.arg(max_context_input_sequence)::bigint<scope.first_opening_sequence
 AND sqlc.arg(stop_sequence)::bigint<=scope.first_opening_sequence
), unanswered AS MATERIALIZED (
    SELECT i.id,e.sequence,e.created_at FROM boundary b
    JOIN agent_events e ON e.agent_id=sqlc.arg(agent_id) AND e.is_opening_event
        AND e.sequence>b.sequence AND e.sequence<=(SELECT last_opening_sequence FROM scope)
    JOIN agent_inputs i ON i.agent_id=e.agent_id AND i.id=e.agent_input_id
        AND i.input_kind='content' AND i.state='resolved' AND i.admitted_event_id=e.id
), opening AS (
    SELECT id,sequence,created_at FROM unanswered
    UNION ALL
    SELECT i.id,e.sequence,e.created_at FROM boundary b
    JOIN agent_events e ON e.agent_id=sqlc.arg(agent_id) AND e.turn_id=sqlc.narg(turn_id) AND e.is_opening_event
        AND e.sequence<=(SELECT last_opening_sequence FROM scope)
    JOIN agent_inputs i ON i.agent_id=e.agent_id AND i.id=e.agent_input_id
        AND i.input_kind='content' AND i.state='resolved' AND i.admitted_event_id=e.id
    WHERE NOT EXISTS(SELECT 1 FROM unanswered)
)
SELECT s.id AS agent_id,s.root_agent_id,s.state,s.database_now,s.turn_id,
s.semantic_sequence,s.semantic_time,s.steering,s.queued,s.first_opening_sequence,s.last_opening_sequence,s.first_content_sequence,
coalesce(o.ids,'{}'::uuid[])::uuid[] AS opening_ids,coalesce(o.first_sequence,0)::bigint AS opening_sequence,
coalesce(o.ready_at,'epoch'::timestamptz)::timestamptz AS opening_ready_at,
coalesce(b.has_tools,false)::boolean AS batch_has_tools, coalesce(b.incomplete,false)::boolean AS batch_incomplete,
coalesce(b.runnable,false)::boolean AS batch_runnable,coalesce(b.calls,0)::bigint AS batch_calls,
coalesce(b.results,0)::bigint AS batch_results,coalesce(b.events,0)::bigint AS batch_events,
coalesce(b.last_sequence,0)::bigint AS batch_last_sequence,coalesce(b.ready_at,'epoch'::timestamptz)::timestamptz AS batch_ready_at
FROM scope s LEFT JOIN LATERAL (
SELECT array_agg(id ORDER BY sequence) AS ids,min(sequence) AS first_sequence,min(created_at) AS ready_at FROM opening
) o ON true LEFT JOIN tool_batch b ON true;

-- name: LoadExecutionContexts :batchmany
WITH outputs AS (
SELECT o.id,o.model_call_context_id,o.stop_reason,e.id AS event_id,e.sequence,e.created_at
FROM model_outputs o JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
WHERE o.agent_id=sqlc.arg(agent_id) AND o.id IN(sqlc.narg(tool_output_id)::uuid,sqlc.narg(limit_output_id)::uuid)
)
SELECT c.id,c.agent_id,c.turn_id,c.operation_kind,c.attempt_number,c.input_event_sequence,c.source_event_sequence_end,
c.state,c.recovery_kind,c.retry_at,c.opening_input_ids,c.opening_event_sequence,
o.id AS output_id,o.stop_reason AS output_stop_reason,o.event_id AS output_event_id,
o.sequence AS output_sequence,o.created_at AS output_created_at
FROM model_call_contexts c LEFT JOIN outputs o ON o.model_call_context_id=c.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=ANY(
ARRAY[sqlc.narg(normal_context_id)::uuid,sqlc.narg(compaction_context_id)::uuid]
|| ARRAY(SELECT model_call_context_id FROM outputs));

-- name: LoadExecutionPendingConfig :batchmany
SELECT i.id,e.turn_id,e.sequence,e.created_at,i.opening_input_ids,coalesce(i.opening_event_sequence,0)::bigint AS opening_event_sequence
FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
WHERE i.agent_id=sqlc.arg(agent_id) AND i.id=sqlc.arg(id) AND i.input_kind='config_change' AND i.state='resolved';

-- name: LoadExecutionPendingCheckpoint :batchmany
SELECT c.id,e.turn_id,e.sequence,c.created_at,c.opening_input_ids,coalesce(c.opening_event_sequence,0)::bigint AS opening_event_sequence
FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.arg(id);

-- name: ExecutionDatabaseTime :one
SELECT statement_timestamp()::timestamptz;

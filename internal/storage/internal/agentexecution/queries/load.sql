-- name: LoadExecutionBase :one
SELECT coalesce(h.agent_id,a.id)::uuid AS agent_id,h.current_turn_id,
       coalesce(h.stop_sequence,0)::bigint AS stop_sequence,
       coalesce(h.answered_through_sequence,0)::bigint AS answered_through_sequence,
       coalesce(h.max_normal_input_sequence,0)::bigint AS max_normal_input_sequence,
       coalesce(h.max_context_input_sequence,0)::bigint AS max_context_input_sequence,
       h.normal_context_id,h.compaction_context_id,h.pending_tool_output_id,h.pending_output_limit_id,
       h.pending_config_input_id,h.pending_checkpoint_id,coalesce(h.turn_continuable,false)::boolean AS turn_continuable,
       coalesce(h.incomplete_tools,false)::boolean AS incomplete_tools,h.logical_ready_at,
       (h.agent_id IS NOT NULL)::boolean AS head_exists,a.root_agent_id,a.state,statement_timestamp()::timestamptz AS database_now
FROM agents a LEFT JOIN agent_execution_state h ON h.agent_id=a.id
WHERE a.project_id=sqlc.arg(project_id) AND a.id=sqlc.arg(agent_id);

-- name: LoadExecutionFacts :batchmany
WITH scope AS MATERIALIZED (
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
), outputs AS MATERIALIZED (
    SELECT o.id,o.model_call_context_id,o.stop_reason,e.id AS event_id,e.sequence,e.created_at
    FROM model_outputs o JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
    WHERE o.agent_id=sqlc.arg(agent_id) AND o.id IN (sqlc.narg(tool_output_id)::uuid,sqlc.narg(limit_output_id)::uuid)
), batch AS MATERIALIZED (
    SELECT EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id)
                     AND c.model_output_id=sqlc.narg(tool_output_id)) AS has_tools,
           EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id)
                     AND c.model_output_id=sqlc.narg(tool_output_id) AND c.state<>'completed') AS incomplete,
           EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id)
                     AND c.model_output_id=sqlc.narg(tool_output_id) AND
                     (c.state='awaiting_authorization' OR (c.state='ready' AND c.type IN ('built_in','mcp')))) AS runnable
    WHERE sqlc.narg(tool_output_id)::uuid IS NOT NULL
), boundary AS MATERIALIZED (
    SELECT greatest(coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=sqlc.arg(agent_id)
                    AND e.event_kind='model_output' AND e.sequence<=(SELECT last_opening_sequence FROM scope)
                    ORDER BY e.sequence DESC LIMIT 1),0),sqlc.arg(stop_sequence)::bigint)::bigint AS sequence
    FROM scope WHERE scope.state='active' AND sqlc.arg(max_context_input_sequence)::bigint<scope.first_opening_sequence
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
), facts AS (
SELECT 'context'::text AS kind,
       c.id AS id,
       c.turn_id AS turn_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS context_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS event_id,
       0::bigint AS sequence,
       'epoch'::timestamptz AS event_time,
       c.operation_kind AS operation,
       c.attempt_number AS attempt,
       c.input_event_sequence AS watermark,
       coalesce(c.source_event_sequence_end,0)::bigint AS source_end,
       c.state AS state,
       coalesce(c.recovery_kind,'')::text AS recovery,
       c.retry_at AS retry_at,
       c.opening_input_ids AS opening_ids,
       c.opening_event_sequence AS opening_sequence,
       false AS has_tools,
       false AS incomplete,
       false AS runnable,
       0::bigint AS calls,
       0::bigint AS results,
       0::bigint AS events,
       ''::text AS stop_reason,
       '00000000-0000-0000-0000-000000000000'::uuid AS root_agent_id,
       'epoch'::timestamptz AS database_now,
       false AS steering,false AS queued,
       0::bigint AS first_opening_sequence,0::bigint AS last_opening_sequence,0::bigint AS first_content_sequence
FROM model_call_contexts c WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=ANY(
    ARRAY[sqlc.narg(normal_context_id)::uuid,sqlc.narg(compaction_context_id)::uuid]
    || ARRAY(SELECT model_call_context_id FROM outputs))
UNION ALL
SELECT 'output'::text AS kind,
       o.id AS id,
       '00000000-0000-0000-0000-000000000000'::uuid AS turn_id,
       o.model_call_context_id AS context_id,
       o.event_id AS event_id,
       o.sequence AS sequence,
       o.created_at AS event_time,
       ''::text AS operation,
       0::integer AS attempt,
       0::bigint AS watermark,
       0::bigint AS source_end,
       ''::text AS state,
       ''::text AS recovery,
       NULL::timestamptz AS retry_at,
       '{}'::uuid[] AS opening_ids,
       0::bigint AS opening_sequence,
       false AS has_tools,
       false AS incomplete,
       false AS runnable,
       0::bigint AS calls,
       0::bigint AS results,
       0::bigint AS events,
       o.stop_reason AS stop_reason,
       '00000000-0000-0000-0000-000000000000'::uuid AS root_agent_id,
       'epoch'::timestamptz AS database_now,
       false AS steering,false AS queued,
       0::bigint AS first_opening_sequence,0::bigint AS last_opening_sequence,0::bigint AS first_content_sequence
FROM outputs o
UNION ALL
SELECT 'batch'::text AS kind,
       '00000000-0000-0000-0000-000000000000'::uuid AS id,
       '00000000-0000-0000-0000-000000000000'::uuid AS turn_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS context_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS event_id,
       coalesce(r.last_sequence,0)::bigint AS sequence,
       coalesce(r.ready_at,'epoch'::timestamptz)::timestamptz AS event_time,
       ''::text AS operation,
       0::integer AS attempt,
       0::bigint AS watermark,
       0::bigint AS source_end,
       ''::text AS state,
       ''::text AS recovery,
       NULL::timestamptz AS retry_at,
       '{}'::uuid[] AS opening_ids,
       0::bigint AS opening_sequence,
       b.has_tools AS has_tools,
       b.incomplete AS incomplete,
       b.runnable AS runnable,
       coalesce(r.calls,0)::bigint AS calls,
       coalesce(r.results,0)::bigint AS results,
       coalesce(r.events,0)::bigint AS events,
       ''::text AS stop_reason,
       '00000000-0000-0000-0000-000000000000'::uuid AS root_agent_id,
       'epoch'::timestamptz AS database_now,
       false AS steering,false AS queued,
       0::bigint AS first_opening_sequence,0::bigint AS last_opening_sequence,0::bigint AS first_content_sequence
FROM batch b LEFT JOIN LATERAL (
    SELECT count(c.id)::bigint AS calls,count(r.id)::bigint AS results,count(e.id)::bigint AS events,
           max(e.sequence)::bigint AS last_sequence,max(r.completed_at)::timestamptz AS ready_at
    FROM tool_calls c LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
    LEFT JOIN agent_events e ON e.agent_id=r.agent_id AND e.tool_call_result_id=r.id
    WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.narg(tool_output_id) AND NOT b.incomplete
) r ON NOT b.incomplete
UNION ALL
SELECT 'config'::text AS kind,
       i.id AS id,
       e.turn_id AS turn_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS context_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS event_id,
       e.sequence AS sequence,
       e.created_at AS event_time,
       ''::text AS operation,
       0::integer AS attempt,
       0::bigint AS watermark,
       0::bigint AS source_end,
       ''::text AS state,
       ''::text AS recovery,
       NULL::timestamptz AS retry_at,
       i.opening_input_ids AS opening_ids,
       coalesce(i.opening_event_sequence,0)::bigint AS opening_sequence,
       false AS has_tools,
       false AS incomplete,
       false AS runnable,
       0::bigint AS calls,
       0::bigint AS results,
       0::bigint AS events,
       ''::text AS stop_reason,
       '00000000-0000-0000-0000-000000000000'::uuid AS root_agent_id,
       'epoch'::timestamptz AS database_now,
       false AS steering,false AS queued,
       0::bigint AS first_opening_sequence,0::bigint AS last_opening_sequence,0::bigint AS first_content_sequence
FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
WHERE i.agent_id=sqlc.arg(agent_id) AND i.id=sqlc.narg(config_id) AND i.input_kind='config_change' AND i.state='resolved'
UNION ALL
SELECT 'checkpoint'::text AS kind,
       c.id AS id,
       e.turn_id AS turn_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS context_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS event_id,
       e.sequence AS sequence,
       c.created_at AS event_time,
       ''::text AS operation,
       0::integer AS attempt,
       0::bigint AS watermark,
       0::bigint AS source_end,
       ''::text AS state,
       ''::text AS recovery,
       NULL::timestamptz AS retry_at,
       c.opening_input_ids AS opening_ids,
       coalesce(c.opening_event_sequence,0)::bigint AS opening_sequence,
       false AS has_tools,
       false AS incomplete,
       false AS runnable,
       0::bigint AS calls,
       0::bigint AS results,
       0::bigint AS events,
       ''::text AS stop_reason,
       '00000000-0000-0000-0000-000000000000'::uuid AS root_agent_id,
       'epoch'::timestamptz AS database_now,
       false AS steering,false AS queued,
       0::bigint AS first_opening_sequence,0::bigint AS last_opening_sequence,0::bigint AS first_content_sequence
FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.id=sqlc.narg(checkpoint_id)
UNION ALL
SELECT 'opening'::text AS kind,
       o.id AS id,
       '00000000-0000-0000-0000-000000000000'::uuid AS turn_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS context_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS event_id,
       o.sequence AS sequence,
       o.created_at AS event_time,
       ''::text AS operation,
       0::integer AS attempt,
       0::bigint AS watermark,
       0::bigint AS source_end,
       ''::text AS state,
       ''::text AS recovery,
       NULL::timestamptz AS retry_at,
       '{}'::uuid[] AS opening_ids,
       0::bigint AS opening_sequence,
       false AS has_tools,
       false AS incomplete,
       false AS runnable,
       0::bigint AS calls,
       0::bigint AS results,
       0::bigint AS events,
       ''::text AS stop_reason,
       '00000000-0000-0000-0000-000000000000'::uuid AS root_agent_id,
       'epoch'::timestamptz AS database_now,
       false AS steering,false AS queued,
       0::bigint AS first_opening_sequence,0::bigint AS last_opening_sequence,0::bigint AS first_content_sequence
FROM opening o
UNION ALL
SELECT 'scope'::text AS kind,s.id,coalesce(s.turn_id,'00000000-0000-0000-0000-000000000000'::uuid),
 '00000000-0000-0000-0000-000000000000'::uuid,'00000000-0000-0000-0000-000000000000'::uuid,
 s.semantic_sequence,s.semantic_time,''::text,0::integer,0::bigint,0::bigint,s.state,''::text,NULL::timestamptz,
 '{}'::uuid[],0::bigint,false,false,false,0::bigint,0::bigint,0::bigint,''::text,
 s.root_agent_id,s.database_now,s.steering,s.queued,s.first_opening_sequence,s.last_opening_sequence,s.first_content_sequence
FROM scope s
 )
SELECT f.kind,f.id,f.turn_id,f.context_id,f.event_id,f.sequence,f.event_time,f.operation,f.attempt,
 f.watermark,f.source_end,f.state,f.recovery,f.retry_at,f.opening_ids,f.opening_sequence,
 f.has_tools,f.incomplete,f.runnable,f.calls,f.results,f.events,f.stop_reason,f.root_agent_id,
 f.database_now,f.steering,f.queued,f.first_opening_sequence,f.last_opening_sequence,f.first_content_sequence
FROM facts f ORDER BY f.kind,f.sequence;

-- name: ExecutionDatabaseTime :one
SELECT statement_timestamp()::timestamptz;

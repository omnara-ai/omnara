-- name: LoadExecutionBoundaries :one
SELECT coalesce((SELECT max(e.sequence)
 FROM (
  SELECT i.admitted_event_id FROM agent_inputs i
  WHERE i.project_id=sqlc.arg(project_id) AND i.agent_id=sqlc.arg(agent_id)
    AND i.input_kind='control' AND i.state='resolved' AND i.control_type='cancel_current'
  ORDER BY i.admitted_event_id
 ) i
 CROSS JOIN LATERAL (
  SELECT e.sequence FROM agent_events e
  WHERE e.id=i.admitted_event_id AND e.agent_id=sqlc.arg(agent_id)
  LIMIT 1
 ) e),0)::bigint AS stop_sequence,
       coalesce((SELECT t.id FROM agent_turns t WHERE t.agent_id=sqlc.arg(agent_id) ORDER BY t.turn_sequence DESC LIMIT 1),
                 '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS turn_id,
coalesce((SELECT c.input_event_sequence FROM model_call_contexts c
WHERE c.project_id=sqlc.arg(project_id) AND c.agent_id=sqlc.arg(agent_id)
ORDER BY c.input_event_sequence DESC LIMIT 1),0)::bigint AS max_context_input_sequence,
coalesce((SELECT c.input_event_sequence FROM model_call_contexts c
WHERE c.project_id=sqlc.arg(project_id) AND c.agent_id=sqlc.arg(agent_id) AND c.operation_kind='normal'
ORDER BY c.input_event_sequence DESC LIMIT 1),0)::bigint AS max_normal_input_sequence;

-- name: ReconstructExecutionOutputs :many
SELECT o.id,o.model_call_context_id,o.stop_reason,e.id AS event_id,e.sequence,e.created_at
FROM model_outputs o JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
WHERE o.agent_id=sqlc.arg(agent_id) AND e.turn_id=sqlc.arg(turn_id) AND e.sequence>=sqlc.arg(stop_sequence)
ORDER BY e.sequence;

-- name: LoadExecutionTurn :one
SELECT t.id,semantic.sequence AS semantic_sequence,semantic.created_at AS semantic_time,
       coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND
 e.is_opening_event
        ORDER BY e.sequence LIMIT 1),0)::bigint AS first_opening_sequence,
       coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND
 e.is_opening_event
        ORDER BY e.sequence DESC LIMIT 1),0)::bigint AS last_opening_sequence,
       coalesce((SELECT e.sequence FROM agent_events e JOIN agent_inputs i ON i.agent_id=e.agent_id AND
 i.id=e.agent_input_id
                 WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND e.is_opening_event AND i.input_kind='content'
                 ORDER BY e.sequence LIMIT 1),0)::bigint AS first_content_sequence
FROM agent_turns t JOIN agent_events semantic ON semantic.agent_id=t.agent_id AND
 semantic.id=t.latest_semantic_event_id
WHERE t.agent_id=sqlc.arg(agent_id) AND t.id=sqlc.arg(id);

-- name: ReconstructExecutionCheckpoint :one
SELECT c.id FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
WHERE c.agent_id=sqlc.arg(agent_id) AND e.turn_id=sqlc.arg(turn_id) AND e.sequence>sqlc.arg(normal_watermark) AND e.sequence>=sqlc.arg(stop_sequence)
ORDER BY e.sequence DESC LIMIT 1;

-- name: ReconstructExecutionContexts :many
SELECT c.id,c.agent_id,c.turn_id,c.operation_kind,c.attempt_number,c.input_event_sequence,
       c.source_event_sequence_end,c.state,c.recovery_kind,c.retry_at,c.opening_input_ids,c.opening_event_sequence
FROM model_call_contexts c WHERE c.agent_id=sqlc.arg(agent_id) AND c.turn_id=sqlc.arg(turn_id)
ORDER BY c.input_event_sequence,c.attempt_number;

-- name: ReconstructExecutionConfig :one
SELECT i.id FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
WHERE i.agent_id=sqlc.arg(agent_id) AND e.turn_id=sqlc.arg(turn_id) AND i.input_kind='config_change' AND i.state='resolved'
  AND e.sequence>sqlc.arg(normal_watermark) AND e.sequence>=sqlc.arg(stop_sequence)
  AND e.sequence>=sqlc.arg(first_content_sequence) AND sqlc.arg(first_content_sequence)::bigint>0
ORDER BY e.sequence DESC LIMIT 1;

-- name: LoadExecutionBatch :one
SELECT EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.arg(model_output_id)) AS has_tools,
       EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.arg(model_output_id) AND c.state<>'completed') AS
 incomplete,
       EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.arg(model_output_id) AND
              (c.state='awaiting_authorization' OR (c.state='ready' AND c.type IN ('built_in','mcp')))) AS runnable;

-- name: LoadExecutionBatchCompletion :one
SELECT count(c.id)::bigint AS calls,count(r.id)::bigint AS results,count(e.id)::bigint AS events,
       coalesce(max(e.sequence),0)::bigint AS last_result_sequence,
       coalesce(max(r.completed_at),'epoch'::timestamptz)::timestamptz AS ready_at
FROM tool_calls c LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
LEFT JOIN agent_events e ON e.agent_id=r.agent_id AND e.tool_call_result_id=r.id
WHERE c.agent_id=sqlc.arg(agent_id) AND c.model_output_id=sqlc.arg(model_output_id);

-- name: ReconstructExecutionContexts :many
SELECT c.id,c.agent_id,c.turn_id,c.operation_kind,c.attempt_number,c.input_event_sequence,
       c.source_event_sequence_end,c.state,c.recovery_kind,c.retry_at,c.opening_input_ids,c.opening_event_sequence
FROM model_call_contexts c WHERE c.agent_id=$1
ORDER BY c.input_event_sequence,c.attempt_number;

-- name: ValidateExecutionLineage :one
WITH snapshots AS (
    SELECT c.opening_input_ids,c.opening_event_sequence,c.input_event_sequence AS watermark
    FROM model_call_contexts c WHERE c.agent_id=$1
    UNION ALL
    SELECT i.opening_input_ids,i.opening_event_sequence,e.sequence
    FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
    WHERE i.agent_id=$1 AND i.input_kind='config_change' AND i.state='resolved'
    UNION ALL
    SELECT c.opening_input_ids,c.opening_event_sequence,e.sequence
    FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
    WHERE c.agent_id=$1
)
SELECT coalesce(NOT EXISTS (
    SELECT 1 FROM model_call_contexts c WHERE c.agent_id=$1
      AND c.turn_id IS DISTINCT FROM (SELECT e.turn_id FROM agent_events e
          WHERE e.agent_id=c.agent_id AND e.is_opening_event AND e.sequence<=c.input_event_sequence
          ORDER BY e.sequence DESC LIMIT 1)
) AND NOT EXISTS (
    SELECT 1 FROM snapshots s WHERE EXISTS (
        SELECT 1 FROM (
            SELECT o.ordinality,e.sequence,lag(e.sequence) OVER (ORDER BY o.ordinality) AS previous
            FROM unnest(s.opening_input_ids) WITH ORDINALITY o(id,ordinality)
            LEFT JOIN agent_inputs i ON i.agent_id=$1 AND i.id=o.id AND i.state='resolved' AND i.input_kind='content'
            LEFT JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id AND e.is_opening_event
        ) opening WHERE sequence IS NULL OR sequence>s.watermark
            OR previous>=sequence OR (ordinality=1 AND sequence<>s.opening_event_sequence)
    )
),false)::boolean AS valid;

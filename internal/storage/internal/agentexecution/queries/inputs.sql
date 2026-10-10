-- name: CaptureExecutionOpening :many
WITH boundary AS MATERIALIZED (
    SELECT greatest(
        coalesce((SELECT output.sequence FROM agent_events output WHERE output.agent_id=sqlc.arg(agent_id)
                  AND output.event_kind='model_output' AND output.sequence<=sqlc.arg(watermark)
                  ORDER BY output.sequence DESC LIMIT 1),0),
        sqlc.arg(stop_sequence)::bigint
    )::bigint AS sequence
), unanswered AS MATERIALIZED (
    SELECT i.id,e.sequence,e.created_at FROM boundary b
    JOIN agent_events e ON e.agent_id=sqlc.arg(agent_id) AND e.is_opening_event
        AND e.sequence>b.sequence AND e.sequence<=sqlc.arg(watermark)
    JOIN agent_inputs i ON i.project_id=sqlc.arg(project_id) AND i.agent_id=e.agent_id AND i.id=e.agent_input_id
        AND i.input_kind='content' AND i.state='resolved' AND i.admitted_event_id=e.id
)
SELECT id,sequence,created_at FROM unanswered
UNION ALL
SELECT i.id,e.sequence,e.created_at FROM agent_events e
JOIN agent_inputs i ON i.project_id=sqlc.arg(project_id) AND i.agent_id=e.agent_id AND i.id=e.agent_input_id
    AND i.input_kind='content' AND i.state='resolved' AND i.admitted_event_id=e.id
WHERE e.agent_id=sqlc.arg(agent_id) AND e.turn_id=sqlc.arg(turn_id) AND e.is_opening_event
  AND e.sequence<=sqlc.arg(watermark) AND NOT EXISTS(SELECT 1 FROM unanswered)
ORDER BY sequence;

-- name: ReconstructExecutionConfig :one
SELECT i.id FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
WHERE i.agent_id=$1 AND e.turn_id=$2 AND i.input_kind='config_change' AND i.state='resolved'
  AND e.sequence>sqlc.arg(normal_watermark) AND e.sequence>=sqlc.arg(stop_sequence)
  AND e.sequence>=sqlc.arg(first_content_sequence) AND sqlc.arg(first_content_sequence)::bigint>0
ORDER BY e.sequence DESC LIMIT 1;

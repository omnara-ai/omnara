-- name: LoadExecutionTurn :one
SELECT t.id,semantic.sequence AS semantic_sequence,semantic.created_at AS semantic_time,
       coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND e.is_opening_event
        ORDER BY e.sequence LIMIT 1),0)::bigint AS first_opening_sequence,
       coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND e.is_opening_event
        ORDER BY e.sequence DESC LIMIT 1),0)::bigint AS last_opening_sequence,
       coalesce((SELECT e.sequence FROM agent_events e JOIN agent_inputs i ON i.agent_id=e.agent_id AND i.id=e.agent_input_id
                 WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND e.is_opening_event AND i.input_kind='content'
                 ORDER BY e.sequence LIMIT 1),0)::bigint AS first_content_sequence
FROM agent_turns t JOIN agent_events semantic ON semantic.agent_id=t.agent_id AND semantic.id=t.latest_semantic_event_id
WHERE t.agent_id=$1 AND t.id=$2;

-- name: ReconstructExecutionCheckpoint :one
SELECT c.id FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
WHERE c.agent_id=$1 AND e.turn_id=$2 AND e.sequence>sqlc.arg(normal_watermark) AND e.sequence>=sqlc.arg(stop_sequence)
ORDER BY e.sequence DESC LIMIT 1;

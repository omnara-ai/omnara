-- name: GetContextCheckpoint :one
SELECT checkpoint.id, agent.project_id,
  checkpoint.agent_id, checkpoint.summarized_through_event_sequence,
  checkpoint.producer_model_call_context_id,
  event.id AS checkpoint_event_id,
  checkpoint.summary,
  checkpoint.created_at, event.sequence AS checkpoint_event_sequence
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
JOIN agent_events event ON event.agent_id = checkpoint.agent_id
  AND event.context_checkpoint_id = checkpoint.id
  AND event.event_kind = 'context_checkpoint'
WHERE agent.project_id = sqlc.arg(project_id)
  AND checkpoint.agent_id = sqlc.arg(agent_id)
  AND checkpoint.id = sqlc.arg(id);

-- name: GetLatestApplicableContextCheckpoint :one
SELECT checkpoint.id, agent.project_id,
  checkpoint.agent_id, checkpoint.summarized_through_event_sequence,
  checkpoint.producer_model_call_context_id,
  event.id AS checkpoint_event_id,
  checkpoint.summary,
  checkpoint.created_at, event.sequence AS checkpoint_event_sequence
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
JOIN agent_events event ON event.agent_id = checkpoint.agent_id
  AND event.context_checkpoint_id = checkpoint.id
  AND event.event_kind = 'context_checkpoint'
WHERE agent.project_id = sqlc.arg(project_id)
  AND checkpoint.agent_id = sqlc.arg(agent_id)
  AND event.sequence <= sqlc.arg(max_event_sequence)
ORDER BY event.sequence DESC, checkpoint.created_at DESC, checkpoint.id DESC
LIMIT 1;

-- name: CountConsecutiveContextCheckpointLineage :one
WITH RECURSIVE lineage AS (
  SELECT producer.input_event_sequence AS prior_frontier
  FROM agent_events checkpoint_event
  JOIN agents agent ON agent.id = checkpoint_event.agent_id
  JOIN context_checkpoints checkpoint ON checkpoint.agent_id = checkpoint_event.agent_id
    AND checkpoint.id = checkpoint_event.context_checkpoint_id
  JOIN model_call_contexts producer ON producer.agent_id = checkpoint.agent_id
    AND producer.id = checkpoint.producer_model_call_context_id
  WHERE agent.project_id = sqlc.arg(project_id)
    AND checkpoint_event.agent_id = sqlc.arg(agent_id)
    AND checkpoint_event.event_kind = 'context_checkpoint'
    AND checkpoint_event.sequence = sqlc.arg(input_event_sequence)
    AND checkpoint_event.sequence = producer.input_event_sequence + 1
  UNION ALL
  SELECT producer.input_event_sequence
  FROM lineage
  JOIN agent_events checkpoint_event ON checkpoint_event.agent_id = sqlc.arg(agent_id)
    AND checkpoint_event.event_kind = 'context_checkpoint'
    AND checkpoint_event.sequence = lineage.prior_frontier
  JOIN context_checkpoints checkpoint ON checkpoint.agent_id = checkpoint_event.agent_id
    AND checkpoint.id = checkpoint_event.context_checkpoint_id
  JOIN model_call_contexts producer ON producer.project_id = sqlc.arg(project_id)
    AND producer.agent_id = checkpoint.agent_id
    AND producer.id = checkpoint.producer_model_call_context_id
  WHERE checkpoint_event.sequence = producer.input_event_sequence + 1
)
SELECT count(*)::bigint AS count
FROM lineage;

-- name: GetContextCheckpointByProducerContext :one
SELECT checkpoint.id, agent.project_id,
  checkpoint.agent_id, checkpoint.summarized_through_event_sequence,
  checkpoint.producer_model_call_context_id,
  event.id AS checkpoint_event_id,
  checkpoint.summary,
  checkpoint.created_at, event.sequence AS checkpoint_event_sequence
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
JOIN agent_events event ON event.agent_id = checkpoint.agent_id
  AND event.context_checkpoint_id = checkpoint.id
  AND event.event_kind = 'context_checkpoint'
WHERE agent.project_id = sqlc.arg(project_id)
  AND checkpoint.agent_id = sqlc.arg(agent_id)
  AND checkpoint.producer_model_call_context_id = sqlc.arg(producer_model_call_context_id);

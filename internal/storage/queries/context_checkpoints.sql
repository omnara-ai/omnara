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

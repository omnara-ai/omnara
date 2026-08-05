-- name: CountCheckpointRangeEvents :one
SELECT count(*)::bigint AS count
FROM agent_events
JOIN agents agent ON agent.id = agent_events.agent_id
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent_events.agent_id = sqlc.arg(agent_id)
  AND sequence BETWEEN sqlc.arg(start_sequence) AND sqlc.arg(end_sequence);

-- name: CountCheckpointRangeOpenToolResults :one
SELECT count(*)::bigint AS count
FROM tool_call_read_projection tool_call
LEFT JOIN tool_call_results result ON result.agent_id = tool_call.agent_id
  AND result.tool_call_id = tool_call.id
LEFT JOIN agent_events result_event ON result_event.agent_id = result.agent_id
  AND result_event.tool_call_result_id = result.id
  AND result_event.event_kind = 'tool_result'
WHERE tool_call.project_id = sqlc.arg(project_id)
  AND tool_call.agent_id = sqlc.arg(agent_id)
  AND tool_call.source_event_sequence BETWEEN sqlc.arg(start_sequence) AND sqlc.arg(end_sequence)
  AND (
    result_event.id IS NULL
    OR result_event.sequence > sqlc.arg(end_sequence)
  );

-- name: InsertContextCheckpoint :one
INSERT INTO context_checkpoints(
  id, agent_id, summarized_through_event_sequence,
  producer_model_call_context_id, summary, created_at
)
SELECT sqlc.arg(id), agent.id,
       context.source_event_sequence_end,
       context.id,
       sqlc.arg(summary),
       statement_timestamp()
FROM agents agent
JOIN model_call_contexts context ON context.project_id = agent.project_id
  AND context.agent_id = agent.id
  AND context.id = sqlc.arg(producer_model_call_context_id)
  AND context.operation_kind = 'compaction'
  AND context.state = 'started'
JOIN agent_runtime_locks runtime_lock ON runtime_lock.agent_id = context.agent_id
  AND runtime_lock.id = context.runtime_lock_id
  AND runtime_lock.cancel_requested_at IS NULL
  AND runtime_lock.lease_expires_at > statement_timestamp()
WHERE agent.project_id = sqlc.arg(project_id)
  AND agent.id = sqlc.arg(agent_id)
RETURNING id;

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

-- name: GetModelCallContext :one
SELECT context.id, context.org_id, context.project_id, context.agent_id,
  context.operation_kind, context.attempt_number, context.agent_config_id,
  context.configured_model_revision_id, context.input_event_sequence,
  context.source_event_sequence_end,
  (SELECT count(*) FROM model_call_contexts prior
   WHERE prior.project_id = context.project_id AND prior.agent_id = context.agent_id
     AND prior.operation_kind = 'compaction'
     AND prior.input_event_sequence = context.input_event_sequence
     AND prior.source_event_sequence_end = context.source_event_sequence_end
     AND prior.attempt_number < context.attempt_number
     AND prior.state = 'failed' AND prior.recovery_kind = 'reduce_compaction_source'
  )::bigint AS source_excerpt_reduction_count,
  context.runtime_lock_id, context.state, context.recovery_kind,
  context.api_format, context.api_variant, context.provider_request_id,
  context.provider_response_id, context.error_kind, context.error_code,
  context.error_message, context.error_details,
  coalesce(context.input_fingerprint, '')::text AS input_fingerprint,
  context.retry_at, context.input_tokens_total, context.uncached_input_tokens,
  context.cache_read_input_tokens, context.cache_write_input_tokens,
  context.output_tokens_total, context.reasoning_output_tokens,
  context.created_at, context.completed_at,
  coalesce(context.provider_reported_cost_usd::text, '')::text AS provider_reported_cost_usd,
  context.provider_metadata
FROM model_call_contexts context
WHERE context.agent_id = sqlc.arg(agent_id)
  AND context.id = sqlc.arg(id)
  AND EXISTS (
    SELECT 1
    FROM agents scoped_agent
    WHERE scoped_agent.project_id = sqlc.arg(project_id)
      AND scoped_agent.id = sqlc.arg(agent_id)
  );

-- name: ListProviderReplayRejectionCutoffs :many
-- @sqlc-vet-disable model-provider-configs-deleted-at
-- Replay safety remains part of immutable call lineage after provider soft deletion.
WITH current_replay_identity AS MATERIALIZED (
  SELECT context.project_id, context.agent_id, context.input_event_sequence,
         revision.model_provider_config_id, revision.provider_model_slug,
         provider.api_format, provider.api_variant
  FROM model_call_contexts context
  JOIN configured_model_revisions revision ON revision.org_id = context.org_id
    AND revision.id = context.configured_model_revision_id
  JOIN model_provider_configs provider ON provider.org_id = revision.org_id
    AND provider.id = revision.model_provider_config_id
  WHERE context.agent_id = sqlc.arg(agent_id)
    AND context.id = sqlc.arg(model_call_context_id)
    AND EXISTS (
      SELECT 1
      FROM agents scoped_agent
      WHERE scoped_agent.project_id = sqlc.arg(project_id)
        AND scoped_agent.id = sqlc.arg(agent_id)
    )
)
-- One row per rejected model slug, or a single row with a zero cutoff when
-- there are none; callers decide which slugs share replay with the current one.
SELECT current.provider_model_slug AS current_provider_model_slug,
       current.api_format AS current_api_format,
       COALESCE(failure.provider_model_slug, '')::text AS rejected_provider_model_slug,
       COALESCE(failure.cutoff_event_sequence, 0)::bigint AS cutoff_event_sequence
FROM current_replay_identity current
LEFT JOIN LATERAL (
  SELECT failure_revision.provider_model_slug,
         MAX(failure.input_event_sequence) AS cutoff_event_sequence
  FROM model_call_contexts failure
  JOIN configured_model_revisions failure_revision
    ON failure_revision.org_id = failure.org_id
    AND failure_revision.id = failure.configured_model_revision_id
  WHERE failure.project_id = current.project_id
    AND failure.agent_id = current.agent_id
    AND failure.operation_kind = 'normal'
    AND failure.state = 'failed'
    AND failure.error_kind = 'replay_rejected'
    AND failure.input_event_sequence <= current.input_event_sequence
    AND failure_revision.model_provider_config_id = current.model_provider_config_id
    AND failure.api_format = current.api_format
    AND failure.api_variant = current.api_variant
  GROUP BY failure_revision.provider_model_slug
) failure ON true;

-- name: CountModelCallRetries :one
SELECT (
  SELECT count(*) FROM model_call_contexts failure
  WHERE failure.project_id = context.project_id
    AND failure.agent_id = context.agent_id
    AND failure.input_event_sequence = context.input_event_sequence
    AND failure.operation_kind = context.operation_kind
    AND failure.state = 'failed' AND failure.recovery_kind = 'retry'
    AND (
      failure.source_event_sequence_end > context.source_event_sequence_end
      OR (failure.source_event_sequence_end IS NOT DISTINCT FROM context.source_event_sequence_end
          AND failure.attempt_number <= context.attempt_number)
    )
)::bigint AS retry_count
FROM model_call_contexts context
WHERE context.agent_id = sqlc.arg(agent_id)
  AND context.id = sqlc.arg(model_call_context_id)
  AND EXISTS (
    SELECT 1
    FROM agents scoped_agent
    WHERE scoped_agent.project_id = sqlc.arg(project_id)
      AND scoped_agent.id = sqlc.arg(agent_id)
  );

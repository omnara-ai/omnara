-- name: GetNormalModelCallContextByIdentity :one
SELECT context.id
FROM model_call_contexts context
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.operation_kind = 'normal'
  AND context.input_event_sequence = sqlc.arg(input_event_sequence)
ORDER BY context.attempt_number DESC
LIMIT 1;

-- name: GetModelCallContext :one
SELECT context.id, context.org_id, context.project_id, context.agent_id,
  context.operation_kind, context.attempt_number, context.agent_config_id,
  context.configured_model_revision_id, context.input_event_sequence,
  context.source_event_sequence_end,
  context.runtime_lock_id, context.state, context.recovery_kind,
  context.api_format, context.api_variant, context.provider_request_id,
  context.provider_response_id, context.error_kind, context.error_code,
  context.error_message, context.error_details,
  context.retry_at, context.input_tokens_total, context.uncached_input_tokens,
  context.cache_read_input_tokens, context.cache_write_input_tokens,
  context.output_tokens_total, context.reasoning_output_tokens,
  context.created_at, context.completed_at,
  coalesce(context.provider_reported_cost_usd::text, '')::text AS provider_reported_cost_usd,
  context.provider_metadata
FROM model_call_contexts context
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.id = sqlc.arg(id);

-- name: GetProviderReplaySuppressionCutoff :one
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
  WHERE context.project_id = sqlc.arg(project_id)
    AND context.agent_id = sqlc.arg(agent_id)
    AND context.id = sqlc.arg(model_call_context_id)
)
SELECT COALESCE((
  SELECT MAX(failure.input_event_sequence)
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
    AND failure_revision.provider_model_slug = current.provider_model_slug
    AND failure.api_format = current.api_format
    AND failure.api_variant = current.api_variant
), 0)::bigint AS cutoff_event_sequence
FROM current_replay_identity current;

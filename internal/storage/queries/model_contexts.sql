-- name: GetModelCallRevisionForClaim :one
-- @sqlc-vet-disable configured-models-deleted-at
-- @sqlc-vet-disable model-provider-configs-deleted-at
-- Model-call lineage must survive soft deletion so the unavailable model can fail durably.
SELECT configured_model.current_revision_id,
       CASE
           WHEN provider.management_kind = 'cluster'
               THEN COALESCE(admission.new_managed_work_allowed, true)
           ELSE true
       END AS new_managed_work_allowed
FROM agent_configs config
JOIN configured_models configured_model ON configured_model.org_id = config.org_id
  AND configured_model.id = config.configured_model_id
JOIN model_provider_configs provider ON provider.org_id = configured_model.org_id
  AND provider.id = configured_model.model_provider_config_id
LEFT JOIN org_managed_work_admission admission ON admission.org_id = config.org_id
WHERE config.project_id = sqlc.arg(project_id)
  AND config.id = sqlc.arg(agent_config_id);

-- name: InsertNormalModelCallContext :one
WITH active_agent AS MATERIALIZED (
  SELECT agent.org_id, agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
    AND agent.state = 'active'
),
live_runtime AS MATERIALIZED (
  SELECT runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN active_agent agent ON agent.id = runtime_lock.agent_id
  WHERE runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
),
active_config AS MATERIALIZED (
  SELECT input.agent_config_id
  FROM agent_events event
  JOIN agent_inputs input ON input.agent_id = event.agent_id
    AND input.id = event.agent_input_id
  JOIN active_agent agent ON agent.id = event.agent_id
  WHERE event.sequence <= sqlc.arg(input_event_sequence)
    AND event.event_kind = 'agent_input'
    AND input.input_kind = 'config_change'
    AND input.state = 'resolved'
    AND input.admitted_event_id = event.id
    AND input.agent_config_id IS NOT NULL
  ORDER BY event.sequence DESC
  LIMIT 1
),
selected_model AS MATERIALIZED (
  SELECT config.id AS agent_config_id,
         revision.id AS configured_model_revision_id
  FROM agent_configs config
  JOIN active_config ON active_config.agent_config_id = config.id
  JOIN configured_model_revisions revision ON revision.org_id = config.org_id
    AND revision.configured_model_id = config.configured_model_id
    AND revision.id = sqlc.arg(configured_model_revision_id)
  WHERE config.project_id = sqlc.arg(project_id)
)
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind, attempt_number,
  agent_config_id, configured_model_revision_id, input_event_sequence,
  runtime_lock_id, state, created_at
)
SELECT active_agent.org_id,
       active_agent.project_id,
       active_agent.id,
       'normal',
       1,
       selected_model.agent_config_id,
       selected_model.configured_model_revision_id,
       sqlc.arg(input_event_sequence),
       live_runtime.id,
       'started',
       statement_timestamp()
FROM active_agent
JOIN live_runtime ON true
JOIN selected_model ON true
RETURNING id;

-- name: InsertTriggeredCompactionModelCallContext :one
WITH active_agent AS MATERIALIZED (
  SELECT agent.org_id, agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
    AND agent.state = 'active'
),
live_runtime AS MATERIALIZED (
  SELECT runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN active_agent agent ON agent.id = runtime_lock.agent_id
  WHERE runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
),
latest_compaction AS MATERIALIZED (
  SELECT dependency.id, dependency.state, dependency.recovery_kind,
         dependency.input_event_sequence, dependency.source_event_sequence_end, dependency.source_excerpt_bytes
  FROM model_call_contexts dependency
  JOIN model_call_contexts parent ON parent.project_id = dependency.project_id
    AND parent.agent_id = dependency.agent_id
    AND parent.id = sqlc.arg(parent_model_call_context_id)
  JOIN active_agent agent ON agent.project_id = parent.project_id
    AND agent.id = parent.agent_id
  WHERE dependency.operation_kind = 'compaction'
    AND dependency.parent_normal_model_call_context_id = parent.id
  ORDER BY dependency.source_event_sequence_end ASC,
           dependency.source_excerpt_bytes ASC NULLS LAST, dependency.attempt_number DESC
  LIMIT 1
),
blocked AS MATERIALIZED (
  SELECT context.id, context.agent_config_id,
         context.input_event_sequence
  FROM model_call_contexts context
  JOIN active_agent agent ON agent.project_id = context.project_id
    AND agent.id = context.agent_id
  LEFT JOIN latest_compaction latest ON true
  WHERE context.id = sqlc.arg(parent_model_call_context_id)
    AND context.operation_kind = 'normal'
    AND context.state = 'failed'
    AND context.recovery_kind IN ('compact', 'compact_optional')
    AND (
      latest.id IS NULL
      OR (
        latest.state = 'failed'
        AND latest.recovery_kind = 'reduce_compaction_source'
        AND latest.input_event_sequence = context.input_event_sequence
        AND (sqlc.arg(source_event_sequence_end)::bigint < latest.source_event_sequence_end
          OR (sqlc.arg(source_event_sequence_end)::bigint = latest.source_event_sequence_end
            AND sqlc.narg(source_excerpt_bytes)::integer IS NOT NULL
            AND (latest.source_excerpt_bytes IS NULL OR sqlc.narg(source_excerpt_bytes)::integer < latest.source_excerpt_bytes)))
      )
    )
),
selected_model AS MATERIALIZED (
  SELECT blocked.id AS blocked_context_id,
         revision.id AS configured_model_revision_id
  FROM blocked
  JOIN agent_configs config ON config.project_id = sqlc.arg(project_id)
    AND config.id = blocked.agent_config_id
  JOIN configured_model_revisions revision ON revision.org_id = config.org_id
    AND revision.configured_model_id = config.configured_model_id
    AND revision.id = sqlc.arg(configured_model_revision_id)
)
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind, attempt_number,
  agent_config_id, configured_model_revision_id, input_event_sequence,
  source_event_sequence_end, parent_normal_model_call_context_id, source_excerpt_bytes,
  replaces_checkpoint_id, runtime_lock_id, state, created_at
)
SELECT active_agent.org_id,
       active_agent.project_id,
       active_agent.id,
       'compaction',
       1,
       blocked.agent_config_id,
       selected_model.configured_model_revision_id,
       blocked.input_event_sequence,
       sqlc.arg(source_event_sequence_end),
       blocked.id,
       sqlc.narg(source_excerpt_bytes)::integer,
       sqlc.narg(replaces_checkpoint_id)::uuid,
       live_runtime.id,
       'started',
       statement_timestamp()
FROM active_agent
JOIN live_runtime ON true
JOIN blocked ON true
JOIN selected_model ON selected_model.blocked_context_id = blocked.id
RETURNING id;

-- name: GetNormalModelCallContextByIdentity :one
SELECT context.id
FROM model_call_contexts context
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.operation_kind = 'normal'
  AND context.input_event_sequence = sqlc.arg(input_event_sequence)
ORDER BY context.attempt_number DESC
LIMIT 1;

-- name: GetCompactionModelCallContextByIdentity :one
SELECT context.id
FROM model_call_contexts context
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.operation_kind = 'compaction'
  AND context.parent_normal_model_call_context_id = sqlc.arg(parent_normal_model_call_context_id)
  AND context.source_excerpt_bytes IS NOT DISTINCT FROM sqlc.narg(source_excerpt_bytes)::integer
  AND context.replaces_checkpoint_id IS NOT DISTINCT FROM sqlc.narg(replaces_checkpoint_id)::uuid
  AND context.input_event_sequence = sqlc.arg(input_event_sequence)
  AND context.source_event_sequence_end = sqlc.arg(source_event_sequence_end)
ORDER BY context.attempt_number DESC
LIMIT 1;

-- name: GetModelCallContext :one
SELECT context.id, context.org_id, context.project_id, context.agent_id,
  context.operation_kind, context.attempt_number, context.agent_config_id,
  context.configured_model_revision_id, context.input_event_sequence,
  context.source_event_sequence_end, context.parent_normal_model_call_context_id,
  context.source_excerpt_bytes, context.recovery_max_output_tokens, context.replaces_checkpoint_id,
  context.recovery_checkpoint_id, context.recovery_checkpoint_retained_bytes,
  context.optional_input_target_tokens,
  context.optional_compaction_outcome,
  context.request_input_version, context.request_input_route_fingerprint,
  context.request_input_static_fingerprint, context.request_input_prefix_fingerprint,
  context.request_input_item_count,
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

-- name: ModelCallContextHasLaterSemanticEvent :one
SELECT model_call_context_has_later_semantic_event(
  sqlc.arg(project_id),
  sqlc.arg(agent_id),
  sqlc.arg(model_call_context_id)
) AS has_later_semantic_event;

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

-- name: GetModelCallContextTurnID :one
SELECT turn_id
FROM model_call_context_turns
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND model_call_context_id = sqlc.arg(model_call_context_id);

-- name: GetModelCallContextProviderModelSlug :one
SELECT revision.provider_model_slug
FROM model_call_contexts context
JOIN configured_model_revisions revision ON revision.org_id = context.org_id
  AND revision.id = context.configured_model_revision_id
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.id = sqlc.arg(model_call_context_id);

-- name: InsertNextModelCallContext :one
WITH agent_scope AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
),
predecessor AS MATERIALIZED (
  SELECT context.id, context.org_id, context.project_id, context.agent_id,
         context.operation_kind, context.attempt_number,
         context.agent_config_id, context.input_event_sequence,
         context.source_event_sequence_end, context.parent_normal_model_call_context_id,
         context.source_excerpt_bytes, context.replaces_checkpoint_id
  FROM model_call_contexts context
  JOIN agent_scope agent ON agent.project_id = context.project_id
    AND agent.id = context.agent_id
  WHERE context.id = sqlc.arg(predecessor_model_call_context_id)
    AND context.state = 'failed'
    AND (
      (context.recovery_kind IN ('retry', 'restore_output')
        AND context.retry_at <= statement_timestamp()
        AND model_call_transient_retry_count(context.id) <= sqlc.arg(max_retries)::integer
        AND EXISTS (
          SELECT 1 FROM agent_continuable_model_contexts(context.project_id, context.agent_id) continuable
          WHERE continuable.model_call_context_id = context.id AND NOT continuable.has_later_semantic_event
        ))
      OR (context.operation_kind = 'normal' AND context.recovery_kind IN ('compact', 'compact_optional')
        AND NOT EXISTS (SELECT 1 FROM model_call_contexts newer
          WHERE newer.agent_id = context.agent_id AND newer.operation_kind = 'normal'
            AND newer.input_event_sequence = context.input_event_sequence
            AND newer.attempt_number > context.attempt_number)
        AND EXISTS (
          SELECT 1 FROM model_call_contexts child
          JOIN agent_continuable_model_contexts(context.project_id, context.agent_id) continuable
            ON continuable.model_call_context_id = child.id
          WHERE child.parent_normal_model_call_context_id = context.id
            AND child.state = 'failed' AND child.recovery_kind = 'resume_normal'
            AND NOT continuable.has_later_semantic_event
        ))
    )
),
selected_model AS MATERIALIZED (
  SELECT predecessor.id AS predecessor_context_id,
         revision.id AS configured_model_revision_id
  FROM predecessor
  JOIN agent_configs config ON config.project_id = predecessor.project_id
    AND config.id = predecessor.agent_config_id
  JOIN configured_model_revisions revision ON revision.org_id = config.org_id
    AND revision.configured_model_id = config.configured_model_id
    AND revision.id = sqlc.arg(configured_model_revision_id)
),
live_runtime AS MATERIALIZED (
  SELECT runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN predecessor context ON context.agent_id = runtime_lock.agent_id
  WHERE runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND runtime_lock.cancel_requested_at IS NULL
    AND runtime_lock.lease_expires_at > statement_timestamp()
)
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind, attempt_number,
  agent_config_id, configured_model_revision_id, input_event_sequence,
  source_event_sequence_end, parent_normal_model_call_context_id, source_excerpt_bytes,
  replaces_checkpoint_id, runtime_lock_id, state, created_at
)
SELECT predecessor.org_id,
       predecessor.project_id,
       predecessor.agent_id,
       predecessor.operation_kind,
       predecessor.attempt_number + 1,
       predecessor.agent_config_id,
       selected_model.configured_model_revision_id,
       predecessor.input_event_sequence,
       predecessor.source_event_sequence_end,
       predecessor.parent_normal_model_call_context_id,
       predecessor.source_excerpt_bytes,
       predecessor.replaces_checkpoint_id,
       live_runtime.id,
       'started',
       statement_timestamp()
FROM predecessor
JOIN selected_model ON selected_model.predecessor_context_id = predecessor.id
JOIN live_runtime ON true
RETURNING id;

-- name: GetLiveModelCallContextForRuntime :one
SELECT id
FROM model_call_contexts
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND runtime_lock_id = sqlc.arg(runtime_lock_id)
  AND state = 'started';

-- name: FinishModelCallContext :one
WITH agent_scope AS MATERIALIZED (
  SELECT agent.project_id, agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
),
runtime AS MATERIALIZED (
  SELECT agent.project_id, runtime_lock.agent_id, runtime_lock.id
  FROM agent_runtime_locks runtime_lock
  JOIN agent_scope agent ON agent.id = runtime_lock.agent_id
  WHERE runtime_lock.id = sqlc.arg(runtime_lock_id)
    AND (
      sqlc.arg(allow_inactive_runtime_lock_for_teardown)::boolean
      OR (
        runtime_lock.cancel_requested_at IS NULL
        AND runtime_lock.lease_expires_at > statement_timestamp()
      )
    )
)
UPDATE model_call_contexts context
SET state = sqlc.arg(to_state),
    recovery_kind = sqlc.narg(recovery_kind),
    api_format = sqlc.arg(api_format),
    api_variant = sqlc.arg(api_variant),
    provider_request_id = sqlc.arg(provider_request_id),
    provider_response_id = sqlc.arg(provider_response_id),
    error_kind = sqlc.arg(error_kind),
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    error_details = sqlc.arg(error_details),
    retry_at = CASE
      WHEN sqlc.narg(retry_delay_microseconds)::bigint IS NULL THEN NULL
      ELSE statement_timestamp() +
        (sqlc.narg(retry_delay_microseconds)::bigint * interval '1 microsecond')
    END,
    input_tokens_total = sqlc.narg(input_tokens_total)::integer,
    uncached_input_tokens = sqlc.narg(uncached_input_tokens)::integer,
    cache_read_input_tokens = sqlc.narg(cache_read_input_tokens)::integer,
    cache_write_input_tokens = sqlc.narg(cache_write_input_tokens)::integer,
    output_tokens_total = sqlc.narg(output_tokens_total)::integer,
    reasoning_output_tokens = sqlc.narg(reasoning_output_tokens)::integer,
    provider_reported_cost_usd = sqlc.narg(provider_reported_cost_usd)::text::numeric,
    provider_metadata = sqlc.arg(provider_metadata),
    recovery_max_output_tokens = sqlc.narg(recovery_max_output_tokens)::integer,
    recovery_checkpoint_id = sqlc.narg(recovery_checkpoint_id)::uuid,
    recovery_checkpoint_retained_bytes = sqlc.narg(recovery_checkpoint_retained_bytes)::integer,
    optional_input_target_tokens = sqlc.narg(optional_input_target_tokens)::integer,
    optional_compaction_outcome = sqlc.narg(optional_compaction_outcome)::text,
    request_input_version = sqlc.narg(request_input_version)::integer,
    request_input_route_fingerprint = sqlc.narg(request_input_route_fingerprint)::text,
    request_input_static_fingerprint = sqlc.narg(request_input_static_fingerprint)::text,
    request_input_prefix_fingerprint = sqlc.narg(request_input_prefix_fingerprint)::text,
    request_input_item_count = sqlc.narg(request_input_item_count)::integer,
    completed_at = statement_timestamp()
FROM runtime
WHERE context.project_id = runtime.project_id
  AND context.agent_id = runtime.agent_id
  AND context.id = sqlc.arg(id)
  AND context.runtime_lock_id = runtime.id
  AND context.state = 'started'
RETURNING context.id;

-- name: AgentHasLiveModelCallContextBeforeFrontier :one
SELECT EXISTS (
  SELECT 1
  FROM model_call_contexts context
  WHERE context.project_id = sqlc.arg(project_id)
    AND context.agent_id = sqlc.arg(agent_id)
    AND context.state = 'started'
    AND context.input_event_sequence < sqlc.arg(input_event_sequence)
)::boolean;

-- name: AgentHasLiveModelCallContexts :one
SELECT EXISTS (
  SELECT 1
  FROM model_call_contexts context
  WHERE context.project_id = sqlc.arg(project_id)
    AND context.agent_id = sqlc.arg(agent_id)
    AND context.state = 'started'
)::boolean;

-- name: CancelRuntimeModelCallContextsForLifecycle :execrows
UPDATE model_call_contexts context
SET state = 'canceled',
    recovery_kind = NULL,
    error_kind = 'canceled',
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    error_details = sqlc.arg(error_details),
    retry_at = NULL,
    completed_at = statement_timestamp()
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.runtime_lock_id = sqlc.arg(runtime_lock_id)
  AND context.state = 'started';

-- name: InterruptRuntimeModelCallContextForRetry :execrows
UPDATE model_call_contexts context
SET state = 'failed',
    recovery_kind = 'retry',
    error_kind = sqlc.arg(error_kind),
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    error_details = sqlc.arg(error_details)::jsonb,
    retry_at = statement_timestamp() +
      (sqlc.arg(retry_delay_microseconds)::bigint * interval '1 microsecond'),
    completed_at = statement_timestamp()
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.id = sqlc.arg(id)
  AND context.runtime_lock_id = sqlc.arg(runtime_lock_id)
  AND context.state = 'started';

-- name: OpeningContentInputSetMatchesInputSequence :one
WITH requested AS (
  SELECT input_id
  FROM unnest(sqlc.arg(input_ids)::uuid[]) AS input_ids(input_id)
),
requested_distinct AS (
  SELECT DISTINCT input_id
  FROM requested
),
target_turn AS MATERIALIZED (
  SELECT agent_turn_id_at_event_sequence(
    sqlc.arg(agent_id),
    sqlc.arg(input_event_sequence)
  ) AS turn_id
),
opening AS (
  SELECT opening.input_id
  FROM target_turn turn
  CROSS JOIN LATERAL agent_model_call_opening_content_inputs(
    sqlc.arg(project_id),
    sqlc.arg(agent_id),
    turn.turn_id,
    sqlc.arg(input_event_sequence)
  ) AS opening(input_id, event_sequence)
)
SELECT (
  (SELECT count(*) FROM requested) = (SELECT count(*) FROM requested_distinct)
  AND NOT EXISTS (
    SELECT input_id FROM requested_distinct
    EXCEPT
    SELECT input_id FROM opening
  )
  AND NOT EXISTS (
    SELECT input_id FROM opening
    EXCEPT
    SELECT input_id FROM requested_distinct
  )
)::boolean;

-- name: ListModelCallOpeningContentInputs :many
SELECT opening.input_id::uuid AS input_id,
       opening.event_sequence::bigint AS event_sequence
FROM agent_model_call_opening_content_inputs(
  sqlc.arg(project_id),
  sqlc.arg(agent_id),
  sqlc.arg(turn_id),
  sqlc.arg(input_event_sequence)
) AS opening(input_id, event_sequence)
ORDER BY opening.event_sequence;

-- name: GetModelCallRecoveryState :one
WITH current AS MATERIALIZED (
  SELECT context.id, context.agent_id, context.operation_kind, context.input_event_sequence,
         context.parent_normal_model_call_context_id, context.configured_model_revision_id,
         context.agent_config_id, context.created_at
  FROM model_call_contexts context
  WHERE context.project_id = sqlc.arg(project_id) AND context.agent_id = sqlc.arg(agent_id)
    AND context.id = sqlc.arg(model_call_context_id)
), latest_optional AS MATERIALIZED (
  SELECT prior.id, prior.optional_input_target_tokens, prior.completed_at
  FROM model_call_contexts prior JOIN current ON prior.agent_id = current.agent_id
  WHERE prior.operation_kind = 'normal' AND prior.recovery_kind = 'compact_optional'
    AND prior.configured_model_revision_id = current.configured_model_revision_id
    AND prior.agent_config_id = current.agent_config_id
    AND prior.created_at < current.created_at
  ORDER BY prior.created_at DESC, prior.id DESC LIMIT 1
), latest_optional_child AS MATERIALIZED (
  SELECT child.state, child.optional_compaction_outcome
  FROM model_call_contexts child JOIN latest_optional ON child.parent_normal_model_call_context_id = latest_optional.id
  ORDER BY child.created_at DESC, child.id DESC LIMIT 1
), latest_observed_normal AS MATERIALIZED (
  SELECT measured.input_tokens_total
  FROM model_call_contexts measured
  JOIN current ON measured.agent_id = current.agent_id
    AND measured.configured_model_revision_id = current.configured_model_revision_id
    AND measured.agent_config_id = current.agent_config_id
  JOIN latest_optional ON measured.created_at > latest_optional.completed_at
  WHERE measured.operation_kind = 'normal' AND measured.state = 'succeeded'
    AND measured.created_at < current.created_at
  ORDER BY measured.created_at DESC, measured.id DESC LIMIT 1
), latest_checkpoint AS MATERIALIZED (
  SELECT event.sequence, checkpoint.id, checkpoint.summarized_through_event_sequence
  FROM agent_events event JOIN current ON event.agent_id = current.agent_id
  JOIN context_checkpoints checkpoint ON checkpoint.id = event.context_checkpoint_id AND checkpoint.agent_id = event.agent_id
  WHERE event.event_kind = 'context_checkpoint' AND event.sequence <= current.input_event_sequence
  ORDER BY event.sequence DESC LIMIT 1
), checkpoint_projection AS MATERIALIZED (
  SELECT prior.recovery_checkpoint_id, prior.recovery_checkpoint_retained_bytes
  FROM model_call_contexts prior JOIN current ON prior.agent_id = current.agent_id
  JOIN latest_checkpoint ON latest_checkpoint.id = prior.recovery_checkpoint_id
  WHERE prior.configured_model_revision_id = current.configured_model_revision_id
    AND prior.agent_config_id = current.agent_config_id
    AND prior.created_at < current.created_at
  ORDER BY prior.recovery_checkpoint_retained_bytes LIMIT 1
), restore_episode AS MATERIALIZED (
  SELECT model_call_productive_frontier(current.id)::bigint AS sequence FROM current
), output_episode AS MATERIALIZED (
  SELECT coalesce(max(event.sequence), 0)::bigint AS sequence
  FROM agent_events event JOIN current ON event.agent_id = current.agent_id
  LEFT JOIN model_outputs output ON output.agent_id = event.agent_id AND output.id = event.model_output_id
  LEFT JOIN context_checkpoints checkpoint ON checkpoint.agent_id = event.agent_id
    AND checkpoint.id = event.context_checkpoint_id
  LEFT JOIN model_call_contexts checkpoint_producer ON checkpoint_producer.agent_id = checkpoint.agent_id
    AND checkpoint_producer.id = checkpoint.producer_model_call_context_id
  WHERE event.sequence <= current.input_event_sequence
    AND (event.event_kind IN ('agent_input', 'tool_result')
      OR (event.event_kind = 'context_checkpoint' AND checkpoint_producer.replaces_checkpoint_id IS NOT NULL)
      OR (event.event_kind = 'model_output' AND (output.stop_reason <> 'max_tokens'
        OR EXISTS (SELECT 1 FROM tool_calls call WHERE call.agent_id = output.agent_id AND call.model_output_id = output.id))))
)
SELECT CASE WHEN current.operation_kind = 'normal' THEN model_call_transient_retry_count(current.id) ELSE 0 END::bigint AS normal_retry_count,
       CASE WHEN current.operation_kind = 'compaction' THEN model_call_transient_retry_count(current.id) ELSE 0 END::bigint AS compaction_retry_count,
       coalesce(parent.recovery_kind, '')::text AS parent_recovery_kind,
       EXISTS (
         SELECT 1 FROM model_call_contexts restored CROSS JOIN restore_episode
         WHERE restored.agent_id = current.agent_id AND restored.agent_config_id = current.agent_config_id
           AND restored.configured_model_revision_id = current.configured_model_revision_id
           AND restored.recovery_kind = 'restore_output'
           AND restored.input_event_sequence >= restore_episode.sequence
           AND restored.created_at < current.created_at
       )::boolean AS output_allowance_restored,
       checkpoint_projection.recovery_checkpoint_id,
       checkpoint_projection.recovery_checkpoint_retained_bytes,
       EXISTS (
         SELECT 1 FROM model_call_contexts prior
         JOIN context_checkpoints replaced ON replaced.id = prior.replaces_checkpoint_id
           AND replaced.agent_id = prior.agent_id
         JOIN latest_checkpoint ON latest_checkpoint.summarized_through_event_sequence = replaced.summarized_through_event_sequence
         WHERE prior.agent_id = current.agent_id AND prior.agent_config_id = current.agent_config_id
           AND prior.configured_model_revision_id = current.configured_model_revision_id
           AND prior.created_at < current.created_at
       )::boolean AS checkpoint_recompression_attempted,
       latest_optional.id AS last_optional_context_id,
       coalesce(latest_optional.optional_input_target_tokens, 0)::integer AS last_optional_input_target_tokens,
       coalesce(latest_optional_child.state = 'succeeded'
         OR latest_optional_child.optional_compaction_outcome = 'ineffective',
         false)::boolean AS last_optional_compaction_needs_headroom,
       EXISTS (
         SELECT 1 FROM model_call_contexts prior WHERE prior.agent_id = current.agent_id
           AND prior.operation_kind = 'normal' AND prior.recovery_kind = 'compact_optional'
           AND prior.input_event_sequence = current.input_event_sequence AND prior.created_at < current.created_at
       )::boolean AS optional_compaction_attempted_at_frontier,
       coalesce(latest_observed_normal.input_tokens_total, 0)::integer AS latest_observed_normal_input_tokens,
       (SELECT count(*) FROM model_call_contexts prior WHERE prior.agent_id = current.agent_id
          AND prior.operation_kind = 'normal' AND prior.input_event_sequence = current.input_event_sequence
          AND prior.created_at < current.created_at AND prior.recovery_kind IS DISTINCT FROM 'compact_optional')::bigint AS provider_attempt_count,
       (SELECT coalesce(min(prior.recovery_max_output_tokens), 0)::integer FROM model_call_contexts prior CROSS JOIN output_episode
        WHERE prior.agent_id = current.agent_id AND prior.operation_kind = 'normal'
          AND prior.configured_model_revision_id = current.configured_model_revision_id
          AND prior.input_event_sequence >= output_episode.sequence AND prior.created_at < current.created_at) AS recovery_max_output_tokens,
       (latest_checkpoint.sequence IS NOT NULL AND NOT EXISTS (
          SELECT 1 FROM model_call_contexts prior WHERE prior.agent_id = current.agent_id
            AND prior.operation_kind = 'normal' AND prior.input_event_sequence >= latest_checkpoint.sequence
            AND prior.created_at < current.created_at AND prior.recovery_kind IS DISTINCT FROM 'compact_optional'
       ))::boolean AS checkpoint_needs_normal_attempt
FROM current
LEFT JOIN model_call_contexts parent ON parent.id = current.parent_normal_model_call_context_id
LEFT JOIN latest_optional ON true
LEFT JOIN latest_optional_child ON true
LEFT JOIN latest_observed_normal ON true
LEFT JOIN latest_checkpoint ON true
LEFT JOIN checkpoint_projection ON true;

-- name: HasObservedInputHeadroomSince :one
SELECT EXISTS (
  SELECT 1 FROM model_call_contexts optional
  JOIN model_call_contexts measured ON measured.agent_id = optional.agent_id AND measured.project_id = optional.project_id
    AND measured.agent_config_id = optional.agent_config_id
  WHERE optional.project_id = sqlc.arg(project_id) AND optional.agent_id = sqlc.arg(agent_id)
    AND optional.id = sqlc.arg(after_optional_context_id) AND optional.recovery_kind = 'compact_optional'
    AND optional.configured_model_revision_id = sqlc.arg(configured_model_revision_id)
    AND measured.operation_kind = 'normal' AND measured.state = 'succeeded'
    AND measured.created_at > optional.completed_at
    AND measured.configured_model_revision_id = sqlc.arg(configured_model_revision_id)
    AND measured.input_tokens_total > 0 AND measured.input_tokens_total <= sqlc.arg(max_input_tokens)::integer
)::boolean;

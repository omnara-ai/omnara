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
         dependency.input_event_sequence, dependency.source_event_sequence_end
  FROM model_call_contexts dependency
  JOIN model_call_contexts parent ON parent.project_id = dependency.project_id
    AND parent.agent_id = dependency.agent_id
    AND parent.id = sqlc.arg(parent_model_call_context_id)
  JOIN active_agent agent ON agent.project_id = parent.project_id
    AND agent.id = parent.agent_id
  WHERE dependency.operation_kind = 'compaction'
    AND dependency.input_event_sequence = parent.input_event_sequence
  ORDER BY dependency.source_event_sequence_end ASC,
           dependency.attempt_number DESC
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
    AND context.recovery_kind = 'compact'
    AND (
      latest.id IS NULL
      OR (
        latest.state = 'failed'
        AND latest.recovery_kind = 'reduce_compaction_source'
        AND latest.input_event_sequence = context.input_event_sequence
        AND sqlc.arg(source_event_sequence_end)::bigint < latest.source_event_sequence_end
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
  source_event_sequence_end, runtime_lock_id, state, created_at
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
  AND context.input_event_sequence = sqlc.arg(input_event_sequence)
  AND context.source_event_sequence_end = sqlc.arg(source_event_sequence_end)
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
  coalesce(context.provider_reported_cost_usd::text, '')::text AS provider_reported_cost_usd
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

-- name: ModelCallOperationHasFailedWithErrorKind :one
WITH current_context AS MATERIALIZED (
  SELECT context.project_id, context.agent_id, context.id,
         context.operation_kind, context.input_event_sequence
  FROM model_call_contexts context
  WHERE context.project_id = sqlc.arg(project_id)
    AND context.agent_id = sqlc.arg(agent_id)
    AND context.id = sqlc.arg(model_call_context_id)
)
SELECT EXISTS (
  SELECT 1
  FROM current_context current
  JOIN model_call_contexts sibling ON sibling.project_id = current.project_id
    AND sibling.agent_id = current.agent_id
    AND sibling.operation_kind = current.operation_kind
    AND sibling.input_event_sequence = current.input_event_sequence
    AND sibling.id <> current.id
  WHERE sibling.state = 'failed'
    AND sibling.error_kind = sqlc.arg(error_kind)
) AS failed;

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
         context.source_event_sequence_end
  FROM model_call_contexts context
  JOIN agent_scope agent ON agent.project_id = context.project_id
    AND agent.id = context.agent_id
  WHERE context.id = sqlc.arg(predecessor_model_call_context_id)
    AND context.state = 'failed'
    AND context.recovery_kind = 'retry'
    AND context.retry_at <= statement_timestamp()
    AND context.attempt_number <= sqlc.arg(max_retries)::integer
    AND EXISTS (
      SELECT 1
      FROM agent_continuable_model_contexts(context.project_id, context.agent_id) continuable
      WHERE continuable.model_call_context_id = context.id
        AND NOT continuable.has_later_semantic_event
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
  source_event_sequence_end, runtime_lock_id, state, created_at
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

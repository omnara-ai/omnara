-- name: InsertOtelExporter :one
INSERT INTO otel_exporters (
    org_id, project_id, agent_id, endpoint, variant, headers, secret_headers, sample_basis_points
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, org_id, project_id, agent_id, endpoint, variant, headers, secret_headers, sample_basis_points,
          created_at, updated_at, deleted_at;

-- name: GetOtelExporter :one
SELECT exporter.id, exporter.org_id, exporter.project_id, exporter.agent_id, exporter.endpoint, exporter.variant,
       exporter.headers, exporter.secret_headers, exporter.sample_basis_points,
       exporter.created_at, exporter.updated_at, exporter.deleted_at
FROM otel_exporters exporter
JOIN orgs org ON org.id = exporter.org_id AND org.deleted_at IS NULL
WHERE exporter.org_id = $1 AND exporter.id = $2 AND exporter.deleted_at IS NULL;

-- name: ListOtelExporters :many
SELECT exporter.id, exporter.org_id, exporter.project_id, exporter.agent_id, exporter.endpoint, exporter.variant,
       exporter.headers, exporter.secret_headers, exporter.sample_basis_points,
       exporter.created_at, exporter.updated_at, exporter.deleted_at
FROM otel_exporters exporter
WHERE exporter.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(project_id)::uuid IS NULL OR exporter.project_id = sqlc.narg(project_id)::uuid)
  AND exporter.deleted_at IS NULL
  AND (exporter.created_at, exporter.id) > (
    coalesce(sqlc.narg(cursor_created_at)::timestamptz, '-infinity'::timestamptz),
    coalesce(sqlc.narg(cursor_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  )
ORDER BY exporter.created_at, exporter.id
LIMIT sqlc.arg(row_limit);

-- name: CountOrgOtelExporters :one
SELECT count(*) FROM otel_exporters
WHERE org_id = $1 AND deleted_at IS NULL;

-- name: UpdateOtelExporter :one
UPDATE otel_exporters SET endpoint = $3, variant = $4, headers = $5, secret_headers = $6,
    sample_basis_points = $7, updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING id, org_id, project_id, agent_id, endpoint, variant, headers, secret_headers, sample_basis_points,
          created_at, updated_at, deleted_at;

-- name: DeleteOtelExporter :execrows
UPDATE otel_exporters SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: EnqueueOtelExportDeliveries :exec
-- Queues one trace export per (exporter, turn) when a model output closes the
-- turn, meaning it did not ask for tools. Matches every active exporter whose
-- scope covers the agent and applies the exporter's trace sampling. A turn
-- that closes again while its delivery is pending or in flight is re-armed, so
-- the later, complete trace is the one exported.
INSERT INTO otel_export_deliveries (org_id, exporter_id, agent_id, turn_id, next_attempt_at)
SELECT DISTINCT exporter.org_id, exporter.id, event.agent_id, event.turn_id,
       statement_timestamp() + sqlc.arg(batch_delay_seconds)::double precision * interval '1 second'
FROM agent_events event
JOIN model_outputs output ON output.agent_id = event.agent_id AND output.id = event.model_output_id
JOIN agents agent ON agent.id = event.agent_id
JOIN projects project ON project.id = agent.project_id
JOIN orgs org ON org.id = project.org_id
JOIN otel_exporters exporter ON exporter.org_id = agent.org_id
    AND (exporter.project_id IS NULL OR exporter.project_id = agent.project_id)
    AND (exporter.agent_id IS NULL OR exporter.agent_id = agent.id)
WHERE event.agent_id = sqlc.arg(agent_id)
  AND event.sequence = ANY(sqlc.arg(event_sequences)::bigint[])
  AND event.event_kind = 'model_output'
  AND output.stop_reason <> 'tool_use'
  AND exporter.deleted_at IS NULL
  AND project.deleted_at IS NULL
  AND org.deleted_at IS NULL
  AND otel_trace_sampled(event.turn_id, exporter.sample_basis_points)
ON CONFLICT (exporter_id, turn_id) DO UPDATE
SET next_attempt_at = excluded.next_attempt_at, claim_token = NULL, claim_expires_at = NULL;

-- name: ClaimOtelExportDeliveries :many
-- Claims up to batch_limit due deliveries for the exporter that owns the
-- oldest due delivery, under one caller-supplied claim token.
WITH saturated_orgs AS MATERIALIZED (
    SELECT claims.org_id
    FROM (
        SELECT DISTINCT org_id, claim_token
        FROM otel_export_deliveries
        WHERE claim_expires_at > statement_timestamp()
    ) claims
    GROUP BY claims.org_id
    HAVING count(*) >= sqlc.arg(per_org_limit)::bigint
), head AS (
    SELECT delivery.exporter_id
    FROM otel_export_deliveries delivery
    WHERE delivery.next_attempt_at <= statement_timestamp()
      AND delivery.created_at > statement_timestamp()
          - sqlc.arg(delivery_window_seconds)::double precision * interval '1 second'
      AND (delivery.claim_expires_at IS NULL OR delivery.claim_expires_at <= statement_timestamp())
      AND delivery.org_id NOT IN (SELECT org_id FROM saturated_orgs)
    ORDER BY delivery.next_attempt_at, delivery.id
    LIMIT 1
), candidates AS (
    SELECT delivery.id
    FROM otel_export_deliveries delivery
    JOIN head ON head.exporter_id = delivery.exporter_id
    WHERE delivery.next_attempt_at <= statement_timestamp()
      AND delivery.created_at > statement_timestamp()
          - sqlc.arg(delivery_window_seconds)::double precision * interval '1 second'
      AND (delivery.claim_expires_at IS NULL OR delivery.claim_expires_at <= statement_timestamp())
    ORDER BY delivery.next_attempt_at, delivery.id
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE OF delivery SKIP LOCKED
)
UPDATE otel_export_deliveries delivery
SET attempt_count = attempt_count + 1,
    claim_token = sqlc.arg(claim_token)::uuid,
    claim_expires_at = statement_timestamp() + sqlc.arg(claim_seconds)::double precision * interval '1 second'
FROM candidates
WHERE delivery.id = candidates.id
RETURNING delivery.id, delivery.org_id, delivery.exporter_id, delivery.agent_id, delivery.turn_id,
    delivery.attempt_count, delivery.created_at;

-- name: CompleteOtelExportDeliveries :execrows
DELETE FROM otel_export_deliveries WHERE claim_token = sqlc.arg(claim_token)::uuid;

-- name: RetryOtelExportDeliveries :execrows
UPDATE otel_export_deliveries
SET next_attempt_at = statement_timestamp() + sqlc.arg(delay_seconds)::double precision * interval '1 second',
    claim_token = NULL, claim_expires_at = NULL
WHERE claim_token = sqlc.arg(claim_token)::uuid;

-- name: DeleteExpiredOtelExportDeliveries :execrows
WITH candidates AS (
    SELECT id FROM otel_export_deliveries
    WHERE created_at <= statement_timestamp()
        - sqlc.arg(delivery_window_seconds)::double precision * interval '1 second'
      AND (claim_expires_at IS NULL OR claim_expires_at <= statement_timestamp())
    ORDER BY created_at, id
    LIMIT sqlc.arg(limit_count)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM otel_export_deliveries delivery
USING candidates
WHERE delivery.id = candidates.id;

-- name: GetTurnTraceContext :one
SELECT agent.org_id, agent.project_id, agent.id AS agent_id, agent.name AS agent_name,
       agent.agent_profile_id, agent.parent_agent_id, agent.root_agent_id,
       turn.id AS turn_id, turn.turn_sequence
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
WHERE (sqlc.narg(project_id)::uuid IS NULL OR agent.project_id = sqlc.narg(project_id)::uuid)
  AND turn.agent_id = sqlc.arg(agent_id)
  AND turn.id = sqlc.arg(turn_id);

-- name: ListTurnTraceModelCalls :many
-- Every finished model call attempt of one turn, including failed attempts
-- that never produced a model output event.
SELECT context.id, context.operation_kind, context.attempt_number, context.state, context.api_format,
       coalesce(context.provider_response_id, '')::text AS provider_response_id,
       coalesce(context.provider_reported_cost_usd::text, '')::text AS provider_reported_cost_usd,
       context.error_kind, context.error_message,
       coalesce(context.input_tokens_total, 0)::bigint AS input_tokens_total,
       coalesce(context.cache_read_input_tokens, 0)::bigint AS cache_read_input_tokens,
       coalesce(context.cache_write_input_tokens, 0)::bigint AS cache_write_input_tokens,
       coalesce(context.output_tokens_total, 0)::bigint AS output_tokens_total,
       coalesce(context.reasoning_output_tokens, 0)::bigint AS reasoning_output_tokens,
       context.created_at, context.completed_at,
       revision.provider_model_slug,
       coalesce(output.served_provider_model_slug, '')::text AS served_provider_model_slug,
       coalesce(output.stop_reason, '')::text AS stop_reason
FROM model_call_contexts context
JOIN configured_model_revisions revision ON revision.org_id = context.org_id
  AND revision.id = context.configured_model_revision_id
LEFT JOIN model_outputs output ON output.agent_id = context.agent_id
  AND output.model_call_context_id = context.id
WHERE context.agent_id = sqlc.arg(agent_id)
  AND context.turn_id = sqlc.arg(turn_id)
  AND context.completed_at IS NOT NULL
ORDER BY context.created_at, context.id;

-- name: ListTurnTraceToolCalls :many
SELECT call.id, call.name, call.type, call.provider_call_id, call.input, call.created_at,
       call.model_call_context_id, result.outcome, result.completed_at
FROM tool_call_read_projection call
JOIN tool_call_results result ON result.agent_id = call.agent_id AND result.tool_call_id = call.id
WHERE call.agent_id = sqlc.arg(agent_id)
  AND call.turn_id = sqlc.arg(turn_id)
ORDER BY call.created_at, call.id;

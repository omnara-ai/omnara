-- name: InsertOtelExporter :one
INSERT INTO otel_exporters (
    org_id, project_id, agent_id, endpoint, variant, encoding, headers, secret_headers, sample_percent
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, org_id, project_id, agent_id, endpoint, variant, encoding, headers, secret_headers, sample_percent,
          created_at, updated_at, deleted_at;

-- name: GetOtelExporter :one
SELECT exporter.id, exporter.org_id, exporter.project_id, exporter.agent_id, exporter.endpoint, exporter.variant,
       exporter.encoding, exporter.headers, exporter.secret_headers, exporter.sample_percent,
       exporter.created_at, exporter.updated_at, exporter.deleted_at
FROM otel_exporters exporter
JOIN orgs org ON org.id = exporter.org_id AND org.deleted_at IS NULL
WHERE exporter.org_id = $1 AND exporter.id = $2 AND exporter.deleted_at IS NULL;

-- name: ListOtelExporters :many
SELECT exporter.id, exporter.org_id, exporter.project_id, exporter.agent_id, exporter.endpoint, exporter.variant,
       exporter.encoding, exporter.headers, exporter.secret_headers, exporter.sample_percent,
       exporter.created_at, exporter.updated_at, exporter.deleted_at
FROM otel_exporters exporter
WHERE exporter.org_id = sqlc.arg(org_id)
  AND (NOT sqlc.arg(filter_scope)::boolean OR (
    exporter.project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::uuid
    AND exporter.agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)::uuid
  ))
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
UPDATE otel_exporters SET endpoint = $3, variant = $4, encoding = $5, headers = $6, secret_headers = $7,
    sample_percent = $8, updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING id, org_id, project_id, agent_id, endpoint, variant, encoding, headers, secret_headers, sample_percent,
          created_at, updated_at, deleted_at;

-- name: DeleteOtelExporter :execrows
UPDATE otel_exporters SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: EnqueueOtelExportDeliveries :exec
-- A turn is queued when a model output ends it. A turn that instead stopped
-- on a tool call, such as one waiting on a subagent, has no such output, so it
-- is queued when the agent's next turn produces its first output.
-- A turn that closes again while its delivery is claimed is re-armed, so the
-- in-flight claim no longer owns the row.
INSERT INTO otel_export_deliveries (org_id, exporter_id, agent_id, turn_id)
SELECT DISTINCT exporter.org_id, exporter.id, closed.agent_id, closed.turn_id
FROM (
    SELECT event.agent_id, event.turn_id
    FROM agent_events event
    JOIN model_outputs output ON output.agent_id = event.agent_id AND output.id = event.model_output_id
    WHERE event.agent_id = sqlc.arg(agent_id)
      AND event.sequence = ANY(sqlc.arg(event_sequences)::bigint[])
      AND event.event_kind = 'model_output'
      AND output.stop_reason NOT IN ('tool_use', 'max_tokens')
    UNION
    SELECT previous.agent_id, previous.id
    FROM agent_events event
    JOIN agent_turns turn ON turn.agent_id = event.agent_id AND turn.id = event.turn_id
    JOIN agent_turns previous ON previous.agent_id = turn.agent_id
        AND previous.turn_sequence = turn.turn_sequence - 1
    JOIN LATERAL (
        SELECT last.model_output_id
        FROM agent_events last
        WHERE last.agent_id = previous.agent_id
          AND last.turn_id = previous.id
          AND last.event_kind = 'model_output'
        ORDER BY last.sequence DESC
        LIMIT 1
    ) final ON true
    JOIN model_outputs final_output ON final_output.agent_id = previous.agent_id
        AND final_output.id = final.model_output_id
    WHERE event.agent_id = sqlc.arg(agent_id)
      AND event.sequence = ANY(sqlc.arg(event_sequences)::bigint[])
      AND event.event_kind = 'model_output'
      AND final_output.stop_reason IN ('tool_use', 'max_tokens')
      AND NOT EXISTS (
          SELECT 1
          FROM agent_events earlier
          WHERE earlier.agent_id = event.agent_id
            AND earlier.turn_id = event.turn_id
            AND earlier.event_kind = 'model_output'
            AND earlier.sequence < event.sequence
      )
) closed
JOIN agents agent ON agent.id = closed.agent_id
JOIN projects project ON project.id = agent.project_id
JOIN orgs org ON org.id = project.org_id
JOIN otel_exporters exporter ON exporter.org_id = agent.org_id
    AND (exporter.project_id IS NULL OR exporter.project_id = agent.project_id)
    AND (exporter.agent_id IS NULL OR exporter.agent_id = agent.id)
WHERE exporter.deleted_at IS NULL
  AND project.deleted_at IS NULL
  AND org.deleted_at IS NULL
  AND otel_trace_sampled(closed.turn_id, exporter.sample_percent)
ON CONFLICT (exporter_id, turn_id) DO UPDATE
SET next_attempt_at = statement_timestamp(), claim_token = NULL, claim_expires_at = NULL;

-- name: ClaimOtelExportDelivery :one
WITH saturated_orgs AS MATERIALIZED (
    SELECT org_id
    FROM otel_export_deliveries
    WHERE claim_expires_at > statement_timestamp()
    GROUP BY org_id
    HAVING count(*) >= sqlc.arg(per_org_limit)::bigint
), candidate AS (
    SELECT delivery.id
    FROM otel_export_deliveries delivery
    WHERE delivery.next_attempt_at <= statement_timestamp()
      AND delivery.created_at > statement_timestamp()
          - sqlc.arg(delivery_window_seconds)::double precision * interval '1 second'
      AND (delivery.claim_expires_at IS NULL OR delivery.claim_expires_at <= statement_timestamp())
      AND delivery.org_id NOT IN (SELECT org_id FROM saturated_orgs)
    ORDER BY delivery.next_attempt_at, delivery.id
    LIMIT 1
    FOR UPDATE OF delivery SKIP LOCKED
)
UPDATE otel_export_deliveries delivery
SET attempt_count = attempt_count + 1,
    claim_token = uuidv7(),
    claim_expires_at = statement_timestamp() + sqlc.arg(claim_seconds)::double precision * interval '1 second'
FROM candidate
WHERE delivery.id = candidate.id
RETURNING delivery.id, delivery.org_id, delivery.exporter_id, delivery.agent_id, delivery.turn_id,
    delivery.attempt_count, delivery.claim_token, delivery.created_at;

-- name: CompleteOtelExportDelivery :exec
DELETE FROM otel_export_deliveries WHERE id = $1 AND claim_token = $2;

-- name: RetryOtelExportDelivery :exec
UPDATE otel_export_deliveries
SET next_attempt_at = statement_timestamp() + sqlc.arg(delay_seconds)::double precision * interval '1 second',
    claim_token = NULL, claim_expires_at = NULL
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(claim_token);

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
       agent.parent_agent_id, agent.root_agent_id,
       turn.id AS turn_id, turn.turn_sequence
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
WHERE (sqlc.narg(project_id)::uuid IS NULL OR agent.project_id = sqlc.narg(project_id)::uuid)
  AND turn.agent_id = sqlc.arg(agent_id)
  AND turn.id = sqlc.arg(turn_id);

-- name: ListTurnTraceModelCalls :many
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
SELECT call.id, call.name, call.provider_call_id, call.input, call.created_at,
       result.outcome, result.completed_at
FROM tool_call_read_projection call
JOIN tool_call_results result ON result.agent_id = call.agent_id AND result.tool_call_id = call.id
WHERE call.agent_id = sqlc.arg(agent_id)
  AND call.turn_id = sqlc.arg(turn_id)
ORDER BY call.created_at, call.id;

-- name: ListTurnTraceInputSources :many
-- An input another agent sent carries, in its idempotency key, the tool call
-- or model output it came from.
SELECT input.idempotency_scope, input.input_idempotency_key
FROM agent_events event
JOIN agent_inputs input ON input.agent_id = event.agent_id AND input.id = event.agent_input_id
WHERE event.agent_id = sqlc.arg(agent_id)
  AND event.turn_id = sqlc.arg(turn_id)
  AND event.event_kind = 'agent_input'
  AND input.idempotency_scope IS NOT NULL
  AND input.input_idempotency_key IS NOT NULL
ORDER BY event.sequence
LIMIT sqlc.arg(row_limit);

-- name: GetTraceLinkToolCall :one
SELECT call.agent_id, call.turn_id
FROM tool_call_read_projection call
WHERE call.project_id = sqlc.arg(project_id)
  AND call.id = sqlc.arg(tool_call_id);

-- name: GetTraceLinkModelOutput :one
SELECT event.agent_id, event.turn_id
FROM model_outputs output
JOIN agents agent ON agent.id = output.agent_id
JOIN agent_events event ON event.agent_id = output.agent_id AND event.model_output_id = output.id
WHERE output.id = sqlc.arg(model_output_id)
  AND agent.project_id = sqlc.arg(project_id);

-- name: ListTurnTraceSentInputs :many
-- The subagent turns that received what this turn's spawn_agent and
-- send_agent_message calls sent.
SELECT call.id AS tool_call_id, call.name AS tool_name, input.agent_id, event.turn_id
FROM tool_call_read_projection call
JOIN agents child ON child.parent_agent_id = call.agent_id
JOIN agent_inputs input ON input.project_id = child.project_id AND input.agent_id = child.id
    AND ((input.idempotency_scope = 'content_input'
            AND input.input_idempotency_key = 'launch:spawn:' || call.id::text || ':content-input')
        OR (input.idempotency_scope = 'subagent_message'
            AND input.input_idempotency_key = 'tool_call:' || call.id::text))
JOIN agent_events event ON event.agent_id = input.agent_id AND event.agent_input_id = input.id
WHERE call.agent_id = sqlc.arg(agent_id)
  AND call.turn_id = sqlc.arg(turn_id)
  AND call.name IN ('spawn_agent', 'send_agent_message')
ORDER BY event.sequence;

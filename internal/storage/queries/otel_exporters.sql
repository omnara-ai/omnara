-- name: InsertOtelExporter :one
INSERT INTO otel_exporters (
    org_id, project_id, agent_id, endpoint, variant, encoding, auth_kind, auth_options, credential_secret_id,
    headers, secret_headers, sample_percent
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, org_id, project_id, agent_id, endpoint, variant, encoding, auth_kind, auth_options, credential_secret_id,
          headers, secret_headers, sample_percent, created_at, updated_at, deleted_at;

-- name: GetOtelExporter :one
SELECT exporter.id, exporter.org_id, exporter.project_id, exporter.agent_id, exporter.endpoint, exporter.variant,
       exporter.encoding, exporter.auth_kind, exporter.auth_options, exporter.credential_secret_id,
       exporter.headers, exporter.secret_headers, exporter.sample_percent,
       exporter.created_at, exporter.updated_at, exporter.deleted_at
FROM otel_exporters exporter
JOIN orgs org ON org.id = exporter.org_id AND org.deleted_at IS NULL
WHERE exporter.org_id = $1 AND exporter.id = $2 AND exporter.deleted_at IS NULL;

-- name: ListOtelExporters :many
SELECT exporter.id, exporter.org_id, exporter.project_id, exporter.agent_id, exporter.endpoint, exporter.variant,
       exporter.encoding, exporter.auth_kind, exporter.auth_options, exporter.credential_secret_id,
       exporter.headers, exporter.secret_headers, exporter.sample_percent,
       exporter.created_at, exporter.updated_at, exporter.deleted_at
FROM otel_exporters exporter
WHERE exporter.org_id = sqlc.arg(org_id)
  AND (NOT sqlc.arg(filter_scope)::boolean OR (
    exporter.project_id = sqlc.narg(project_id)::uuid
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
UPDATE otel_exporters SET endpoint = $3, variant = $4, encoding = $5, auth_kind = $6, auth_options = $7,
    credential_secret_id = $8, headers = $9, secret_headers = $10, sample_percent = $11,
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL
RETURNING id, org_id, project_id, agent_id, endpoint, variant, encoding, auth_kind, auth_options, credential_secret_id,
          headers, secret_headers, sample_percent, created_at, updated_at, deleted_at;

-- name: DeleteOtelExporter :execrows
UPDATE otel_exporters SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: DeleteOtelExporterDeliveries :exec
-- Deleting an exporter drops the turns still queued for it.
DELETE FROM otel_export_deliveries WHERE org_id = $1 AND exporter_id = $2;

-- name: DeleteProjectOtelExporters :exec
-- A deleted project retires its own and its agents' exporters.
UPDATE otel_exporters SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteProjectOtelExportDeliveries :exec
-- Turns of a deleted project's agents are dropped when the project is deleted.
DELETE FROM otel_export_deliveries delivery
USING agents agent
WHERE agent.id = delivery.agent_id
  AND agent.org_id = sqlc.arg(org_id)
  AND agent.project_id = sqlc.arg(project_id);

-- name: EnqueueOtelExportDeliveries :exec
-- Queues turns the execution commands have finished. Each turn finishes once,
-- so exporter matching and sampling are decided here once per turn.
INSERT INTO otel_export_deliveries (org_id, exporter_id, agent_id, turn_id)
SELECT exporter.org_id, exporter.id, turn.agent_id, turn.id
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
JOIN projects project ON project.id = agent.project_id
JOIN orgs org ON org.id = project.org_id
JOIN otel_exporters exporter ON exporter.org_id = agent.org_id
    AND exporter.project_id = agent.project_id
    AND (exporter.agent_id IS NULL OR exporter.agent_id = agent.id)
WHERE turn.agent_id = sqlc.arg(agent_id)
  AND turn.id = ANY(sqlc.arg(turn_ids)::uuid[])
  AND exporter.deleted_at IS NULL
  AND project.deleted_at IS NULL
  AND org.deleted_at IS NULL
  AND otel_trace_sampled(turn.id, exporter.sample_percent)
ON CONFLICT (exporter_id, turn_id) DO NOTHING;

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

-- name: RetryOtelExportDelivery :one
UPDATE otel_export_deliveries
SET next_attempt_at = statement_timestamp() + sqlc.arg(delay_seconds)::double precision * interval '1 second',
    claim_token = NULL, claim_expires_at = NULL
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(claim_token)
RETURNING next_attempt_at, next_attempt_at >= created_at
    + sqlc.arg(delivery_window_seconds)::double precision * interval '1 second' AS gave_up;

-- name: DeleteExpiredOtelExportDeliveries :many
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
WHERE delivery.id = candidates.id
RETURNING delivery.id, delivery.org_id, delivery.exporter_id, delivery.agent_id, delivery.turn_id,
    delivery.attempt_count, delivery.created_at;

-- name: GetTurnTraceContext :one
SELECT agent.org_id, agent.project_id, agent.id AS agent_id, agent.name AS agent_name,
       agent.parent_agent_id, agent.root_agent_id,
       turn.id AS turn_id, turn.turn_sequence
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
JOIN projects project ON project.id = agent.project_id AND project.deleted_at IS NULL
JOIN orgs org ON org.id = project.org_id AND org.deleted_at IS NULL
WHERE (sqlc.narg(project_id)::uuid IS NULL OR agent.project_id = sqlc.narg(project_id)::uuid)
  AND turn.agent_id = sqlc.arg(agent_id)
  AND turn.id = sqlc.arg(turn_id);

-- name: ListTurnTraceModelCalls :many
-- Newest first, so a turn with more calls than the limit keeps its latest.
SELECT context.id, context.operation_kind, context.attempt_number, context.state, context.api_format,
       coalesce(context.provider_response_id, '')::text AS provider_response_id,
       coalesce(context.provider_reported_cost_usd::text, '')::text AS provider_reported_cost_usd,
       context.error_kind, context.error_message,
       coalesce(context.input_tokens_total, 0)::bigint AS input_tokens_total,
       context.cache_read_input_tokens, context.cache_write_input_tokens,
       coalesce(context.output_tokens_total, 0)::bigint AS output_tokens_total,
       context.reasoning_output_tokens,
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
ORDER BY context.created_at DESC, context.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListTurnTraceToolCalls :many
-- Newest first, so a turn with more calls than the limit keeps its latest.
-- Bounds each argument value and cumulative argument bytes read in SQL before
-- transferring rows to the worker.
WITH recent AS (
    SELECT call.id, call.name, call.provider_call_id, call.input::text AS raw_input,
           octet_length(call.input::text) AS input_bytes,
           call.created_at, result.outcome, result.completed_at
    FROM tool_call_read_projection call
    JOIN tool_call_results result ON result.agent_id = call.agent_id AND result.tool_call_id = call.id
    WHERE call.agent_id = sqlc.arg(agent_id)
      AND call.turn_id = sqlc.arg(turn_id)
    ORDER BY call.created_at DESC, call.id DESC
    LIMIT sqlc.arg(row_limit)
), budgeted AS (
    SELECT id, name, provider_call_id, raw_input, input_bytes, created_at, outcome, completed_at,
           sum(least(input_bytes, sqlc.arg(max_value_bytes)::integer))
               OVER (ORDER BY created_at DESC, id DESC) AS cumulative_bytes
    FROM recent
)
SELECT id, name, provider_call_id,
       (CASE
           WHEN cumulative_bytes > sqlc.arg(max_total_bytes)::bigint THEN ''::text
           WHEN input_bytes > sqlc.arg(max_value_bytes)::integer THEN left(raw_input, sqlc.arg(max_value_chars)::integer)
           ELSE raw_input
       END)::text AS input_text,
       (cumulative_bytes > sqlc.arg(max_total_bytes)::bigint
           OR input_bytes > sqlc.arg(max_value_bytes)::integer)::boolean AS input_truncated,
       created_at, outcome, completed_at
FROM budgeted
ORDER BY created_at DESC, id DESC;

-- name: ListTurnTraceEvents :many
-- Newest first, bounded by row count and cumulative content-block bytes read
-- before rows leave Postgres. Oversized single events clip each block in JSONB
-- so tool_call structure and valid JSON survive; events past the cumulative
-- budget omit content blocks and flag content_truncated. The budget is charged
-- to the turn's opening inputs first and then newest first, so a turn with
-- large later content still exports the messages that opened it.
WITH recent AS (
    SELECT projection.sequence, projection.event_kind, projection.is_opening_event,
           projection.model_call_context_id, projection.tool_call_id, projection.model_stop_reason,
           projection.content_blocks, projection.created_at,
           octet_length(projection.content_blocks::text) AS content_bytes
    FROM agent_event_read_projection projection
    WHERE projection.project_id = sqlc.arg(project_id)
      AND projection.agent_id = sqlc.arg(agent_id)
      AND projection.turn_id = sqlc.arg(turn_id)
    ORDER BY projection.sequence DESC
    LIMIT sqlc.arg(row_limit)
), budgeted AS (
    SELECT sequence, event_kind, is_opening_event, model_call_context_id, tool_call_id,
           model_stop_reason, content_blocks, created_at, content_bytes,
           sum(least(content_bytes, sqlc.arg(max_event_bytes)::integer))
               OVER (ORDER BY is_opening_event DESC, sequence DESC) AS cumulative_bytes
    FROM recent
)
SELECT sequence, event_kind, is_opening_event, model_call_context_id, tool_call_id, model_stop_reason,
       (CASE
           WHEN cumulative_bytes > sqlc.arg(max_total_bytes)::bigint THEN '[]'::jsonb
           WHEN content_bytes > sqlc.arg(max_event_bytes)::integer THEN coalesce((
               SELECT jsonb_agg(
                   CASE
                       WHEN elem.block ? 'text' AND octet_length(elem.block->>'text') > sqlc.arg(max_value_bytes)::integer
                           THEN jsonb_set(elem.block, '{text}', to_jsonb(left(elem.block->>'text', sqlc.arg(max_value_chars)::integer)))
                       WHEN elem.block ? 'value' AND octet_length((elem.block->'value')::text) > sqlc.arg(max_value_bytes)::integer
                           THEN jsonb_set(elem.block, '{value}', to_jsonb(left((elem.block->'value')::text, sqlc.arg(max_value_chars)::integer)))
                       WHEN elem.block ? 'input' AND octet_length((elem.block->'input')::text) > sqlc.arg(max_value_bytes)::integer
                           THEN jsonb_set(elem.block, '{input}', to_jsonb(left((elem.block->'input')::text, sqlc.arg(max_value_chars)::integer)))
                       ELSE elem.block
                   END
               )
               FROM jsonb_array_elements(content_blocks) AS elem(block)
           ), '[]'::jsonb)
           ELSE content_blocks
       END)::jsonb AS content_blocks,
       (cumulative_bytes > sqlc.arg(max_total_bytes)::bigint
           OR content_bytes > sqlc.arg(max_event_bytes)::integer)::boolean AS content_truncated,
       created_at
FROM budgeted
ORDER BY sequence DESC;

-- name: ListTurnTraceInputSources :many
-- Explicit provenance facts recorded by the cross-agent operation that created
-- the input, never derived from caller-controlled idempotency keys.
SELECT input.source_link_kind, input.source_tool_call_id, input.source_model_output_id
FROM agent_events event
JOIN agent_inputs input ON input.agent_id = event.agent_id AND input.id = event.agent_input_id
WHERE event.agent_id = sqlc.arg(agent_id)
  AND event.turn_id = sqlc.arg(turn_id)
  AND event.event_kind = 'agent_input'
  AND input.source_link_kind IS NOT NULL
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
-- The subagent turns that received what this turn's retained spawn_agent and
-- send_agent_message calls sent, restricted to the retained tool calls.
SELECT input.source_tool_call_id AS tool_call_id, input.source_link_kind, input.agent_id, event.turn_id
FROM agent_inputs input
JOIN agents child ON child.id = input.agent_id AND child.parent_agent_id = sqlc.arg(agent_id)
JOIN agent_events event ON event.agent_id = input.agent_id AND event.agent_input_id = input.id
WHERE input.source_tool_call_id = ANY(sqlc.arg(tool_call_ids)::uuid[])
  AND input.source_link_kind IN ('spawned_by', 'message_from')
ORDER BY event.sequence
LIMIT sqlc.arg(row_limit);

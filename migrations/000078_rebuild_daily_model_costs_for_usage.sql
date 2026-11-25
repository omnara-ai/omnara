-- +goose Up

SELECT pg_advisory_xact_lock(hashtextextended('daily_model_costs', 0));

DELETE FROM daily_model_costs;

INSERT INTO daily_model_costs(
    org_id, project_id, agent_profile_id, agent_id, configured_model_revision_id, day, effective_cost_usd,
    model_calls, model_calls_with_effective_cost, model_calls_with_reported_cost, provider_reported_cost_usd,
    uncached_input_tokens, cache_read_input_tokens, cache_write_input_tokens, output_tokens_total,
    reasoning_output_tokens
)
SELECT call.org_id, scope.project_id, scope.agent_profile_id, scope.agent_id, scope.configured_model_revision_id,
       (call.created_at AT TIME ZONE 'UTC')::date,
       coalesce(sum(coalesce(call.effective_cost_usd, call.provider_reported_cost_usd)), 0),
       count(*), count(coalesce(call.effective_cost_usd, call.provider_reported_cost_usd)),
       count(call.provider_reported_cost_usd), coalesce(sum(call.provider_reported_cost_usd), 0),
       coalesce(sum(call.uncached_input_tokens), 0), coalesce(sum(call.cache_read_input_tokens), 0),
       coalesce(sum(call.cache_write_input_tokens), 0), coalesce(sum(call.output_tokens_total), 0),
       coalesce(sum(call.reasoning_output_tokens), 0)
FROM model_call_contexts call
JOIN agents agent ON agent.project_id = call.project_id AND agent.id = call.agent_id
JOIN agents root ON root.project_id = agent.project_id AND root.id = agent.root_agent_id
CROSS JOIN LATERAL (VALUES
    (NULL::uuid, NULL::uuid, NULL::uuid, NULL::uuid, true),
    (call.project_id, NULL, NULL, NULL, true),
    (call.project_id, root.agent_profile_id, NULL, NULL, root.agent_profile_id IS NOT NULL),
    (call.project_id, NULL, call.agent_id, call.configured_model_revision_id, true)
) scope(project_id, agent_profile_id, agent_id, configured_model_revision_id, applies)
WHERE scope.applies
  AND (
    call.input_tokens_total IS NOT NULL
    OR call.output_tokens_total IS NOT NULL
    OR call.provider_reported_cost_usd IS NOT NULL
  )
  AND NOT EXISTS (SELECT 1 FROM pending_model_costs pending WHERE pending.model_call_context_id = call.id)
GROUP BY call.org_id, scope.project_id, scope.agent_profile_id, scope.agent_id, scope.configured_model_revision_id,
         (call.created_at AT TIME ZONE 'UTC')::date;

CREATE INDEX daily_model_costs_agent_usage_idx ON daily_model_costs(org_id, day) WHERE agent_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION model_usage_rows(p_org_id uuid, p_since timestamptz, p_until timestamptz, p_use_buckets boolean)
RETURNS TABLE (
    started_at timestamptz,
    project_id uuid,
    agent_id uuid,
    configured_model_revision_id uuid,
    model_calls bigint,
    model_calls_with_effective_cost bigint,
    model_calls_with_reported_cost bigint,
    input_tokens_total bigint,
    uncached_input_tokens bigint,
    cache_read_input_tokens bigint,
    cache_write_input_tokens bigint,
    output_tokens_total bigint,
    reasoning_output_tokens bigint,
    effective_cost_usd numeric,
    provider_reported_cost_usd numeric
)
LANGUAGE sql STABLE PARALLEL SAFE
AS $$
    WITH bounds AS NOT MATERIALIZED (
        SELECT coalesce(p_since, '-infinity') AS since,
               coalesce(p_until, 'infinity') AS until,
               whole.first_day, whole.end_day,
               whole.first_day::timestamp AT TIME ZONE 'UTC' AS first_day_at,
               whole.end_day::timestamp AT TIME ZONE 'UTC' AS end_day_at
        FROM (
            SELECT CASE
                       WHEN NOT p_use_buckets THEN 'infinity'::date
                       WHEN p_since IS NULL THEN '-infinity'::date
                       WHEN (p_since AT TIME ZONE 'UTC')::date::timestamp AT TIME ZONE 'UTC' = p_since
                           THEN (p_since AT TIME ZONE 'UTC')::date
                       ELSE (p_since AT TIME ZONE 'UTC')::date + 1
                   END AS first_day,
                   coalesce((p_until AT TIME ZONE 'UTC')::date, 'infinity'::date) AS end_day
        ) whole
    ), calls AS (
        SELECT call.*
        FROM model_call_contexts call CROSS JOIN bounds
        WHERE call.org_id = p_org_id
          AND (
            call.input_tokens_total IS NOT NULL
            OR call.output_tokens_total IS NOT NULL
            OR call.provider_reported_cost_usd IS NOT NULL
          )
          AND call.created_at >= bounds.since AND call.created_at < least(bounds.until, bounds.first_day_at)
        UNION ALL
        SELECT call.*
        FROM model_call_contexts call CROSS JOIN bounds
        WHERE call.org_id = p_org_id
          AND (
            call.input_tokens_total IS NOT NULL
            OR call.output_tokens_total IS NOT NULL
            OR call.provider_reported_cost_usd IS NOT NULL
          )
          AND call.created_at >= greatest(bounds.since, bounds.first_day_at, bounds.end_day_at)
          AND call.created_at < bounds.until
        UNION ALL
        SELECT call.*
        FROM pending_model_costs pending CROSS JOIN bounds
        JOIN model_call_contexts call ON call.id = pending.model_call_context_id
        WHERE pending.org_id = p_org_id AND pending.day >= bounds.first_day AND pending.day < bounds.end_day
    )
    SELECT calls.created_at, calls.project_id, calls.agent_id, calls.configured_model_revision_id, 1::bigint,
           (coalesce(calls.effective_cost_usd, calls.provider_reported_cost_usd) IS NOT NULL)::int::bigint,
           (calls.provider_reported_cost_usd IS NOT NULL)::int::bigint,
           coalesce(calls.input_tokens_total, 0)::bigint, coalesce(calls.uncached_input_tokens, 0)::bigint,
           coalesce(calls.cache_read_input_tokens, 0)::bigint, coalesce(calls.cache_write_input_tokens, 0)::bigint,
           coalesce(calls.output_tokens_total, 0)::bigint, coalesce(calls.reasoning_output_tokens, 0)::bigint,
           coalesce(calls.effective_cost_usd, calls.provider_reported_cost_usd, 0),
           coalesce(calls.provider_reported_cost_usd, 0)
    FROM calls
    UNION ALL
    SELECT cost.day::timestamp AT TIME ZONE 'UTC', cost.project_id, cost.agent_id, cost.configured_model_revision_id,
           cost.model_calls, cost.model_calls_with_effective_cost, cost.model_calls_with_reported_cost,
           cost.uncached_input_tokens + cost.cache_read_input_tokens + cost.cache_write_input_tokens,
           cost.uncached_input_tokens, cost.cache_read_input_tokens, cost.cache_write_input_tokens,
           cost.output_tokens_total, cost.reasoning_output_tokens, cost.effective_cost_usd,
           cost.provider_reported_cost_usd
    FROM daily_model_costs cost CROSS JOIN bounds
    WHERE cost.org_id = p_org_id AND cost.agent_profile_id IS NULL AND cost.agent_id IS NOT NULL
      AND cost.day >= bounds.first_day AND cost.day < bounds.end_day
$$;
-- +goose StatementEnd

ALTER TABLE daily_model_costs VALIDATE CONSTRAINT daily_model_costs_agent_revision;

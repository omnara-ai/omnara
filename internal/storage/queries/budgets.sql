-- name: InsertBudget :one
INSERT INTO budgets (org_id, project_id, agent_profile_id, agent_id, interval, amount_usd)
VALUES (
  sqlc.arg(org_id), sqlc.narg(project_id), sqlc.narg(agent_profile_id), sqlc.narg(agent_id),
  sqlc.arg(interval), sqlc.arg(amount_usd)::text::numeric
)
RETURNING id, org_id, project_id, agent_profile_id, agent_id, interval, amount_usd::text AS amount_usd,
          created_at, updated_at;

-- name: GetBudget :one
SELECT budget.id, budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval,
       budget.amount_usd::text AS amount_usd, budget.created_at, budget.updated_at,
       cost.effective_cost_usd::text AS spent_usd
FROM budgets budget
CROSS JOIN LATERAL budget_interval_cost(
  budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval
) cost
WHERE budget.org_id = sqlc.arg(org_id) AND budget.id = sqlc.arg(id);

-- name: ListBudgets :many
SELECT budget.id, budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval,
       budget.amount_usd::text AS amount_usd, budget.created_at, budget.updated_at,
       cost.effective_cost_usd::text AS spent_usd
FROM budgets budget
CROSS JOIN LATERAL budget_interval_cost(
  budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval
) cost
WHERE budget.org_id = sqlc.arg(org_id)
  AND (budget.created_at, budget.id) > (
    coalesce(sqlc.narg(cursor_created_at)::timestamptz, '-infinity'::timestamptz),
    coalesce(sqlc.narg(cursor_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  )
ORDER BY budget.created_at, budget.id
LIMIT sqlc.arg(row_limit);

-- name: ListScopedBudgets :many
SELECT budget.id, budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval,
       budget.amount_usd::text AS amount_usd, budget.created_at, budget.updated_at,
       cost.effective_cost_usd::text AS spent_usd
FROM budgets budget
CROSS JOIN LATERAL budget_interval_cost(
  budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval
) cost
WHERE budget.org_id = sqlc.arg(org_id)
  AND (
    (sqlc.narg(project_id)::uuid IS NULL
      AND budget.project_id IS NULL AND budget.agent_profile_id IS NULL AND budget.agent_id IS NULL)
    OR (budget.project_id = sqlc.narg(project_id)::uuid
      AND sqlc.narg(agent_profile_id)::uuid IS NULL AND sqlc.narg(agent_id)::uuid IS NULL
      AND budget.agent_profile_id IS NULL AND budget.agent_id IS NULL)
    OR (budget.project_id = sqlc.narg(project_id)::uuid
      AND budget.agent_profile_id = sqlc.narg(agent_profile_id)::uuid AND budget.agent_id IS NULL)
    OR (budget.project_id = sqlc.narg(project_id)::uuid
      AND budget.agent_profile_id IS NULL AND budget.agent_id = sqlc.narg(agent_id)::uuid)
  )
  AND (budget.created_at, budget.id) > (
    coalesce(sqlc.narg(cursor_created_at)::timestamptz, '-infinity'::timestamptz),
    coalesce(sqlc.narg(cursor_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  )
ORDER BY budget.created_at, budget.id
LIMIT sqlc.arg(row_limit);

-- name: UpdateBudgetAmount :one
UPDATE budgets SET amount_usd = sqlc.arg(amount_usd)::text::numeric, updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING id, org_id, project_id, agent_profile_id, agent_id, interval, amount_usd::text AS amount_usd,
          created_at, updated_at;

-- name: DeleteBudget :execrows
DELETE FROM budgets WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: DeleteBudgetsForProjectDeletion :exec
DELETE FROM budgets WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)::uuid;

-- name: DeleteBudgetsForAgentProfile :execrows
DELETE FROM budgets
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)::uuid
  AND agent_profile_id = sqlc.arg(agent_profile_id)::uuid;

-- name: DeleteOrganizationBudgets :exec
DELETE FROM budgets WHERE org_id = sqlc.arg(org_id);

-- name: MergeDailyModelCosts :one
WITH lock AS MATERIALIZED (
    SELECT pg_try_advisory_xact_lock(hashtextextended('daily_model_costs', 0)) AS held
), pending AS (
    SELECT pending.model_call_context_id
    FROM pending_model_costs pending CROSS JOIN lock
    WHERE lock.held
    ORDER BY pending.model_call_context_id
    LIMIT sqlc.arg(limit_count)
), calls AS (
    DELETE FROM pending_model_costs cost USING pending
    WHERE cost.model_call_context_id = pending.model_call_context_id
    RETURNING cost.model_call_context_id, cost.org_id, cost.project_id, cost.agent_profile_id, cost.agent_id, cost.day
), merged AS (
    INSERT INTO daily_model_costs(
        org_id, project_id, agent_profile_id, agent_id, configured_model_revision_id, day, effective_cost_usd,
        model_calls, model_calls_with_effective_cost, model_calls_with_reported_cost, provider_reported_cost_usd,
        uncached_input_tokens, cache_read_input_tokens, cache_write_input_tokens, output_tokens_total,
        reasoning_output_tokens
    )
    SELECT calls.org_id, scope.project_id, scope.agent_profile_id, scope.agent_id, scope.configured_model_revision_id,
           calls.day, coalesce(sum(call.effective_cost_usd), 0), count(*), count(call.effective_cost_usd),
           count(call.provider_reported_cost_usd), coalesce(sum(call.provider_reported_cost_usd), 0),
           coalesce(sum(call.uncached_input_tokens), 0), coalesce(sum(call.cache_read_input_tokens), 0),
           coalesce(sum(call.cache_write_input_tokens), 0), coalesce(sum(call.output_tokens_total), 0),
           coalesce(sum(call.reasoning_output_tokens), 0)
    FROM calls
    JOIN model_call_contexts call ON call.id = calls.model_call_context_id
    CROSS JOIN LATERAL (VALUES
        (NULL::uuid, NULL::uuid, NULL::uuid, NULL::uuid, true),
        (calls.project_id, NULL, NULL, NULL, true),
        (calls.project_id, calls.agent_profile_id, NULL, NULL, calls.agent_profile_id IS NOT NULL),
        (calls.project_id, NULL, calls.agent_id, call.configured_model_revision_id, true)
    ) scope(project_id, agent_profile_id, agent_id, configured_model_revision_id, applies)
    WHERE scope.applies
    GROUP BY calls.org_id, scope.project_id, scope.agent_profile_id, scope.agent_id, scope.configured_model_revision_id,
             calls.day
    ON CONFLICT (org_id, project_id, agent_profile_id, agent_id, day, configured_model_revision_id) DO UPDATE
    SET effective_cost_usd = daily_model_costs.effective_cost_usd + EXCLUDED.effective_cost_usd,
        model_calls = daily_model_costs.model_calls + EXCLUDED.model_calls,
        model_calls_with_effective_cost =
            daily_model_costs.model_calls_with_effective_cost + EXCLUDED.model_calls_with_effective_cost,
        model_calls_with_reported_cost =
            daily_model_costs.model_calls_with_reported_cost + EXCLUDED.model_calls_with_reported_cost,
        provider_reported_cost_usd = daily_model_costs.provider_reported_cost_usd + EXCLUDED.provider_reported_cost_usd,
        uncached_input_tokens = daily_model_costs.uncached_input_tokens + EXCLUDED.uncached_input_tokens,
        cache_read_input_tokens = daily_model_costs.cache_read_input_tokens + EXCLUDED.cache_read_input_tokens,
        cache_write_input_tokens = daily_model_costs.cache_write_input_tokens + EXCLUDED.cache_write_input_tokens,
        output_tokens_total = daily_model_costs.output_tokens_total + EXCLUDED.output_tokens_total,
        reasoning_output_tokens = daily_model_costs.reasoning_output_tokens + EXCLUDED.reasoning_output_tokens
)
SELECT count(*)::bigint FROM calls;

-- name: CountPendingModelCosts :one
SELECT count(*)::bigint FROM (SELECT 1 FROM pending_model_costs LIMIT sqlc.arg(row_limit)) pending;

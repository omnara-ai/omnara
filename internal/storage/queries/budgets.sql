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
WHERE budget.org_id = sqlc.arg(org_id) AND budget.id = sqlc.arg(id) AND budget.deleted_at IS NULL;

-- name: ListBudgets :many
SELECT budget.id, budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval,
       budget.amount_usd::text AS amount_usd, budget.created_at, budget.updated_at,
       cost.effective_cost_usd::text AS spent_usd
FROM budgets budget
CROSS JOIN LATERAL budget_interval_cost(
  budget.org_id, budget.project_id, budget.agent_profile_id, budget.agent_id, budget.interval
) cost
WHERE budget.org_id = sqlc.arg(org_id)
  AND (NOT sqlc.arg(filter_scope)::boolean OR (
    budget.project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::uuid
    AND budget.agent_profile_id IS NOT DISTINCT FROM sqlc.narg(agent_profile_id)::uuid
    AND budget.agent_id IS NOT DISTINCT FROM sqlc.narg(agent_id)::uuid
  ))
  AND budget.deleted_at IS NULL
  AND (budget.created_at, budget.id) > (
    coalesce(sqlc.narg(cursor_created_at)::timestamptz, '-infinity'::timestamptz),
    coalesce(sqlc.narg(cursor_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  )
ORDER BY budget.created_at, budget.id
LIMIT sqlc.arg(row_limit);

-- name: UpdateBudgetAmount :one
UPDATE budgets SET amount_usd = sqlc.arg(amount_usd)::text::numeric, updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id, org_id, project_id, agent_profile_id, agent_id, interval, amount_usd::text AS amount_usd,
          created_at, updated_at;

-- name: DeleteBudget :execrows
UPDATE budgets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteBudgetsForProjectDeletion :exec
UPDATE budgets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)::uuid AND deleted_at IS NULL;

-- name: DeleteBudgetsForAgentProfile :execrows
UPDATE budgets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)::uuid
  AND agent_profile_id = sqlc.arg(agent_profile_id)::uuid AND deleted_at IS NULL;

-- name: DeleteOrganizationBudgets :exec
UPDATE budgets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

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
    RETURNING cost.org_id, cost.project_id, cost.agent_profile_id, cost.agent_id, cost.day, cost.effective_cost_usd
), merged AS (
    INSERT INTO daily_model_costs(org_id, project_id, agent_profile_id, agent_id, day, effective_cost_usd)
    SELECT calls.org_id, scope.project_id, scope.agent_profile_id, scope.agent_id, calls.day,
           sum(calls.effective_cost_usd)
    FROM calls
    CROSS JOIN LATERAL (VALUES
        (NULL::uuid, NULL::uuid, NULL::uuid, true),
        (calls.project_id, NULL, NULL, true),
        (calls.project_id, calls.agent_profile_id, NULL, calls.agent_profile_id IS NOT NULL),
        (calls.project_id, NULL, calls.agent_id, true)
    ) scope(project_id, agent_profile_id, agent_id, applies)
    WHERE scope.applies
    GROUP BY calls.org_id, scope.project_id, scope.agent_profile_id, scope.agent_id, calls.day
    ON CONFLICT (org_id, project_id, agent_profile_id, agent_id, day) DO UPDATE
    SET effective_cost_usd = daily_model_costs.effective_cost_usd + EXCLUDED.effective_cost_usd
)
SELECT count(*)::bigint FROM calls;

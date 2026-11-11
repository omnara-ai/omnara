-- name: InsertBudget :one
INSERT INTO budgets (org_id, project_id, interval, amount_usd)
VALUES (sqlc.arg(org_id), sqlc.narg(project_id), sqlc.arg(interval), sqlc.arg(amount_usd)::text::numeric)
RETURNING id, org_id, project_id, interval, amount_usd::text AS amount_usd, created_at, updated_at;

-- name: GetBudget :one
SELECT budget.id, budget.org_id, budget.project_id, budget.interval, budget.amount_usd::text AS amount_usd,
       budget.created_at, budget.updated_at,
       cost.effective_cost_usd::text AS spent_usd
FROM budgets budget
CROSS JOIN LATERAL budget_interval_cost(budget.org_id, budget.project_id, budget.interval) cost
WHERE budget.org_id = sqlc.arg(org_id) AND budget.id = sqlc.arg(id) AND budget.deleted_at IS NULL;

-- name: ListBudgets :many
SELECT budget.id, budget.org_id, budget.project_id, budget.interval, budget.amount_usd::text AS amount_usd,
       budget.created_at, budget.updated_at,
       cost.effective_cost_usd::text AS spent_usd
FROM budgets budget
CROSS JOIN LATERAL budget_interval_cost(budget.org_id, budget.project_id, budget.interval) cost
WHERE budget.org_id = sqlc.arg(org_id)
  AND (NOT sqlc.arg(filter_scope)::boolean OR budget.project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::uuid)
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
RETURNING id, org_id, project_id, interval, amount_usd::text AS amount_usd, created_at, updated_at;

-- name: DeleteBudget :execrows
UPDATE budgets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeleteBudgetsForProjectDeletion :exec
UPDATE budgets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id)::uuid AND deleted_at IS NULL;

-- name: DeleteOrganizationBudgets :exec
UPDATE budgets SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL;

-- name: MergeProjectDailyModelCosts :exec
WITH pending AS (
    SELECT cost.org_id, cost.day, cost.project_id, cost.model_call_context_id
    FROM model_call_contexts c JOIN project_daily_model_costs cost
        ON cost.org_id = c.org_id AND cost.model_call_context_id IS NOT NULL
    WHERE c.agent_id = sqlc.arg(agent_id) AND c.id = sqlc.arg(id)
      AND pg_try_advisory_xact_lock(hashtextextended('project_daily_model_costs:' || c.org_id::text, 0))
    LIMIT 1000
), calls AS (
    DELETE FROM project_daily_model_costs cost USING pending
    WHERE cost.org_id = pending.org_id AND cost.day = pending.day AND cost.project_id = pending.project_id
      AND cost.model_call_context_id = pending.model_call_context_id
    RETURNING cost.org_id, cost.day, cost.project_id, cost.effective_cost_usd
)
INSERT INTO project_daily_model_costs(org_id, day, project_id, effective_cost_usd)
SELECT org_id, day, project_id, sum(effective_cost_usd)
FROM calls GROUP BY org_id, day, project_id
ON CONFLICT (org_id, day, project_id, model_call_context_id) DO UPDATE
SET effective_cost_usd = project_daily_model_costs.effective_cost_usd + EXCLUDED.effective_cost_usd;

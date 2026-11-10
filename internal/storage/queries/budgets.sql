-- name: InsertBudget :one
INSERT INTO budgets (org_id, project_id, interval, amount_usd)
VALUES (sqlc.arg(org_id), sqlc.narg(project_id), sqlc.arg(interval), sqlc.arg(amount_usd)::text::numeric)
RETURNING id, org_id, project_id, interval, amount_usd::text AS amount_usd, created_at, updated_at;

-- name: GetBudget :one
SELECT budget.id, budget.org_id, budget.project_id, budget.interval, budget.amount_usd::text AS amount_usd,
       budget.created_at, budget.updated_at,
       (cost.provider_reported_cost_usd + cost.estimated_cost_usd)::text AS spent_usd,
       cost.estimated_cost_usd::text AS estimated_usd
FROM budgets budget
CROSS JOIN LATERAL budget_interval_cost(budget.org_id, budget.project_id, budget.interval) cost
WHERE budget.org_id = sqlc.arg(org_id) AND budget.id = sqlc.arg(id) AND budget.deleted_at IS NULL;

-- name: ListBudgets :many
SELECT budget.id, budget.org_id, budget.project_id, budget.interval, budget.amount_usd::text AS amount_usd,
       budget.created_at, budget.updated_at,
       (cost.provider_reported_cost_usd + cost.estimated_cost_usd)::text AS spent_usd,
       cost.estimated_cost_usd::text AS estimated_usd
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

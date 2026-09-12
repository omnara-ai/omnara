-- name: LockOrganizationLifecycleShared :exec
SELECT pg_advisory_xact_lock_shared(
  hashtextextended('organization_lifecycle:' || sqlc.arg(org_id)::uuid::text, 0)
);

-- name: LockOrganizationLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(
  hashtextextended('organization_lifecycle:' || sqlc.arg(org_id)::uuid::text, 0)
);

-- name: LockProjectLifecycleShared :exec
SELECT pg_advisory_xact_lock_shared(
  hashtextextended('project_lifecycle:' || sqlc.arg(project_id)::uuid::text, 0)
);

-- name: LockProjectLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(
  hashtextextended('project_lifecycle:' || sqlc.arg(project_id)::uuid::text, 0)
);

-- name: GetActiveProjectForLifecycle :one
SELECT project.id
FROM projects project
JOIN orgs org ON org.id = project.org_id
  AND org.deleted_at IS NULL
WHERE project.org_id = sqlc.arg(org_id)
  AND project.id = sqlc.arg(project_id)
  AND project.deleted_at IS NULL;

-- name: OrgExistsActive :one
SELECT EXISTS (
  SELECT 1 FROM orgs WHERE id = sqlc.arg(id) AND deleted_at IS NULL
) AS org_exists;

-- name: UpsertProjectMachinePoolGrant :one
INSERT INTO project_machine_pool_grants(org_id, project_id, machine_pool_id, description, default_machine_cpu, default_machine_memory_mb, default_machine_env_overlay, default_machine_secret_env_overlay, default_machine_provider_options_overlay, default_cwd, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, idempotency_key, metadata, created_at, updated_at)
SELECT project.org_id, project.id, pool.id, sqlc.arg(description), sqlc.narg(default_machine_cpu)::integer, sqlc.narg(default_machine_memory_mb)::integer, sqlc.arg(default_machine_env_overlay)::jsonb, sqlc.arg(default_machine_secret_env_overlay)::jsonb, sqlc.arg(default_machine_provider_options_overlay)::jsonb, sqlc.arg(default_cwd), sqlc.narg(max_total_machines)::integer, sqlc.narg(max_total_cpu)::integer, sqlc.narg(max_total_memory_mb)::integer, sqlc.narg(max_machine_cpu)::integer, sqlc.narg(max_machine_memory_mb)::integer, sqlc.narg(idempotency_key), sqlc.arg(metadata), statement_timestamp(), statement_timestamp()
FROM projects project
JOIN machine_pools pool ON pool.org_id = project.org_id AND pool.id = sqlc.arg(machine_pool_id) AND pool.deleted_at IS NULL
WHERE project.org_id = sqlc.arg(org_id)
  AND project.id = sqlc.arg(project_id)
  AND project.deleted_at IS NULL
ON CONFLICT (project_id, machine_pool_id) DO NOTHING
RETURNING id, org_id, project_id, machine_pool_id, description, default_machine_cpu, default_machine_memory_mb, default_machine_env_overlay, default_machine_secret_env_overlay, default_machine_provider_options_overlay, default_cwd, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at;

-- name: GetProjectMachinePoolGrantByIdempotency :one
SELECT id, org_id, project_id, machine_pool_id, description, default_machine_cpu, default_machine_memory_mb, default_machine_env_overlay, default_machine_secret_env_overlay, default_machine_provider_options_overlay, default_cwd, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at
FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: GetProjectMachinePoolGrant :one
SELECT id, org_id, project_id, machine_pool_id, description, default_machine_cpu, default_machine_memory_mb, default_machine_env_overlay, default_machine_secret_env_overlay, default_machine_provider_options_overlay, default_cwd, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at
FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND id = sqlc.arg(id);

-- name: GetActiveProjectMachinePoolGrantForMachinePool :one
SELECT pmpg.id, pmpg.org_id, pmpg.project_id, pmpg.machine_pool_id, pmpg.description, pmpg.default_machine_cpu, pmpg.default_machine_memory_mb, pmpg.default_machine_env_overlay, pmpg.default_machine_secret_env_overlay, pmpg.default_machine_provider_options_overlay, pmpg.default_cwd, pmpg.max_total_machines, pmpg.max_total_cpu, pmpg.max_total_memory_mb, pmpg.max_machine_cpu, pmpg.max_machine_memory_mb, coalesce(pmpg.idempotency_key, '') AS idempotency_key, pmpg.metadata, pmpg.created_at, pmpg.updated_at, pool.name AS pool_name
FROM project_machine_pool_grants pmpg
JOIN machine_pools pool ON pool.org_id = pmpg.org_id AND pool.id = pmpg.machine_pool_id AND pool.deleted_at IS NULL
WHERE pmpg.project_id = sqlc.arg(project_id) AND pmpg.machine_pool_id = sqlc.arg(machine_pool_id);

-- name: ListProjectMachinePoolGrants :many
WITH listed AS (
 SELECT g.id, g.org_id, g.project_id, g.machine_pool_id, g.description, g.default_machine_cpu, g.default_machine_memory_mb, g.default_machine_env_overlay, g.default_machine_secret_env_overlay, g.default_machine_provider_options_overlay, g.default_cwd, g.max_total_machines, g.max_total_cpu, g.max_total_memory_mb, g.max_machine_cpu, g.max_machine_memory_mb, coalesce(g.idempotency_key, '') AS idempotency_key, g.metadata, g.created_at, g.updated_at,
 pool.name AS pool_name, pool.management_kind AS pool_management_kind, pool.description AS pool_description, pool.provider AS pool_provider, pool.created_at AS pool_created_at, pool.updated_at AS pool_updated_at,
 CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(pool.name) WHEN 'created_at' THEN to_char(g.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'updated_at' THEN to_char(g.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') END::text AS sort_key, false AS sort_is_null
 FROM project_machine_pool_grants g
 JOIN machine_pools pool ON pool.org_id = g.org_id AND pool.id = g.machine_pool_id AND pool.deleted_at IS NULL
 WHERE g.org_id = sqlc.arg(org_id) AND g.project_id = sqlc.arg(project_id)
  AND (sqlc.arg(name_pattern)::text = '' OR pool.name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
)
SELECT id, org_id, project_id, machine_pool_id, description, default_machine_cpu,
 default_machine_memory_mb, default_machine_env_overlay, default_machine_secret_env_overlay,
 default_machine_provider_options_overlay, default_cwd, max_total_machines, max_total_cpu,
 max_total_memory_mb, max_machine_cpu, max_machine_memory_mb,
 idempotency_key, metadata, created_at, updated_at,
 pool_name, pool_management_kind, pool_description, pool_provider, pool_created_at, pool_updated_at,
 sort_key, sort_is_null
FROM listed WHERE sqlc.arg(cursor_set)::boolean = false
 OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListProjectMachinePoolGrantRefsForMachinePool :many
SELECT id, project_id, machine_pool_id
FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id)
  AND machine_pool_id = sqlc.arg(machine_pool_id)
ORDER BY project_id, id;

-- name: ListProjectMachinePoolGrantRefsForProjectLifecycle :many
SELECT id, project_id, machine_pool_id
FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id)
  AND project_id = sqlc.arg(project_id)
ORDER BY machine_pool_id, id;

-- name: ListProjectMachinePoolGrantRefsForOrganizationLifecycle :many
SELECT id, project_id, machine_pool_id
FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id)
ORDER BY machine_pool_id, project_id, id;

-- name: LockProjectMachinePoolGrantForLifecycle :one
SELECT id, machine_pool_id
FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id)
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ListPoolGrantMachineIDsForLifecycle :many
SELECT machine.id
FROM machines machine
JOIN project_machine_grants machine_grant ON machine_grant.org_id = machine.org_id
  AND machine_grant.machine_id = machine.id
WHERE machine_grant.org_id = sqlc.arg(org_id)
  AND machine_grant.project_id = sqlc.arg(project_id)
  AND machine_grant.project_machine_pool_grant_id = sqlc.arg(project_machine_pool_grant_id)::uuid
  AND machine_grant.source_kind = 'pool'
  AND machine.source_kind = 'pool'
  AND machine.deleted_at IS NULL
ORDER BY machine.id;

-- name: ListPoolGrantAgentRefsForLifecycle :many
SELECT DISTINCT agent.project_id, agent.id AS agent_id
FROM agents agent
JOIN agent_machine_bindings binding ON binding.project_id = agent.project_id
  AND binding.agent_id = agent.id
JOIN project_machine_grants machine_grant ON machine_grant.project_id = binding.project_id
  AND machine_grant.machine_id = binding.machine_id
WHERE machine_grant.org_id = sqlc.arg(org_id)
  AND machine_grant.project_id = sqlc.arg(project_id)
  AND machine_grant.project_machine_pool_grant_id = sqlc.arg(project_machine_pool_grant_id)::uuid
  AND machine_grant.source_kind = 'pool'
  AND binding.state = 'attached'
ORDER BY agent.project_id, agent.id;

-- name: ListMachinePoolAgentRefsForLifecycle :many
SELECT DISTINCT agent.project_id, agent.id AS agent_id
FROM agents agent
JOIN agent_machine_bindings binding ON binding.project_id = agent.project_id
  AND binding.agent_id = agent.id
JOIN machines machine ON machine.org_id = binding.org_id
  AND machine.id = binding.machine_id
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.source_kind = 'pool'
  AND machine.deleted_at IS NULL
  AND binding.state = 'attached'
ORDER BY agent.project_id, agent.id;

-- name: UpdateProjectMachinePoolGrant :one
UPDATE project_machine_pool_grants
SET description = sqlc.arg(description),
    default_machine_cpu = sqlc.narg(default_machine_cpu)::integer,
    default_machine_memory_mb = sqlc.narg(default_machine_memory_mb)::integer,
    default_machine_env_overlay = sqlc.arg(default_machine_env_overlay)::jsonb,
    default_machine_secret_env_overlay = sqlc.arg(default_machine_secret_env_overlay)::jsonb,
    default_machine_provider_options_overlay = sqlc.arg(default_machine_provider_options_overlay)::jsonb,
    default_cwd = sqlc.arg(default_cwd),
    max_total_machines = sqlc.narg(max_total_machines)::integer,
    max_total_cpu = sqlc.narg(max_total_cpu)::integer,
    max_total_memory_mb = sqlc.narg(max_total_memory_mb)::integer,
    max_machine_cpu = sqlc.narg(max_machine_cpu)::integer,
    max_machine_memory_mb = sqlc.narg(max_machine_memory_mb)::integer,
    metadata = sqlc.arg(metadata),
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
RETURNING id, org_id, project_id, machine_pool_id, description, default_machine_cpu, default_machine_memory_mb, default_machine_env_overlay, default_machine_secret_env_overlay, default_machine_provider_options_overlay, default_cwd, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at;

-- name: DeleteProjectMachinePoolGrant :one
DELETE FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
RETURNING id, org_id, project_id, machine_pool_id, description, default_machine_cpu, default_machine_memory_mb, default_machine_env_overlay, default_machine_secret_env_overlay, default_machine_provider_options_overlay, default_cwd, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at;

-- name: MarkPoolGrantMachinesDeleting :many
UPDATE machines machine
SET lifecycle_state = 'deleting',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = machine.lifecycle_version + 1,
    lifecycle_reason_code = 'pool_grant_revoked',
    lifecycle_reason_message = 'project machine pool grant revoked',
    next_reconcile_after = statement_timestamp(),
    updated_at = statement_timestamp()
FROM project_machine_grants pmgrant
WHERE pmgrant.org_id = sqlc.arg(org_id)
  AND pmgrant.project_id = sqlc.arg(project_id)
  AND pmgrant.project_machine_pool_grant_id = sqlc.arg(project_machine_pool_grant_id)::uuid
  AND pmgrant.source_kind = 'pool'
  AND machine.org_id = pmgrant.org_id
  AND machine.id = pmgrant.machine_id
  AND machine.source_kind = 'pool'
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
RETURNING machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at, 'offline'::text AS connection_state, machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version;

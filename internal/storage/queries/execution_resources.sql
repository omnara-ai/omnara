-- name: InsertMachinePool :one
INSERT INTO machine_pools(org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, runtime_protection_enabled, max_total_machines, max_total_cpu, max_total_memory_mb, min_machine_cpu, min_machine_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, created_at, updated_at)
SELECT orgs.id, sqlc.arg(name), sqlc.arg(management_kind), sqlc.arg(description), sqlc.arg(provider), sqlc.narg(default_machine_cpu)::integer, sqlc.narg(default_machine_memory_mb)::integer, sqlc.arg(default_machine_env)::jsonb, sqlc.arg(default_machine_secret_env)::jsonb, sqlc.arg(default_machine_provider_options)::jsonb, sqlc.arg(default_cwd), sqlc.arg(provider_config), sqlc.narg(provider_auth_secret_id)::uuid, sqlc.arg(provider_auth_env_var), sqlc.arg(runtime_protection_enabled)::boolean, sqlc.arg(max_total_machines)::integer, sqlc.narg(max_total_cpu)::integer, sqlc.narg(max_total_memory_mb)::integer, sqlc.narg(min_machine_cpu)::integer, sqlc.narg(min_machine_memory_mb)::integer, sqlc.narg(max_machine_cpu)::integer, sqlc.narg(max_machine_memory_mb)::integer, sqlc.arg(metadata), statement_timestamp(), statement_timestamp()
FROM orgs
LEFT JOIN secrets provider_auth_secret ON provider_auth_secret.org_id = orgs.id
  AND provider_auth_secret.id = sqlc.narg(provider_auth_secret_id)::uuid
  AND provider_auth_secret.management_kind = 'tenant'
  AND provider_auth_secret.owner_kind = 'org'
  AND provider_auth_secret.kind = 'generic'
WHERE orgs.id = sqlc.arg(org_id)
  AND orgs.deleted_at IS NULL
  AND (
    (sqlc.arg(management_kind) = 'tenant' AND provider_auth_secret.id IS NOT NULL AND sqlc.arg(provider_auth_env_var) = '') OR
    (sqlc.arg(management_kind) = 'cluster' AND sqlc.narg(provider_auth_secret_id)::uuid IS NULL AND sqlc.arg(provider_auth_env_var) <> '')
  )
ON CONFLICT (org_id, name) WHERE deleted_at IS NULL DO NOTHING
RETURNING id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id;

-- name: GetMachinePool :one
SELECT id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id
FROM machine_pools
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: LockMachinePoolForUpdate :one
SELECT id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id
FROM machine_pools
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- name: UpdateMachinePool :one
UPDATE machine_pools
SET name = sqlc.arg(name),
    description = sqlc.arg(description),
    default_machine_cpu = sqlc.narg(default_machine_cpu)::integer,
    default_machine_memory_mb = sqlc.narg(default_machine_memory_mb)::integer,
    default_machine_env = sqlc.arg(default_machine_env)::jsonb,
    default_machine_secret_env = sqlc.arg(default_machine_secret_env)::jsonb,
    default_machine_provider_options = sqlc.arg(default_machine_provider_options)::jsonb,
    default_cwd = sqlc.arg(default_cwd),
    provider_config = sqlc.arg(provider_config),
    provider_auth_secret_id = sqlc.narg(provider_auth_secret_id)::uuid,
    runtime_protection_enabled = sqlc.arg(runtime_protection_enabled)::boolean,
    max_total_machines = sqlc.arg(max_total_machines)::integer,
    max_total_cpu = sqlc.narg(max_total_cpu)::integer,
    max_total_memory_mb = sqlc.narg(max_total_memory_mb)::integer,
    min_machine_cpu = sqlc.narg(min_machine_cpu)::integer,
    min_machine_memory_mb = sqlc.narg(min_machine_memory_mb)::integer,
    max_machine_cpu = sqlc.narg(max_machine_cpu)::integer,
    max_machine_memory_mb = sqlc.narg(max_machine_memory_mb)::integer,
    metadata = sqlc.arg(metadata),
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND management_kind = sqlc.arg(management_kind)
  AND deleted_at IS NULL
RETURNING id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id;

-- name: ClearMachinePoolRuntimeMismatch :exec
UPDATE machines
SET provider_runtime_mismatch_since = NULL,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND lifecycle_state = 'active'
  AND deleted_at IS NULL
  AND provider_runtime_mismatch_since IS NOT NULL;

-- name: DeleteMachinePool :one
UPDATE machine_pools pool
SET deleted_at = statement_timestamp(),
    deletion_provider_auth_secret_version_id = (
      SELECT secret.current_version_id
      FROM secrets secret
      WHERE secret.org_id = pool.org_id
        AND secret.id = pool.provider_auth_secret_id
    ),
    updated_at = statement_timestamp()
WHERE pool.org_id = sqlc.arg(org_id)
  AND pool.id = sqlc.arg(id)
  AND pool.deleted_at IS NULL
RETURNING deleted_at;

-- name: ReleaseMachinePoolCredentialIfIdle :exec
-- A deleted pool keeps its provider credential until every pooled machine has
-- finished provider teardown; once idle the credential is released so the
-- secret can be deleted.
UPDATE machine_pools pool
SET provider_auth_secret_id = NULL,
    deletion_provider_auth_secret_version_id = NULL,
    updated_at = statement_timestamp()
WHERE pool.org_id = sqlc.arg(org_id)
  AND pool.id = sqlc.arg(machine_pool_id)
  AND pool.deleted_at IS NOT NULL
  AND pool.provider_auth_secret_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM machines machine
    WHERE machine.org_id = pool.org_id
      AND machine.machine_pool_id = pool.id
      AND machine.deleted_at IS NULL
  );

-- name: MarkMachinePoolMachinesDeleting :many
UPDATE machines machine
SET lifecycle_state = 'deleting',
    lifecycle_changed_at = pool.deleted_at,
    lifecycle_version = machine.lifecycle_version + 1,
    lifecycle_reason_code = sqlc.narg(lifecycle_reason_code),
    lifecycle_reason_message = sqlc.arg(lifecycle_reason_message),
    next_reconcile_after = pool.deleted_at,
    provider_runtime_mismatch_since = NULL,
    wake_attempt_expires_at = NULL,
    updated_at = pool.deleted_at
FROM machine_pools pool
WHERE pool.org_id = sqlc.arg(org_id)
  AND pool.id = sqlc.arg(machine_pool_id)
  AND pool.deleted_at IS NOT NULL
  AND machine.org_id = pool.org_id
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.source_kind = 'pool'
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
RETURNING machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at, 'offline'::text AS connection_state, machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version;

-- name: DeletePoolProjectMachineGrantsForMachinePool :exec
DELETE FROM project_machine_grants machine_grant
USING machines machine
WHERE machine_grant.org_id = sqlc.arg(org_id)
  AND machine.org_id = machine_grant.org_id
  AND machine.id = machine_grant.machine_id
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine_grant.source_kind = 'pool';

-- name: DeleteProjectMachinePoolGrantsForMachinePool :many
DELETE FROM project_machine_pool_grants
WHERE org_id = sqlc.arg(org_id)
  AND machine_pool_id = sqlc.arg(machine_pool_id)
RETURNING id, project_id;

-- name: GetMachinePoolForLifecycle :one
SELECT id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id
FROM machine_pools
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: LockMachinePoolForLifecycle :one
-- @sqlc-vet-disable machine-pools-deleted-at
SELECT id
FROM machine_pools
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ListMachinePools :many
SELECT id, org_id, name, management_kind, description, provider, default_machine_cpu,
       default_machine_memory_mb, default_machine_env, default_machine_secret_env,
       default_machine_provider_options, default_cwd, provider_config,
       provider_auth_secret_id, deletion_provider_auth_secret_version_id,
       provider_auth_env_var, max_total_machines,
       max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb,
       metadata, deleted_at, created_at, updated_at, runtime_protection_enabled,
       min_machine_cpu, min_machine_memory_mb,
       CASE sqlc.arg(sort_field)::text
         WHEN 'name' THEN lower(name)
         WHEN 'created_at' THEN to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
         WHEN 'updated_at' THEN to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
       END::text AS sort_key,
       false AS sort_is_null
FROM machine_pools
WHERE org_id = sqlc.arg(org_id)
  AND deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (
    sqlc.arg(cursor_set)::boolean = false
    OR (
      sqlc.arg(sort_desc)::boolean = false
      AND (
        CASE sqlc.arg(sort_field)::text
          WHEN 'name' THEN lower(name)
          WHEN 'created_at' THEN to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
          WHEN 'updated_at' THEN to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
        END::text,
        id
      ) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid)
    )
    OR (
      sqlc.arg(sort_desc)::boolean = true
      AND (
        CASE sqlc.arg(sort_field)::text
          WHEN 'name' THEN lower(name)
          WHEN 'created_at' THEN to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
          WHEN 'updated_at' THEN to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
        END::text,
        id
      ) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid)
    )
  )
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN
           CASE sqlc.arg(sort_field)::text
             WHEN 'name' THEN lower(name)
             WHEN 'created_at' THEN to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
             WHEN 'updated_at' THEN to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
           END::text
         END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean THEN
           CASE sqlc.arg(sort_field)::text
             WHEN 'name' THEN lower(name)
             WHEN 'created_at' THEN to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
             WHEN 'updated_at' THEN to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
           END::text
         END DESC,
         CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC,
         CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListActiveMachinePoolIDsForOrganizationDeletion :many
SELECT id
FROM machine_pools
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL
ORDER BY id;

-- name: ListActiveBYOMachineIDsForOrganizationDeletion :many
SELECT id
FROM machines
WHERE org_id = sqlc.arg(org_id) AND source_kind = 'byo' AND deleted_at IS NULL
ORDER BY id;

-- name: ListOrganizationMachineIDsForLifecycle :many
SELECT id
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND deleted_at IS NULL
ORDER BY id;

-- name: GetMachinePoolByName :one
SELECT id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id
FROM machine_pools
WHERE org_id = sqlc.arg(org_id) AND name = sqlc.arg(name) AND deleted_at IS NULL;

-- name: ListClusterManagedMachinePools :many
SELECT id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id
FROM machine_pools
WHERE org_id = sqlc.arg(org_id) AND management_kind = 'cluster' AND deleted_at IS NULL
ORDER BY name, id;

-- name: ListClusterManagedMachinePoolsByName :many
SELECT id, org_id, name, management_kind, description, provider, default_machine_cpu, default_machine_memory_mb, default_machine_env, default_machine_secret_env, default_machine_provider_options, default_cwd, provider_config, provider_auth_secret_id, provider_auth_env_var, max_total_machines, max_total_cpu, max_total_memory_mb, max_machine_cpu, max_machine_memory_mb, metadata, deleted_at, created_at, updated_at, runtime_protection_enabled, min_machine_cpu, min_machine_memory_mb, deletion_provider_auth_secret_version_id
FROM machine_pools
WHERE name = sqlc.arg(name) AND management_kind = 'cluster' AND deleted_at IS NULL
ORDER BY org_id, id;

-- name: GetPoolGrantConfigValidationContext :one
SELECT pool.provider,
       pool.management_kind AS pool_management_kind,
       pool.provider_config,
       pool.default_machine_cpu,
       pool.default_machine_memory_mb,
       pool.default_machine_env,
       pool.default_machine_secret_env,
       pool.default_machine_provider_options,
       pool.max_total_cpu AS pool_max_total_cpu,
       pool.max_total_memory_mb AS pool_max_total_memory_mb,
       pool.min_machine_cpu AS pool_min_machine_cpu,
       pool.min_machine_memory_mb AS pool_min_machine_memory_mb,
       pool.max_machine_cpu AS pool_max_machine_cpu,
       pool.max_machine_memory_mb AS pool_max_machine_memory_mb,
       pool_grant.default_machine_cpu AS grant_default_machine_cpu,
       pool_grant.default_machine_memory_mb AS grant_default_machine_memory_mb,
       pool_grant.default_machine_env_overlay AS grant_default_machine_env_overlay,
       pool_grant.default_machine_secret_env_overlay AS grant_default_machine_secret_env_overlay,
       pool_grant.default_machine_provider_options_overlay AS grant_default_machine_provider_options_overlay,
       pool_grant.min_machine_cpu AS grant_min_machine_cpu,
       pool_grant.min_machine_memory_mb AS grant_min_machine_memory_mb,
       pool_grant.max_machine_cpu AS grant_max_machine_cpu,
       pool_grant.max_machine_memory_mb AS grant_max_machine_memory_mb
FROM project_machine_pool_grants pool_grant
JOIN machine_pools pool ON pool.org_id = pool_grant.org_id
  AND pool.id = pool_grant.machine_pool_id
  AND pool.deleted_at IS NULL
WHERE pool_grant.project_id = sqlc.arg(project_id)
  AND pool_grant.machine_pool_id = sqlc.arg(machine_pool_id)
;

-- name: GetActiveProjectMachinePoolGrantForLaunch :one
SELECT pool_grant.id,
       pool_grant.org_id,
       pool_grant.project_id,
       pool_grant.machine_pool_id,
       pool_grant.description,
       pool_grant.default_machine_cpu AS grant_default_machine_cpu,
       pool_grant.default_machine_memory_mb AS grant_default_machine_memory_mb,
       pool_grant.default_machine_env_overlay AS grant_default_machine_env_overlay,
       pool_grant.default_machine_secret_env_overlay AS grant_default_machine_secret_env_overlay,
       pool_grant.default_machine_provider_options_overlay AS grant_default_machine_provider_options_overlay,
       pool_grant.default_cwd AS grant_default_cwd,
       pool_grant.max_total_machines AS grant_max_total_machines,
       pool_grant.max_total_cpu AS grant_max_total_cpu,
       pool_grant.max_total_memory_mb AS grant_max_total_memory_mb,
       pool_grant.min_machine_cpu AS grant_min_machine_cpu,
       pool_grant.min_machine_memory_mb AS grant_min_machine_memory_mb,
       pool_grant.max_machine_cpu AS grant_max_machine_cpu,
       pool_grant.max_machine_memory_mb AS grant_max_machine_memory_mb,
       pool_grant.metadata,
       pool.name AS pool_name,
       pool.management_kind AS pool_management_kind,
       pool.max_total_machines AS pool_max_total_machines,
       pool.max_total_cpu AS pool_max_total_cpu,
       pool.max_total_memory_mb AS pool_max_total_memory_mb,
       pool.min_machine_cpu AS pool_min_machine_cpu,
       pool.min_machine_memory_mb AS pool_min_machine_memory_mb,
       pool.max_machine_cpu AS pool_max_machine_cpu,
       pool.max_machine_memory_mb AS pool_max_machine_memory_mb,
       pool.provider,
       pool.provider_auth_secret_id,
       pool.provider_auth_env_var,
       pool.provider_config,
       pool.default_machine_cpu,
       pool.default_machine_memory_mb,
       pool.default_machine_env,
       pool.default_machine_secret_env,
       pool.default_machine_provider_options,
       pool.default_cwd,
       CASE
           WHEN pool.management_kind = 'cluster'
               THEN COALESCE(admission.new_managed_work_allowed, true)
           ELSE true
       END AS new_managed_work_allowed
FROM machine_pools pool
JOIN project_machine_pool_grants pool_grant ON pool_grant.org_id = pool.org_id
  AND pool_grant.machine_pool_id = pool.id
LEFT JOIN org_managed_work_admission admission ON admission.org_id = pool.org_id
WHERE pool_grant.org_id = sqlc.arg(org_id)
  AND pool_grant.project_id = sqlc.arg(project_id)
  AND pool_grant.machine_pool_id = sqlc.arg(machine_pool_id)
  AND pool.deleted_at IS NULL
FOR UPDATE OF pool_grant;

-- name: GetActivePoolMachineUsage :one
SELECT count(*)::integer AS machines,
       coalesce(sum(cpu), 0)::bigint AS cpu,
       coalesce(sum(memory_mb), 0)::bigint AS memory_mb
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND machine_pool_id = sqlc.arg(machine_pool_id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL;

-- name: GetActiveProjectMachinePoolUsage :one
SELECT count(*)::integer AS machines,
       coalesce(sum(machine.cpu), 0)::bigint AS cpu,
       coalesce(sum(machine.memory_mb), 0)::bigint AS memory_mb
FROM project_machine_grants machine_grant
JOIN machines machine ON machine.org_id = machine_grant.org_id
  AND machine.id = machine_grant.machine_id
WHERE machine_grant.org_id = sqlc.arg(org_id)
  AND machine_grant.project_id = sqlc.arg(project_id)
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)
  AND machine_grant.source_kind = 'pool'
  AND machine.source_kind = 'pool'
  AND machine.deleted_at IS NULL;

-- name: ListMachinePoolMachineIDsForLifecycle :many
SELECT id
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND source_kind = 'pool'
  AND deleted_at IS NULL
ORDER BY id;

-- name: ListProjectMachineIDsForLifecycle :many
SELECT machine.id
FROM machines machine
JOIN project_machine_grants machine_grant ON machine_grant.org_id = machine.org_id
  AND machine_grant.machine_id = machine.id
WHERE machine_grant.org_id = sqlc.arg(org_id)
  AND machine_grant.project_id = sqlc.arg(project_id)
  AND machine.deleted_at IS NULL
ORDER BY machine.id;

-- name: InsertMachine :one
INSERT INTO machines(org_id, machine_pool_id, source_kind, display_name, description, provider, lifecycle_state, provider_resource_id, cpu, memory_mb, cwd, env, secret_env, provider_options, idempotency_key, lifecycle_changed_at, lifecycle_reason_code, lifecycle_reason_message, next_reconcile_after, provision_attempts, delete_attempts, metadata, created_at, updated_at)
SELECT orgs.id, sqlc.narg(machine_pool_id)::uuid, sqlc.arg(source_kind), sqlc.arg(display_name), sqlc.arg(description), sqlc.arg(provider), sqlc.arg(lifecycle_state), sqlc.narg(provider_resource_id), sqlc.narg(cpu)::integer, sqlc.narg(memory_mb)::integer, sqlc.arg(cwd)::text, sqlc.arg(env)::jsonb, sqlc.arg(secret_env)::jsonb, sqlc.narg(provider_options)::jsonb, sqlc.narg(idempotency_key), statement_timestamp(), sqlc.narg(lifecycle_reason_code), sqlc.arg(lifecycle_reason_message), CASE WHEN sqlc.arg(source_kind)::text = 'pool' THEN statement_timestamp() END, sqlc.arg(provision_attempts)::integer, sqlc.arg(delete_attempts)::integer, sqlc.arg(metadata), statement_timestamp(), statement_timestamp()
FROM orgs
LEFT JOIN machine_pools pool ON pool.org_id = orgs.id AND pool.id = sqlc.narg(machine_pool_id)::uuid
WHERE orgs.id = sqlc.arg(org_id)
  AND orgs.deleted_at IS NULL
  AND (
    sqlc.narg(machine_pool_id)::uuid IS NULL
    OR (
      pool.id IS NOT NULL
      AND pool.deleted_at IS NULL
      AND pool.provider = sqlc.arg(provider)
    )
  )
ON CONFLICT (org_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING id, org_id, machine_pool_id, source_kind, display_name, description, provider, lifecycle_state, provider_resource_id, provider_provision_attempted_at, 'offline'::text AS connection_state, last_observed_at, cpu, memory_mb, cwd, env, secret_env, provider_options, coalesce(idempotency_key, '') AS idempotency_key, coalesce(lifecycle_reason_code, '') AS lifecycle_reason_code, lifecycle_reason_message, next_reconcile_after, provision_attempts, delete_attempts, metadata, deleted_at, created_at, updated_at, lifecycle_changed_at, lifecycle_version;

-- name: GetMachineByIdempotency :one
SELECT machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at,
       connection.connection_state,
       coalesce(current_runtime.state_reason_code, '') AS connection_state_reason,
       machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version
FROM machines machine
JOIN machine_connection_states connection ON connection.org_id = machine.org_id AND connection.machine_id = machine.id
LEFT JOIN daemon_runtimes current_runtime ON current_runtime.org_id = machine.org_id
  AND current_runtime.id = machine.current_daemon_runtime_id
WHERE machine.org_id = sqlc.arg(org_id) AND machine.idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: GetMachine :one
SELECT machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at,
       connection.connection_state,
       coalesce(current_runtime.state_reason_code, '') AS connection_state_reason,
       machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at,
       machine.lifecycle_changed_at, machine.lifecycle_version,
       coalesce(machine.sandbox_url, '') AS sandbox_url
FROM machines machine
JOIN machine_connection_states connection ON connection.org_id = machine.org_id AND connection.machine_id = machine.id
LEFT JOIN daemon_runtimes current_runtime ON current_runtime.org_id = machine.org_id
  AND current_runtime.id = machine.current_daemon_runtime_id
WHERE machine.org_id = sqlc.arg(org_id) AND machine.id = sqlc.arg(id);

-- name: LockPoolMachineGrant :one
-- @sqlc-vet-disable machine-pools-deleted-at machines-deleted-at
-- Lock helper: teardown flows must lock rows for soft-deleted machines and pools.
WITH locked_pool AS MATERIALIZED (
  SELECT machine.id AS machine_id,
         pool.id AS machine_pool_id,
         pool.org_id
  FROM machines machine
  JOIN machine_pools pool ON pool.org_id = machine.org_id
    AND pool.id = machine.machine_pool_id
  WHERE machine.org_id = sqlc.arg(org_id)
    AND machine.id = sqlc.arg(machine_id)
    AND machine.source_kind = 'pool'
  FOR UPDATE OF pool
)
SELECT machine_grant.project_id
FROM locked_pool pool
JOIN project_machine_grants machine_grant ON machine_grant.org_id = pool.org_id
  AND machine_grant.machine_id = pool.machine_id
JOIN project_machine_pool_grants pool_grant ON pool_grant.org_id = machine_grant.org_id
  AND pool_grant.project_id = machine_grant.project_id
  AND pool_grant.id = machine_grant.project_machine_pool_grant_id
  AND pool_grant.machine_pool_id = pool.machine_pool_id
WHERE machine_grant.source_kind = 'pool'
FOR UPDATE OF pool_grant;

-- name: LockPoolMachineForProvisioningClaim :one
SELECT id
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL
FOR UPDATE;

-- name: ClaimPoolMachineForProvisioning :one
WITH claimed AS (
  UPDATE machines machine
  SET lifecycle_state = 'provisioning',
      lifecycle_changed_at = statement_timestamp(),
      lifecycle_version = machine.lifecycle_version + 1,
      lifecycle_reason_code = NULL,
      lifecycle_reason_message = '',
      failure_report = NULL,
      next_reconcile_after = statement_timestamp() + sqlc.arg(claim_timeout_seconds)::bigint * interval '1 second',
      provision_attempts = machine.provision_attempts + 1,
      updated_at = statement_timestamp()
  WHERE machine.org_id = sqlc.arg(org_id)
    AND machine.id = sqlc.arg(id)
    AND machine.source_kind = 'pool'
    AND machine.deleted_at IS NULL
    AND (
      (
        machine.lifecycle_state = 'provision_failed'
        AND machine.provision_attempts < sqlc.arg(max_provision_attempts)::integer
        AND machine.next_reconcile_after <= statement_timestamp()
      )
      OR (
        machine.lifecycle_state = 'provisioning'
        AND machine.provision_attempts < sqlc.arg(max_provision_attempts)::integer
        AND machine.next_reconcile_after <= statement_timestamp()
      )
    )
  RETURNING machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at, machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, machine.idempotency_key, machine.lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version
)
SELECT claimed.id, claimed.org_id, claimed.machine_pool_id, claimed.source_kind, claimed.display_name, claimed.description, claimed.provider, claimed.lifecycle_state, claimed.provider_resource_id, claimed.provider_provision_attempted_at,
       'offline'::text AS connection_state,
       claimed.last_observed_at, claimed.cpu, claimed.memory_mb, claimed.cwd, claimed.env, claimed.secret_env, claimed.provider_options, coalesce(claimed.idempotency_key, '') AS idempotency_key, coalesce(claimed.lifecycle_reason_code, '') AS lifecycle_reason_code, claimed.lifecycle_reason_message, claimed.next_reconcile_after, claimed.provision_attempts, claimed.delete_attempts, claimed.metadata, claimed.deleted_at, claimed.created_at, claimed.updated_at,
       claimed.lifecycle_changed_at, claimed.lifecycle_version,
       machine_grant.project_id AS grant_project_id,
       coalesce(binding.env_overlay, '{}'::jsonb) AS binding_env_overlay,
       coalesce(binding.secret_env_overlay, '{}'::jsonb) AS binding_secret_env_overlay
FROM claimed
LEFT JOIN project_machine_grants machine_grant ON machine_grant.org_id = claimed.org_id
  AND machine_grant.machine_id = claimed.id
  AND machine_grant.source_kind = 'pool'
LEFT JOIN agent_machine_bindings binding ON binding.org_id = claimed.org_id
  AND binding.machine_id = claimed.id
  AND binding.binding_kind = 'pool'
  AND binding.state = 'attached';

-- name: LockPoolMachineProvisioningResources :one
SELECT cpu, memory_mb, updated_at
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND machine_pool_id = sqlc.arg(machine_pool_id)
  AND source_kind = 'pool'
  AND lifecycle_state = 'provisioning'
  AND provision_attempts = sqlc.arg(provision_attempt)::integer
  AND deleted_at IS NULL
FOR UPDATE;

-- name: EnrichPoolMachineProvisioningResources :one
UPDATE machines
SET cpu = coalesce(cpu, sqlc.narg(cpu)::integer),
    memory_mb = coalesce(memory_mb, sqlc.narg(memory_mb)::integer),
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND machine_pool_id = sqlc.arg(machine_pool_id)
  AND source_kind = 'pool'
  AND lifecycle_state = 'provisioning'
  AND provision_attempts = sqlc.arg(provision_attempt)::integer
  AND deleted_at IS NULL
  AND (cpu IS NULL OR cpu IS NOT DISTINCT FROM sqlc.narg(cpu)::integer)
  AND (memory_mb IS NULL OR memory_mb IS NOT DISTINCT FROM sqlc.narg(memory_mb)::integer)
RETURNING cpu, memory_mb, updated_at;

-- name: RecordPoolMachineProvisioningResource :one
UPDATE machines
SET provider_resource_id = coalesce(provider_resource_id, sqlc.arg(provider_resource_id)::text),
    provider_runtime_mismatch_since = NULL,
    updated_at = CASE
      WHEN provider_resource_id IS NULL THEN statement_timestamp()
      ELSE updated_at
    END
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_state = 'provisioning'
  AND provision_attempts = sqlc.arg(provision_attempt)::integer
  AND provider_provision_attempted_at IS NOT NULL
  AND (provider_resource_id IS NULL OR provider_resource_id = sqlc.arg(provider_resource_id)::text)
RETURNING coalesce(provider_resource_id, '') AS provider_resource_id, updated_at;

-- name: CompletePoolMachineProvisioning :one
UPDATE machines
SET lifecycle_state = 'active',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = lifecycle_version + 1,
    sandbox_url = sqlc.narg(sandbox_url),
    lifecycle_reason_code = NULL,
    lifecycle_reason_message = '',
    next_reconcile_after = NULL,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_state = 'provisioning'
  AND provision_attempts = sqlc.arg(provision_attempt)::integer
  AND provider_resource_id = sqlc.arg(provider_resource_id)
  AND provider_provision_attempted_at IS NOT NULL
RETURNING id;

-- name: MarkPoolMachineProvisionFailed :one
UPDATE machines
SET lifecycle_state = 'provision_failed',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = lifecycle_version + 1,
    lifecycle_reason_code = sqlc.narg(lifecycle_reason_code),
    lifecycle_reason_message = sqlc.arg(lifecycle_reason_message),
    next_reconcile_after = statement_timestamp() + sqlc.arg(retry_delay_milliseconds)::bigint * interval '1 millisecond',
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_state = 'provisioning'
  AND provision_attempts = sqlc.arg(provision_attempt)::integer
RETURNING id;

-- name: ListPoolMachinesForProvisioning :many
SELECT id, org_id, machine_pool_id, source_kind, display_name, description, provider, lifecycle_state, provider_resource_id, provider_provision_attempted_at, 'offline'::text AS connection_state, last_observed_at, cpu, memory_mb, cwd, env, secret_env, provider_options, coalesce(idempotency_key, '') AS idempotency_key, coalesce(lifecycle_reason_code, '') AS lifecycle_reason_code, lifecycle_reason_message, next_reconcile_after, provision_attempts, delete_attempts, metadata, deleted_at, created_at, updated_at, lifecycle_changed_at, lifecycle_version
FROM machines
WHERE source_kind = 'pool'
  AND deleted_at IS NULL
  AND (
    (
      lifecycle_state = 'provision_failed'
      AND provision_attempts < sqlc.arg(max_provision_attempts)::integer
      AND next_reconcile_after <= transaction_timestamp()
    )
    OR (
      lifecycle_state = 'provisioning'
      AND provision_attempts < sqlc.arg(max_provision_attempts)::integer
      AND next_reconcile_after <= transaction_timestamp()
    )
  )
ORDER BY created_at, id
LIMIT sqlc.arg(limit_count)::integer;

-- name: ListPoolMachinesForCleanup :many
WITH cleanup_candidates AS (
  SELECT machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at,
         'offline'::text AS connection_state,
         machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version,
         CASE
           WHEN machine.lifecycle_state = 'delete_failed'
             AND machine.next_reconcile_after <= transaction_timestamp()
             THEN 'delete_failed_retry'
           WHEN machine.lifecycle_state = 'provisioning'
             AND machine.provision_attempts >= sqlc.arg(max_provision_attempts)::integer
             AND machine.next_reconcile_after <= transaction_timestamp()
             THEN 'provisioning_stale_cleanup'
           WHEN machine.lifecycle_state = 'deleting'
             AND machine.next_reconcile_after <= transaction_timestamp()
             THEN 'deleting_retry'
           WHEN machine.lifecycle_state = 'active'
             AND machine.provider_resource_id IS NOT NULL
             AND machine.lifecycle_changed_at <= transaction_timestamp() - sqlc.arg(stale_bootstrap_age_seconds)::bigint * interval '1 second'
             AND machine.current_daemon_runtime_id IS NULL
             THEN 'startup_or_daemon_bootstrap_failed'
           ELSE ''
         END AS cleanup_reason_code
  FROM machines machine
  WHERE machine.source_kind = 'pool'
    AND machine.deleted_at IS NULL
)
SELECT id, org_id, machine_pool_id, source_kind, display_name, description, provider, lifecycle_state, provider_resource_id, provider_provision_attempted_at,
       connection_state,
       last_observed_at, cpu, memory_mb, cwd, env, secret_env, provider_options, idempotency_key, lifecycle_reason_code, lifecycle_reason_message, next_reconcile_after, provision_attempts, delete_attempts, metadata, deleted_at, created_at, updated_at, lifecycle_changed_at, lifecycle_version,
       cleanup_reason_code
FROM cleanup_candidates
WHERE cleanup_reason_code <> ''
ORDER BY created_at, id
LIMIT sqlc.arg(limit_count)::integer;

-- name: LockPoolMachineDeletionAttemptForLifecycle :one
SELECT id, machine_pool_id
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND lifecycle_state = 'deleting'
  AND delete_attempts = sqlc.arg(delete_attempt)::integer
  AND deleted_at IS NULL
FOR UPDATE;

-- name: GetPoolMachinePoolIDForLifecycle :one
-- @sqlc-vet-disable machines-deleted-at
SELECT machine_pool_id
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND machine_pool_id IS NOT NULL;

-- name: DeletePoolMachine :one
UPDATE machines
SET lifecycle_state = 'deleted',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = lifecycle_version + 1,
    deleted_at = statement_timestamp(),
    lifecycle_reason_code = 'provider_deleted',
    lifecycle_reason_message = '',
    next_reconcile_after = NULL,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND lifecycle_state = 'deleting'
  AND delete_attempts = sqlc.arg(delete_attempt)::integer
  AND deleted_at IS NULL
RETURNING id;

-- name: MarkPoolMachineDeleting :one
UPDATE machines
SET lifecycle_state = 'deleting',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = lifecycle_version + 1,
    lifecycle_reason_code = sqlc.narg(lifecycle_reason_code),
    lifecycle_reason_message = sqlc.arg(lifecycle_reason_message),
    next_reconcile_after = statement_timestamp(),
    provider_runtime_mismatch_since = NULL,
    wake_attempt_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_version = sqlc.arg(expected_lifecycle_version)::bigint
  AND lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
RETURNING id, org_id, machine_pool_id, source_kind, display_name, description, provider, lifecycle_state, provider_resource_id, provider_provision_attempted_at, 'offline'::text AS connection_state, last_observed_at, cpu, memory_mb, cwd, env, secret_env, provider_options, coalesce(idempotency_key, '') AS idempotency_key, coalesce(lifecycle_reason_code, '') AS lifecycle_reason_code, lifecycle_reason_message, next_reconcile_after, provision_attempts, delete_attempts, metadata, deleted_at, created_at, updated_at, lifecycle_changed_at, lifecycle_version;

-- name: ClaimPoolMachineDeletion :one
WITH candidate AS MATERIALIZED (
  SELECT candidate_machine.id,
         CASE
           WHEN candidate_machine.provider_provision_attempted_at IS NOT NULL
             AND candidate_machine.lifecycle_state = 'delete_failed'
             AND candidate_machine.lifecycle_reason_code = 'provider_resource_not_found'
             AND candidate_machine.delete_attempts >= 3
             AND candidate_machine.provider_provision_attempted_at <= statement_timestamp() - sqlc.arg(missing_resource_finality_age_seconds)::bigint * interval '1 second'
             THEN true
           ELSE false
         END AS can_finalize_missing_provider_resource
  FROM machines candidate_machine
  WHERE candidate_machine.org_id = sqlc.arg(org_id)
    AND candidate_machine.id = sqlc.arg(id)
    AND candidate_machine.source_kind = 'pool'
    AND candidate_machine.deleted_at IS NULL
    AND candidate_machine.lifecycle_version = sqlc.arg(expected_lifecycle_version)::bigint
    AND (
      (
        candidate_machine.lifecycle_state = 'active'
        AND candidate_machine.provider_resource_id IS NOT NULL
        AND candidate_machine.lifecycle_changed_at <= statement_timestamp() - sqlc.arg(stale_bootstrap_age_seconds)::bigint * interval '1 second'
        AND candidate_machine.current_daemon_runtime_id IS NULL
      )
      OR (
        candidate_machine.lifecycle_state = 'provisioning'
        AND candidate_machine.provision_attempts >= sqlc.arg(max_provision_attempts)::integer
        AND candidate_machine.next_reconcile_after <= statement_timestamp()
      )
      OR (
        candidate_machine.lifecycle_state IN ('deleting', 'delete_failed')
        AND candidate_machine.next_reconcile_after <= statement_timestamp()
      )
    )
)
UPDATE machines machine
SET lifecycle_state = 'deleting',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = machine.lifecycle_version + 1,
    lifecycle_reason_code = sqlc.narg(lifecycle_reason_code),
    lifecycle_reason_message = sqlc.arg(lifecycle_reason_message),
    next_reconcile_after = statement_timestamp() + sqlc.arg(claim_timeout_seconds)::bigint * interval '1 second',
    delete_attempts = machine.delete_attempts + 1,
    provider_runtime_mismatch_since = CASE
      WHEN machine.lifecycle_state IN ('deleting', 'delete_failed')
        THEN machine.provider_runtime_mismatch_since
      ELSE NULL
    END,
    wake_attempt_expires_at = NULL,
    updated_at = statement_timestamp()
FROM candidate
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = candidate.id
RETURNING machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state, machine.provider_resource_id, machine.provider_provision_attempted_at, 'offline'::text AS connection_state, machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd, machine.env, machine.secret_env, machine.provider_options, coalesce(machine.idempotency_key, '') AS idempotency_key, coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code, machine.lifecycle_reason_message, machine.next_reconcile_after, machine.provision_attempts, machine.delete_attempts, machine.metadata, machine.deleted_at, machine.created_at, machine.updated_at, machine.lifecycle_changed_at, machine.lifecycle_version, candidate.can_finalize_missing_provider_resource;

-- name: RecordPoolMachineDeletionResource :one
UPDATE machines
SET provider_resource_id = coalesce(provider_resource_id, sqlc.arg(provider_resource_id)::text),
    updated_at = CASE
      WHEN provider_resource_id IS NULL THEN statement_timestamp()
      ELSE updated_at
    END
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_state = 'deleting'
  AND delete_attempts = sqlc.arg(delete_attempt)::integer
  AND provider_provision_attempted_at IS NOT NULL
  AND (provider_resource_id IS NULL OR provider_resource_id = sqlc.arg(provider_resource_id)::text)
RETURNING coalesce(provider_resource_id, '') AS provider_resource_id, updated_at;

-- name: FinalizePoolMachineDeletionClaim :one
UPDATE machines
SET next_reconcile_after = statement_timestamp() + sqlc.arg(claim_timeout_seconds)::bigint * interval '1 second',
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND lifecycle_state = 'deleting'
  AND deleted_at IS NULL
  AND lifecycle_version = sqlc.arg(expected_lifecycle_version)::bigint
  AND delete_attempts = sqlc.arg(delete_attempt)::integer
RETURNING next_reconcile_after, updated_at;

-- name: MarkMachineDeleteFailed :one
UPDATE machines
SET lifecycle_state = 'delete_failed',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = lifecycle_version + 1,
    lifecycle_reason_code = sqlc.narg(lifecycle_reason_code),
    lifecycle_reason_message = sqlc.arg(lifecycle_reason_message),
    next_reconcile_after = statement_timestamp() + sqlc.arg(retry_delay_milliseconds)::bigint * interval '1 millisecond',
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_state = 'deleting'
  AND delete_attempts = sqlc.arg(delete_attempt)::integer
RETURNING id;

-- name: DeletePoolProjectMachineGrantsForMachine :exec
DELETE FROM project_machine_grants
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND source_kind = 'pool';

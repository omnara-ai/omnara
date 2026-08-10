-- name: ListProviderRuntimeDiscoveryCandidates :many
SELECT scope.scope_key,
       machine.org_id,
       machine.id AS machine_id,
       machine.machine_pool_id,
       machine.provider,
       machine.provider_resource_id,
       machine.cpu,
       machine.memory_mb,
       coalesce(machine.provider_options, '{}'::jsonb) AS provider_options,
       machine.lifecycle_version,
       machine.current_daemon_runtime_id,
       inactivity.inactive_since,
       machine.provider_runtime_mismatch_since,
       machine.wake_attempt_expires_at,
       pool.management_kind,
       pool.provider_config,
       pool.provider_auth_secret_id,
       pool.provider_auth_env_var,
       credential.current_version_id AS provider_auth_version_id
FROM machines machine
JOIN machine_pools pool ON pool.org_id = machine.org_id
  AND pool.id = machine.machine_pool_id
  AND pool.deleted_at IS NULL
  AND pool.runtime_protection_enabled
JOIN daemon_runtimes runtime ON runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id
LEFT JOIN secrets credential ON credential.org_id = pool.org_id
  AND credential.id = pool.provider_auth_secret_id
  AND credential.deleted_at IS NULL
CROSS JOIN LATERAL (
  SELECT jsonb_build_array(
    machine.provider,
    pool.management_kind,
    CASE WHEN pool.management_kind = 'tenant' THEN pool.org_id::text END,
    pool.provider_auth_secret_id::text,
    credential.current_version_id::text,
    pool.provider_auth_env_var,
    pool.provider_config
  )::text AS scope_key
) scope
CROSS JOIN LATERAL (
  SELECT CASE
    WHEN machine.asleep_since IS NOT NULL THEN machine.asleep_since
    WHEN runtime.state = 'active' AND runtime.lease_expires_at <= statement_timestamp()
      THEN runtime.lease_expires_at
    WHEN runtime.state = 'ended'
      THEN LEAST(runtime.ended_at, runtime.lease_expires_at)
    ELSE NULL
  END::timestamptz AS inactive_since
) inactivity
WHERE machine.source_kind = 'pool'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND machine.provider_resource_id IS NOT NULL
  AND inactivity.inactive_since IS NOT NULL
  AND (
    NOT sqlc.arg(cursor_set)::boolean
    OR machine.id > sqlc.arg(after_machine_id)::uuid
  )
ORDER BY machine.id
LIMIT sqlc.arg(row_limit)::integer;

-- name: ListDueProviderRuntimeMismatches :many
SELECT scope.scope_key,
       machine.org_id,
       machine.id AS machine_id,
       machine.machine_pool_id,
       machine.provider,
       machine.provider_resource_id,
       machine.cpu,
       machine.memory_mb,
       coalesce(machine.provider_options, '{}'::jsonb) AS provider_options,
       machine.lifecycle_version,
       machine.current_daemon_runtime_id,
       inactivity.inactive_since,
       machine.provider_runtime_mismatch_since,
       machine.wake_attempt_expires_at,
       pool.management_kind,
       pool.provider_config,
       pool.provider_auth_secret_id,
       pool.provider_auth_env_var,
       credential.current_version_id AS provider_auth_version_id
FROM machines machine
JOIN machine_pools pool ON pool.org_id = machine.org_id
  AND pool.id = machine.machine_pool_id
  AND pool.deleted_at IS NULL
  AND pool.runtime_protection_enabled
JOIN daemon_runtimes runtime ON runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id
LEFT JOIN secrets credential ON credential.org_id = pool.org_id
  AND credential.id = pool.provider_auth_secret_id
  AND credential.deleted_at IS NULL
CROSS JOIN LATERAL (
  SELECT jsonb_build_array(
    machine.provider,
    pool.management_kind,
    CASE WHEN pool.management_kind = 'tenant' THEN pool.org_id::text END,
    pool.provider_auth_secret_id::text,
    credential.current_version_id::text,
    pool.provider_auth_env_var,
    pool.provider_config
  )::text AS scope_key
) scope
CROSS JOIN LATERAL (
  SELECT CASE
    WHEN machine.asleep_since IS NOT NULL THEN machine.asleep_since
    WHEN runtime.state = 'active' AND runtime.lease_expires_at <= statement_timestamp()
      THEN runtime.lease_expires_at
    WHEN runtime.state = 'ended'
      THEN LEAST(runtime.ended_at, runtime.lease_expires_at)
    ELSE NULL
  END::timestamptz AS inactive_since
) inactivity
WHERE machine.source_kind = 'pool'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND machine.provider_resource_id IS NOT NULL
  AND machine.provider_runtime_mismatch_since IS NOT NULL
  AND machine.provider_runtime_mismatch_since <= statement_timestamp()
      - sqlc.arg(confirmation_grace_milliseconds)::bigint * interval '1 millisecond'
  AND inactivity.inactive_since IS NOT NULL
  AND inactivity.inactive_since <= statement_timestamp()
      - sqlc.arg(inactivity_grace_milliseconds)::bigint * interval '1 millisecond'
  AND NOT EXISTS (
    SELECT 1
    FROM online_daemon_runtimes online
    WHERE online.org_id = machine.org_id
      AND online.machine_id = machine.id
  )
  AND (
    NOT sqlc.arg(cursor_set)::boolean
    OR (machine.provider_runtime_mismatch_since, machine.id) > (
      sqlc.arg(after_mismatch_since)::timestamptz,
      sqlc.arg(after_machine_id)::uuid
    )
  )
ORDER BY machine.provider_runtime_mismatch_since, machine.id
LIMIT sqlc.arg(row_limit)::integer;

-- name: MarkProviderRuntimeMismatch :one
UPDATE machines machine
SET provider_runtime_mismatch_since = statement_timestamp(),
    updated_at = statement_timestamp()
FROM machine_pools pool
CROSS JOIN daemon_runtimes runtime
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id)
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.source_kind = 'pool'
  AND machine.lifecycle_state = 'active'
  AND machine.lifecycle_version = sqlc.arg(lifecycle_version)::bigint
  AND machine.deleted_at IS NULL
  AND machine.provider = sqlc.arg(provider)
  AND machine.provider_resource_id = sqlc.arg(provider_resource_id)
  AND machine.provider_runtime_mismatch_since IS NULL
  AND machine.current_daemon_runtime_id = sqlc.arg(daemon_runtime_id)::uuid
  AND pool.org_id = machine.org_id
  AND pool.id = machine.machine_pool_id
  AND pool.deleted_at IS NULL
  AND pool.runtime_protection_enabled
  AND runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id
  AND (CASE
    WHEN machine.asleep_since IS NOT NULL THEN machine.asleep_since
    WHEN runtime.state = 'active' AND runtime.lease_expires_at <= statement_timestamp()
      THEN runtime.lease_expires_at
    WHEN runtime.state = 'ended'
      THEN LEAST(runtime.ended_at, runtime.lease_expires_at)
    ELSE NULL
  END) = sqlc.arg(inactive_since)::timestamptz
  AND NOT EXISTS (
    SELECT 1 FROM online_daemon_runtimes online
    WHERE online.org_id = machine.org_id AND online.machine_id = machine.id
  )
RETURNING machine.id;

-- name: ApplyProviderRuntimeInactiveObservation :one
UPDATE machines machine
SET provider_runtime_mismatch_since = NULL,
    wake_attempt_expires_at = CASE
      WHEN sqlc.arg(clear_active_wake)::boolean
        OR machine.wake_attempt_expires_at <= statement_timestamp()
        THEN NULL
      ELSE machine.wake_attempt_expires_at
    END,
    updated_at = statement_timestamp()
FROM daemon_runtimes runtime
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id)
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.source_kind = 'pool'
  AND machine.lifecycle_state = 'active'
  AND machine.lifecycle_version = sqlc.arg(lifecycle_version)::bigint
  AND machine.deleted_at IS NULL
  AND machine.provider = sqlc.arg(provider)
  AND machine.provider_resource_id = sqlc.arg(provider_resource_id)
  AND machine.provider_runtime_mismatch_since IS NOT DISTINCT FROM sqlc.narg(mismatch_since)::timestamptz
  AND machine.wake_attempt_expires_at IS NOT DISTINCT FROM sqlc.narg(wake_attempt_expires_at)::timestamptz
  AND (
    machine.provider_runtime_mismatch_since IS NOT NULL
    OR (
      machine.wake_attempt_expires_at IS NOT NULL
      AND (
        sqlc.arg(clear_active_wake)::boolean
        OR machine.wake_attempt_expires_at <= statement_timestamp()
      )
    )
  )
  AND machine.current_daemon_runtime_id = sqlc.arg(daemon_runtime_id)::uuid
  AND runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id
  AND (CASE
    WHEN machine.asleep_since IS NOT NULL THEN machine.asleep_since
    WHEN runtime.state = 'active' AND runtime.lease_expires_at <= statement_timestamp()
      THEN runtime.lease_expires_at
    WHEN runtime.state = 'ended'
      THEN LEAST(runtime.ended_at, runtime.lease_expires_at)
    ELSE NULL
  END) = sqlc.arg(inactive_since)::timestamptz
RETURNING machine.id,
  CASE WHEN sqlc.narg(wake_attempt_expires_at)::timestamptz IS NOT NULL
      AND machine.wake_attempt_expires_at IS NULL
    THEN true ELSE false
  END AS wake_attempt_cleared;

-- name: LockProviderRuntimeProtectionPool :one
SELECT pool.id
FROM machine_pools pool
WHERE pool.org_id = sqlc.arg(org_id)
  AND pool.id = sqlc.arg(machine_pool_id)
  AND pool.deleted_at IS NULL
  AND pool.runtime_protection_enabled
  AND pool.provider = sqlc.arg(provider)
  AND pool.management_kind = sqlc.arg(management_kind)
  AND pool.provider_config = sqlc.arg(provider_config)::jsonb
  AND pool.provider_auth_secret_id IS NOT DISTINCT FROM sqlc.narg(provider_auth_secret_id)::uuid
  AND pool.provider_auth_env_var = sqlc.arg(provider_auth_env_var)
FOR UPDATE OF pool;

-- name: LockProviderRuntimeCredential :one
SELECT credential.id
FROM secrets credential
WHERE credential.org_id = sqlc.arg(org_id)
  AND credential.id = sqlc.arg(provider_auth_secret_id)
  AND credential.management_kind = 'tenant'
  AND credential.current_version_id = sqlc.arg(provider_auth_version_id)
  AND credential.deleted_at IS NULL
FOR UPDATE;

-- name: ClaimProviderRuntimeMismatchDeletion :one
UPDATE machines machine
SET lifecycle_state = 'deleting',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = machine.lifecycle_version + 1,
    lifecycle_reason_code = 'provider_runtime_mismatch',
    lifecycle_reason_message = 'provider remained running after the Omnara daemon became inactive',
    next_reconcile_after = statement_timestamp()
      + sqlc.arg(claim_timeout_seconds)::bigint * interval '1 second',
    delete_attempts = machine.delete_attempts + 1,
    wake_attempt_expires_at = NULL,
    updated_at = statement_timestamp()
FROM daemon_runtimes runtime
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id)
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.source_kind = 'pool'
  AND machine.lifecycle_state = 'active'
  AND machine.lifecycle_version = sqlc.arg(lifecycle_version)::bigint
  AND machine.deleted_at IS NULL
  AND machine.provider = sqlc.arg(provider)
  AND machine.provider_resource_id = sqlc.arg(provider_resource_id)
  AND machine.provider_runtime_mismatch_since = sqlc.arg(mismatch_since)::timestamptz
  AND machine.provider_runtime_mismatch_since <= statement_timestamp()
      - sqlc.arg(confirmation_grace_milliseconds)::bigint * interval '1 millisecond'
  AND (
    machine.wake_attempt_expires_at IS NULL
    OR machine.wake_attempt_expires_at <= statement_timestamp()
  )
  AND machine.current_daemon_runtime_id = sqlc.arg(daemon_runtime_id)::uuid
  AND runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = machine.current_daemon_runtime_id
  AND (CASE
    WHEN machine.asleep_since IS NOT NULL THEN machine.asleep_since
    WHEN runtime.state = 'active' AND runtime.lease_expires_at <= statement_timestamp()
      THEN runtime.lease_expires_at
    WHEN runtime.state = 'ended'
      THEN LEAST(runtime.ended_at, runtime.lease_expires_at)
    ELSE NULL
  END) = sqlc.arg(inactive_since)::timestamptz
  AND sqlc.arg(inactive_since)::timestamptz <= statement_timestamp()
      - sqlc.arg(inactivity_grace_milliseconds)::bigint * interval '1 millisecond'
  AND NOT EXISTS (
    SELECT 1 FROM online_daemon_runtimes online
    WHERE online.org_id = machine.org_id AND online.machine_id = machine.id
  )
RETURNING machine.id, machine.org_id, machine.machine_pool_id, machine.source_kind,
          machine.display_name, machine.description, machine.provider,
          machine.lifecycle_state, machine.provider_resource_id,
          machine.provider_provision_attempted_at, 'offline'::text AS connection_state,
          machine.last_observed_at, machine.cpu, machine.memory_mb, machine.cwd,
          machine.env, machine.secret_env, machine.provider_options,
          coalesce(machine.idempotency_key, '') AS idempotency_key,
          coalesce(machine.lifecycle_reason_code, '') AS lifecycle_reason_code,
          machine.lifecycle_reason_message, machine.next_reconcile_after,
          machine.provision_attempts, machine.delete_attempts, machine.metadata,
          machine.deleted_at, machine.created_at, machine.updated_at,
          machine.lifecycle_changed_at, machine.lifecycle_version,
          false AS can_finalize_missing_provider_resource;

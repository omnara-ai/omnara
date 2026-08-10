-- name: ListVisibleMachineSourcesForPrincipal :many
WITH current_org_membership AS (
  SELECT om.role
  FROM org_memberships om
  WHERE om.org_id = sqlc.arg(org_id)
    AND (
      (sqlc.narg(user_id)::uuid IS NOT NULL AND om.user_id = sqlc.narg(user_id)::uuid)
      OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND om.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
    )
),
is_admin AS (
  SELECT EXISTS (
    SELECT 1
    FROM current_org_membership
    WHERE role IN ('owner', 'admin')
  ) AS value
),
visible_projects AS (
  SELECT DISTINCT roles.project_id
  FROM principal_project_authorization_roles roles
  WHERE roles.org_id = sqlc.arg(org_id)
    AND (
      (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
      OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
    )
    AND NOT (SELECT value FROM is_admin)
), visible_ids AS (
  SELECT m.id FROM machines m WHERE m.org_id = sqlc.arg(org_id) AND m.deleted_at IS NULL AND (SELECT value FROM is_admin)
  UNION
  SELECT pmg.machine_id FROM project_machine_grants pmg JOIN visible_projects vp ON vp.project_id = pmg.project_id
   WHERE pmg.org_id = sqlc.arg(org_id)
), listed AS (
  SELECT m.id,
   CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(m.display_name) WHEN 'created_at' THEN to_char(m.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'updated_at' THEN to_char(m.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'provider' THEN m.provider WHEN 'source_kind' THEN m.source_kind WHEN 'lifecycle_state' THEN m.lifecycle_state WHEN 'connection_state' THEN c.connection_state END::text AS sort_key,
   false AS sort_is_null
  FROM visible_ids v JOIN machines m ON m.id = v.id
  JOIN machine_connection_states c ON c.org_id = m.org_id AND c.machine_id = m.id
  WHERE (sqlc.arg(name_pattern)::text = '' OR m.display_name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
   AND (COALESCE(cardinality(sqlc.arg(providers)::text[]), 0) = 0 OR m.provider = ANY(sqlc.arg(providers)::text[]))
   AND (COALESCE(cardinality(sqlc.arg(source_kinds)::text[]), 0) = 0 OR m.source_kind = ANY(sqlc.arg(source_kinds)::text[]))
   AND (COALESCE(cardinality(sqlc.arg(lifecycle_states)::text[]), 0) = 0 OR m.lifecycle_state = ANY(sqlc.arg(lifecycle_states)::text[]))
   AND (COALESCE(cardinality(sqlc.arg(connection_states)::text[]), 0) = 0 OR c.connection_state = ANY(sqlc.arg(connection_states)::text[]))
   AND (sqlc.narg(machine_pool_id)::uuid IS NULL OR m.machine_pool_id = sqlc.narg(machine_pool_id)::uuid)
), visible_page AS (
 SELECT id, sort_key, sort_is_null FROM listed WHERE sqlc.arg(cursor_set)::boolean = false
  OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
  OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
  CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
 LIMIT sqlc.arg(row_limit)::bigint
),
sources AS (
  SELECT m.id, m.org_id, m.source_kind, m.display_name, m.description, m.provider, m.lifecycle_state,
         connection.connection_state,
         m.last_observed_at, m.deleted_at, m.created_at, m.updated_at, page.sort_key, page.sort_is_null,
         'org_role'::text AS access_source_kind, NULL::uuid AS access_project_id, NULL::uuid AS access_grant_id, ''::text AS access_grant_source_kind, true AS can_manage
  FROM visible_page page
  JOIN machines m ON m.id = page.id
  JOIN machine_connection_states connection ON connection.org_id = m.org_id AND connection.machine_id = m.id
  WHERE EXISTS (
    SELECT 1
    FROM is_admin
    WHERE value
  )
  UNION ALL
  SELECT m.id, m.org_id, m.source_kind, m.display_name, m.description, m.provider, m.lifecycle_state,
         connection.connection_state,
         m.last_observed_at, m.deleted_at, m.created_at, m.updated_at, page.sort_key, page.sort_is_null,
         'project_machine_grant'::text AS access_source_kind, pmg.project_id AS access_project_id, pmg.id AS access_grant_id, pmg.source_kind AS access_grant_source_kind, false AS can_manage
  FROM visible_page page
  JOIN project_machine_grants pmg
    ON pmg.org_id = sqlc.arg(org_id)
   AND pmg.machine_id = page.id
  JOIN machines m ON m.org_id = pmg.org_id AND m.id = pmg.machine_id AND m.deleted_at IS NULL AND m.lifecycle_state = 'active'
  JOIN machine_connection_states connection ON connection.org_id = m.org_id AND connection.machine_id = m.id
  WHERE (SELECT value FROM is_admin)
    OR EXISTS (
      SELECT 1
      FROM visible_projects vp
      WHERE vp.project_id = pmg.project_id
    )
)
SELECT sources.id, sources.org_id, sources.source_kind, sources.display_name, sources.description, sources.provider, sources.lifecycle_state, sources.connection_state, sources.last_observed_at, sources.deleted_at, sources.created_at, sources.updated_at, sources.access_source_kind, sources.access_project_id, sources.access_grant_id, sources.access_grant_source_kind, sources.can_manage, sources.sort_key, sources.sort_is_null
FROM sources
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sources.sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sources.sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sources.id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sources.id END DESC,
 sources.access_source_kind, sources.access_project_id, sources.access_grant_id;

-- name: DeleteMachine :one
UPDATE machines
SET lifecycle_state = 'deleted',
    lifecycle_changed_at = statement_timestamp(),
    lifecycle_version = lifecycle_version + 1,
    deleted_at = statement_timestamp(),
    lifecycle_reason_code = sqlc.arg(reason),
    lifecycle_reason_message = sqlc.arg(message),
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND source_kind = 'byo' AND deleted_at IS NULL
RETURNING id, org_id, machine_pool_id, source_kind, display_name, description, provider, lifecycle_state, provider_resource_id, provider_provision_attempted_at, 'offline'::text AS connection_state, last_observed_at, cpu, memory_mb, cwd, env, secret_env, provider_options, coalesce(idempotency_key, '') AS idempotency_key, coalesce(lifecycle_reason_code, '') AS lifecycle_reason_code, lifecycle_reason_message, next_reconcile_after, provision_attempts, delete_attempts, metadata, deleted_at, created_at, updated_at, lifecycle_changed_at, lifecycle_version;

-- name: LockMachineEnvironmentKey :exec
SELECT pg_advisory_xact_lock(
    hashtextextended('machine_environment:' || sqlc.arg(machine_id)::uuid::text, 0)
);

-- name: LockMachineExecutionDefaults :one
SELECT cwd, env, secret_env
FROM machines
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- name: UpdateMachineExecutionDefaults :execrows
UPDATE machines
SET cwd = sqlc.arg(cwd),
    env = sqlc.arg(env),
    secret_env = sqlc.arg(secret_env),
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: DeleteProjectMachineGrantsForMachine :exec
DELETE FROM project_machine_grants
WHERE org_id = sqlc.arg(org_id) AND machine_id = sqlc.arg(machine_id);

-- name: UpsertProjectMachineGrant :one
INSERT INTO project_machine_grants(org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, idempotency_key, metadata, created_at, updated_at)
SELECT project.org_id, project.id, machine.id, sqlc.arg(source_kind), sqlc.narg(project_machine_pool_grant_id)::uuid, sqlc.arg(description), sqlc.narg(idempotency_key), sqlc.arg(metadata), statement_timestamp(), statement_timestamp()
FROM projects project
JOIN machines machine ON machine.org_id = project.org_id
  AND machine.id = sqlc.arg(machine_id)
  AND machine.deleted_at IS NULL
WHERE project.org_id = sqlc.arg(org_id)
  AND project.id = sqlc.arg(project_id)
  AND project.deleted_at IS NULL
  AND (
    (
      sqlc.arg(source_kind)::text = 'explicit'
      AND machine.source_kind = 'byo'
      AND machine.lifecycle_state = 'active'
    )
    OR EXISTS (
      SELECT 1
      FROM project_machine_pool_grants pool_grant
      WHERE pool_grant.project_id = project.id
        AND pool_grant.id = sqlc.narg(project_machine_pool_grant_id)::uuid
        AND pool_grant.machine_pool_id = machine.machine_pool_id
        AND sqlc.arg(source_kind)::text = 'pool'
        AND machine.source_kind = 'pool'
        AND machine.lifecycle_state IN ('provisioning', 'provision_failed', 'active')
    )
  )
ON CONFLICT (project_id, machine_id) DO NOTHING
RETURNING id, org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at;

-- name: UpdateMachineObservation :exec
-- @sqlc-vet-disable machines-deleted-at
-- lifecycle_state = 'active' excludes soft-deleted machines.
UPDATE machines
SET last_observed_at = statement_timestamp(),
    provider_runtime_mismatch_since = NULL,
    wake_attempt_expires_at = NULL,
    metadata = CASE
      WHEN sqlc.arg(observed_platform)::jsonb = '{}'::jsonb THEN metadata
      ELSE jsonb_set(metadata, '{observed_platform}', sqlc.arg(observed_platform)::jsonb, true)
    END,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND lifecycle_state = 'active';

-- name: GetProjectMachineGrant :one
SELECT id, org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at
FROM project_machine_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND id = sqlc.arg(id);

-- name: GetProjectMachineGrantByIdempotency :one
SELECT id, org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at
FROM project_machine_grants
WHERE project_id = sqlc.arg(project_id) AND idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: GetActiveProjectMachineGrantForMachineName :one
SELECT pmg.id, pmg.org_id, pmg.project_id, pmg.machine_id, pmg.source_kind, pmg.project_machine_pool_grant_id, pmg.description, coalesce(pmg.idempotency_key, '') AS idempotency_key, pmg.metadata, pmg.created_at, pmg.updated_at
FROM project_machine_grants pmg
JOIN machines machine ON machine.org_id = pmg.org_id
  AND machine.id = pmg.machine_id
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state = 'active'
  AND machine.source_kind = 'byo'
WHERE pmg.project_id = sqlc.arg(project_id)
  AND pmg.source_kind = 'explicit'
  AND machine.display_name = sqlc.arg(machine_name);

-- name: GetActiveProjectMachineGrantForMachine :one
SELECT pmg.id, pmg.org_id, pmg.project_id, pmg.machine_id, pmg.source_kind, pmg.project_machine_pool_grant_id, pmg.description, coalesce(pmg.idempotency_key, '') AS idempotency_key, pmg.metadata, pmg.created_at, pmg.updated_at,
       machine.env AS machine_env, machine.secret_env AS machine_secret_env
FROM project_machine_grants pmg
JOIN machines machine ON machine.org_id = pmg.org_id
  AND machine.id = pmg.machine_id
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state = 'active'
  AND machine.source_kind = 'byo'
WHERE pmg.project_id = sqlc.arg(project_id)
  AND pmg.machine_id = sqlc.arg(machine_id)
  AND pmg.source_kind = 'explicit'
;

-- name: ListProjectMachineGrants :many
WITH listed AS (
 SELECT g.id, g.org_id, g.project_id, g.machine_id, g.source_kind, g.project_machine_pool_grant_id, g.description, coalesce(g.idempotency_key, '') AS idempotency_key, g.metadata, g.created_at, g.updated_at,
 m.source_kind AS machine_source_kind, m.display_name, m.description AS machine_description, m.provider, m.lifecycle_state, c.connection_state, m.last_observed_at, m.deleted_at, m.created_at AS machine_created_at, m.updated_at AS machine_updated_at,
 CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(m.display_name) WHEN 'created_at' THEN to_char(g.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'updated_at' THEN to_char(g.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'source_kind' THEN g.source_kind WHEN 'provider' THEN m.provider WHEN 'lifecycle_state' THEN m.lifecycle_state WHEN 'connection_state' THEN c.connection_state END::text AS sort_key, false AS sort_is_null
 FROM project_machine_grants g JOIN machines m ON m.org_id = g.org_id AND m.id = g.machine_id
 JOIN machine_connection_states c ON c.org_id = m.org_id AND c.machine_id = m.id
 WHERE g.org_id = sqlc.arg(org_id) AND g.project_id = sqlc.arg(project_id)
  AND (sqlc.arg(name_pattern)::text = '' OR m.display_name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (sqlc.narg(machine_id)::uuid IS NULL OR m.id = sqlc.narg(machine_id)::uuid)
  AND (COALESCE(cardinality(sqlc.arg(source_kinds)::text[]), 0) = 0 OR g.source_kind = ANY(sqlc.arg(source_kinds)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(providers)::text[]), 0) = 0 OR m.provider = ANY(sqlc.arg(providers)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(lifecycle_states)::text[]), 0) = 0 OR m.lifecycle_state = ANY(sqlc.arg(lifecycle_states)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(connection_states)::text[]), 0) = 0 OR c.connection_state = ANY(sqlc.arg(connection_states)::text[]))
)
SELECT id, org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id,
 description, idempotency_key, metadata, created_at,
 updated_at, machine_source_kind, display_name, machine_description, provider, lifecycle_state,
 connection_state, last_observed_at, deleted_at, machine_created_at, machine_updated_at,
 sort_key, sort_is_null
FROM listed WHERE sqlc.arg(cursor_set)::boolean = false
 OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListProjectVisibleMachines :many
WITH listed AS (
SELECT machine.id, machine.org_id, machine.source_kind, machine.display_name, machine.description, machine.provider, machine.lifecycle_state,
       connection.connection_state,
       machine.last_observed_at, machine.deleted_at, machine.created_at, machine.updated_at,
       pmg.id AS grant_id, pmg.source_kind AS grant_source_kind,
       EXISTS (
         SELECT 1
         FROM org_memberships om
         WHERE om.org_id = machine.org_id
           AND (
             (sqlc.narg(user_id)::uuid IS NOT NULL AND om.user_id = sqlc.narg(user_id)::uuid)
             OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND om.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
           )
           AND om.role IN ('owner', 'admin')
       ) AS can_manage,
       CASE sqlc.arg(sort_field)::text WHEN 'name' THEN lower(machine.display_name) WHEN 'created_at' THEN to_char(machine.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'updated_at' THEN to_char(machine.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') WHEN 'provider' THEN machine.provider WHEN 'source_kind' THEN machine.source_kind WHEN 'lifecycle_state' THEN machine.lifecycle_state WHEN 'connection_state' THEN connection.connection_state END::text AS sort_key,
       false AS sort_is_null
FROM project_machine_grants pmg
JOIN machines machine
  ON machine.org_id = pmg.org_id
 AND machine.id = pmg.machine_id
 AND machine.deleted_at IS NULL
 AND machine.lifecycle_state = 'active'
JOIN machine_connection_states connection ON connection.org_id = machine.org_id AND connection.machine_id = machine.id
WHERE pmg.org_id = sqlc.arg(org_id)
  AND pmg.project_id = sqlc.arg(project_id)
  AND (sqlc.arg(name_pattern)::text = '' OR machine.display_name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (COALESCE(cardinality(sqlc.arg(providers)::text[]), 0) = 0 OR machine.provider = ANY(sqlc.arg(providers)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(source_kinds)::text[]), 0) = 0 OR machine.source_kind = ANY(sqlc.arg(source_kinds)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(lifecycle_states)::text[]), 0) = 0 OR machine.lifecycle_state = ANY(sqlc.arg(lifecycle_states)::text[]))
  AND (COALESCE(cardinality(sqlc.arg(connection_states)::text[]), 0) = 0 OR connection.connection_state = ANY(sqlc.arg(connection_states)::text[]))
  AND (sqlc.narg(machine_pool_id)::uuid IS NULL OR machine.machine_pool_id = sqlc.narg(machine_pool_id)::uuid)
  AND EXISTS (
    SELECT 1
    FROM principal_project_authorization_roles roles
    WHERE roles.org_id = pmg.org_id
      AND roles.project_id = pmg.project_id
      AND (
        (sqlc.narg(user_id)::uuid IS NOT NULL AND roles.user_id = sqlc.narg(user_id)::uuid)
        OR (sqlc.narg(org_api_key_id)::uuid IS NOT NULL AND roles.org_api_key_id = sqlc.narg(org_api_key_id)::uuid)
      )
  )
)
SELECT id, org_id, source_kind, display_name, description, provider, lifecycle_state,
 connection_state, last_observed_at, deleted_at, created_at, updated_at, grant_id,
 grant_source_kind, can_manage, sort_key, sort_is_null
FROM listed WHERE sqlc.arg(cursor_set)::boolean = false
 OR (sqlc.arg(sort_desc)::boolean = false AND (sort_key, id) > (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
 OR (sqlc.arg(sort_desc)::boolean = true AND (sort_key, id) < (sqlc.arg(cursor_key)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN sort_key END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN sort_key END DESC,
 CASE WHEN NOT sqlc.arg(sort_desc)::boolean THEN id END ASC, CASE WHEN sqlc.arg(sort_desc)::boolean THEN id END DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListActiveProjectMachineGrantsForMachine :many
SELECT pmg.id, pmg.org_id, pmg.project_id, pmg.machine_id, pmg.source_kind, pmg.project_machine_pool_grant_id, pmg.description, coalesce(pmg.idempotency_key, '') AS idempotency_key, pmg.metadata, pmg.created_at, pmg.updated_at
FROM project_machine_grants pmg
JOIN machines machine ON machine.org_id = pmg.org_id AND machine.id = pmg.machine_id AND machine.deleted_at IS NULL AND machine.lifecycle_state = 'active'
WHERE pmg.org_id = sqlc.arg(org_id) AND pmg.machine_id = sqlc.arg(machine_id)
ORDER BY pmg.project_id, pmg.id;

-- name: DeleteProjectMachineGrant :one
DELETE FROM project_machine_grants
WHERE org_id = sqlc.arg(org_id) AND project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND source_kind = 'explicit'
RETURNING id, org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id, description, coalesce(idempotency_key, '') AS idempotency_key, metadata, created_at, updated_at;

-- name: CreateBYOMachineDaemonToken :one
INSERT INTO machine_daemon_tokens(org_id, machine_id, name, token_hash, metadata, created_at)
SELECT machine.org_id, machine.id, sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(metadata), statement_timestamp()
FROM machines machine
WHERE machine.org_id = sqlc.arg(org_id) AND machine.id = sqlc.arg(machine_id) AND machine.deleted_at IS NULL AND machine.lifecycle_state = 'active' AND machine.source_kind = 'byo'
RETURNING id, org_id, machine_id, name, token_hash, metadata, created_at, last_used_at, revoked_at, revoke_reason;

-- name: BeginPoolMachineProviderProvisioning :one
WITH provisioning_attempt AS (
  UPDATE machines machine
  SET provider_provision_attempted_at = statement_timestamp(),
      updated_at = statement_timestamp()
  WHERE machine.org_id = sqlc.arg(org_id)
    AND machine.id = sqlc.arg(machine_id)
    AND machine.deleted_at IS NULL
    AND machine.source_kind = 'pool'
    AND machine.lifecycle_state = 'provisioning'
    AND machine.provision_attempts = sqlc.arg(provision_attempt)::integer
  RETURNING machine.org_id, machine.id, machine.provider_provision_attempted_at, machine.updated_at
),
created_token AS (
  INSERT INTO machine_daemon_tokens(org_id, machine_id, name, token_hash, metadata, created_at)
  SELECT attempt.org_id, attempt.id, sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(metadata), statement_timestamp()
  FROM provisioning_attempt attempt
  RETURNING id, org_id, machine_id, name, token_hash, metadata, created_at, last_used_at, revoked_at, revoke_reason
)
SELECT created_token.id, created_token.org_id, created_token.machine_id, created_token.name,
       created_token.token_hash, created_token.metadata,
       created_token.created_at, created_token.last_used_at, created_token.revoked_at,
       created_token.revoke_reason, provisioning_attempt.provider_provision_attempted_at,
       provisioning_attempt.updated_at
FROM created_token
JOIN provisioning_attempt
  ON provisioning_attempt.org_id = created_token.org_id
 AND provisioning_attempt.id = created_token.machine_id;

-- name: ListBYOMachineDaemonTokens :many
SELECT token.id, token.org_id, token.machine_id, token.name, token.token_hash, token.metadata, token.created_at, token.last_used_at, token.revoked_at, token.revoke_reason
FROM machine_daemon_tokens token
JOIN machines machine ON machine.org_id = token.org_id AND machine.id = token.machine_id
WHERE token.org_id = sqlc.arg(org_id) AND token.machine_id = sqlc.arg(machine_id)
  AND machine.deleted_at IS NULL
  AND machine.source_kind = 'byo'
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (token.created_at, token.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY token.created_at DESC, token.id DESC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListAllMachineDaemonTokens :many
SELECT id, org_id, machine_id, name, token_hash, metadata, created_at, last_used_at, revoked_at, revoke_reason
FROM machine_daemon_tokens
WHERE org_id = sqlc.arg(org_id) AND machine_id = sqlc.arg(machine_id)
ORDER BY created_at, id;

-- name: AuthenticateMachineDaemonToken :one
WITH authenticated AS MATERIALIZED (
  SELECT token.id, token.org_id, token.machine_id, token.last_used_at
  FROM machine_daemon_tokens token
  JOIN machines machine ON machine.org_id = token.org_id AND machine.id = token.machine_id
  WHERE token.token_hash = sqlc.arg(token_hash)
    AND token.revoked_at IS NULL
    AND machine.deleted_at IS NULL
    AND (
      machine.lifecycle_state = 'active'
      OR (
        machine.source_kind = 'pool'
        AND machine.lifecycle_state IN ('provisioning', 'provision_failed')
      )
    )
  LIMIT 1
), touched AS (
  UPDATE machine_daemon_tokens token
  SET last_used_at = transaction_timestamp()
  FROM authenticated
  WHERE token.id = authenticated.id
    AND token.revoked_at IS NULL
    AND (
      token.last_used_at IS NULL
      OR token.last_used_at < transaction_timestamp() - (sqlc.arg(touch_interval_seconds)::bigint * interval '1 second')
    )
  RETURNING token.id
)
SELECT id, org_id, machine_id, last_used_at
FROM authenticated;

-- name: ValidateMachineDaemonBootstrap :one
SELECT installation.id AS installation_id,
       machine.id AS machine_id,
       machine.org_id AS org_id,
       token.id AS daemon_token_id
FROM machine_daemon_tokens token
JOIN machines machine ON machine.org_id = token.org_id AND machine.id = token.machine_id
CROSS JOIN installation
WHERE token.org_id = sqlc.arg(org_id)
  AND token.machine_id = sqlc.arg(machine_id)
  AND token.id = sqlc.arg(daemon_token_id)
  AND token.revoked_at IS NULL
  AND machine.deleted_at IS NULL
  AND (
    machine.lifecycle_state = 'active'
    OR (
      machine.source_kind = 'pool'
      AND machine.lifecycle_state IN ('provisioning', 'provision_failed')
    )
  );

-- name: RecordMachineFailureReport :one
UPDATE machines machine
SET failure_report = jsonb_build_object(
      'stage', sqlc.arg(stage)::text,
      'exit_status', sqlc.arg(exit_status)::integer,
      'output_tail', sqlc.arg(output_tail)::text,
      'output_truncated', sqlc.arg(output_truncated)::boolean,
      'reported_at', statement_timestamp()
    ),
    updated_at = statement_timestamp()
FROM machine_daemon_tokens token
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id)
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state IN ('provisioning', 'provision_failed', 'active')
  AND token.org_id = machine.org_id
  AND token.machine_id = machine.id
  AND token.id = sqlc.arg(daemon_token_id)
  AND token.revoked_at IS NULL
RETURNING machine.failure_report;

-- name: RevokeBYOMachineDaemonToken :one
UPDATE machine_daemon_tokens
SET revoked_at = statement_timestamp(), revoke_reason = sqlc.arg(reason)
WHERE machine_daemon_tokens.org_id = sqlc.arg(org_id)
  AND machine_daemon_tokens.machine_id = sqlc.arg(machine_id)
  AND machine_daemon_tokens.id = sqlc.arg(id)
  AND machine_daemon_tokens.revoked_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM machines machine
    WHERE machine.org_id = machine_daemon_tokens.org_id
      AND machine.id = machine_daemon_tokens.machine_id
      AND machine.deleted_at IS NULL
      AND machine.source_kind = 'byo'
  )
RETURNING id, org_id, machine_id, name, token_hash, metadata, created_at, last_used_at, revoked_at, revoke_reason;

-- name: RevokeMachineDaemonTokensForMachine :exec
UPDATE machine_daemon_tokens
SET revoked_at = statement_timestamp(),
    revoke_reason = sqlc.arg(reason)
WHERE org_id = sqlc.arg(org_id)
  AND machine_id = sqlc.arg(machine_id)
  AND revoked_at IS NULL;

-- name: RevokeSiblingSystemMachineDaemonTokens :exec
UPDATE machine_daemon_tokens
SET revoked_at = statement_timestamp(),
    revoke_reason = 'replaced_by_registered_runtime'
WHERE machine_daemon_tokens.org_id = sqlc.arg(org_id)
  AND machine_daemon_tokens.machine_id = sqlc.arg(machine_id)
  AND machine_daemon_tokens.revoked_at IS NULL
  AND machine_daemon_tokens.id <> sqlc.arg(active_token_id)
  AND EXISTS (
    SELECT 1
    FROM machines machine
    WHERE machine.org_id = machine_daemon_tokens.org_id
      AND machine.id = machine_daemon_tokens.machine_id
      AND machine.source_kind = 'pool'
      AND machine.deleted_at IS NULL
  );

-- name: LockMachineForRuntimeRegistration :one
SELECT id, current_daemon_runtime_id
FROM machines
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL AND lifecycle_state = 'active'
FOR UPDATE;

-- name: LockMachineForLifecycle :one
-- @sqlc-vet-disable machines-deleted-at
-- Lifecycle lock covers soft-deleted machines during teardown.
SELECT id
FROM machines
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ListActiveDaemonRuntimesForUpdate :many
SELECT id, org_id, machine_id, daemon_token_id, daemon_instance_id, daemon_version, state, coalesce(state_reason_code, '') AS state_reason_code, state_reason_message, capacity, metadata, created_at, last_seen_at, lease_expires_at, ended_at, updated_at
FROM daemon_runtimes
WHERE org_id = sqlc.arg(org_id) AND machine_id = sqlc.arg(machine_id) AND state = 'active'
FOR UPDATE;

-- name: GetDaemonRuntimeInstanceForUpdate :one
SELECT runtime.id,
  runtime.state,
  coalesce(runtime.state_reason_code, '') AS state_reason_code,
  runtime.daemon_version
FROM daemon_runtimes runtime
WHERE runtime.org_id = sqlc.arg(org_id)
  AND runtime.machine_id = sqlc.arg(machine_id)
  AND runtime.daemon_instance_id = sqlc.arg(daemon_instance_id)
FOR UPDATE OF runtime;

-- name: ListExpiredDaemonRuntimeCandidates :many
SELECT id, org_id, machine_id
FROM daemon_runtimes
WHERE state = 'active'
  AND lease_expires_at <= statement_timestamp()
ORDER BY lease_expires_at ASC, id ASC
LIMIT sqlc.arg(batch_limit);

-- name: EndExpiredDaemonRuntime :one
UPDATE daemon_runtimes runtime
SET state = 'ended',
    ended_at = statement_timestamp(),
    state_reason_code = 'daemon_lease_expired',
    state_reason_message = '',
    updated_at = statement_timestamp()
WHERE runtime.id = sqlc.arg(id)
  AND runtime.org_id = sqlc.arg(org_id)
  AND runtime.machine_id = sqlc.arg(machine_id)
  AND runtime.state = 'active'
  AND runtime.lease_expires_at <= statement_timestamp()
RETURNING runtime.id, runtime.org_id, runtime.machine_id, runtime.daemon_token_id, runtime.daemon_instance_id, runtime.daemon_version, runtime.state, coalesce(runtime.state_reason_code, '') AS state_reason_code, runtime.state_reason_message, runtime.capacity, runtime.metadata, runtime.created_at, runtime.last_seen_at, runtime.lease_expires_at, runtime.ended_at, runtime.updated_at;

-- name: EndDaemonRuntime :one
UPDATE daemon_runtimes
SET state = 'ended', ended_at = statement_timestamp(), state_reason_code = sqlc.arg(reason), state_reason_message = sqlc.arg(message), updated_at = statement_timestamp()
WHERE daemon_runtimes.org_id = sqlc.arg(org_id) AND daemon_runtimes.machine_id = sqlc.arg(machine_id) AND daemon_runtimes.id = sqlc.arg(id) AND daemon_runtimes.state = 'active'
  AND EXISTS (
    SELECT 1
    FROM machine_daemon_tokens token
    JOIN machines machine ON machine.org_id = token.org_id AND machine.id = token.machine_id
    WHERE token.org_id = daemon_runtimes.org_id
      AND token.machine_id = daemon_runtimes.machine_id
      AND token.id = sqlc.arg(daemon_token_id)
      AND daemon_runtimes.daemon_token_id = token.id
      AND machine.current_daemon_runtime_id = daemon_runtimes.id
      AND token.revoked_at IS NULL
      AND machine.deleted_at IS NULL
      AND machine.lifecycle_state = 'active'
  )
RETURNING id, org_id, machine_id, daemon_token_id, daemon_instance_id, daemon_version, state, coalesce(state_reason_code, '') AS state_reason_code, state_reason_message, capacity, metadata, created_at, last_seen_at, lease_expires_at, ended_at, updated_at;

-- name: ForceEndDaemonRuntime :one
UPDATE daemon_runtimes
SET state = 'ended', ended_at = statement_timestamp(), state_reason_code = sqlc.arg(reason), state_reason_message = sqlc.arg(message), updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id) AND machine_id = sqlc.arg(machine_id) AND id = sqlc.arg(id) AND state = 'active'
RETURNING id, org_id, machine_id, daemon_token_id, daemon_instance_id, daemon_version, state, coalesce(state_reason_code, '') AS state_reason_code, state_reason_message, capacity, metadata, created_at, last_seen_at, lease_expires_at, ended_at, updated_at;

-- name: MachineHasUnfinishedDaemonWork :one
SELECT EXISTS (
  SELECT 1
  FROM processes process
  WHERE process.org_id = sqlc.arg(org_id)
    AND process.machine_id = sqlc.arg(machine_id)
    AND process.state IN ('queued', 'starting', 'running')
) OR EXISTS (
  SELECT 1
  FROM process_actions action
  JOIN processes process ON process.project_id = action.project_id
    AND process.agent_id = action.agent_id
    AND process.id = action.process_id
  WHERE process.org_id = sqlc.arg(org_id)
    AND process.machine_id = sqlc.arg(machine_id)
    AND action.state IN ('queued', 'accepted')
) AS has_unfinished_work;

-- name: MarkMachineAsleep :one
UPDATE machines
SET asleep_since = statement_timestamp(),
    wake_attempt_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND lifecycle_state = 'active'
  AND sandbox_url IS NOT NULL
RETURNING id;

-- name: ClearMachineSleep :exec
UPDATE machines
SET asleep_since = NULL,
    wake_attempt_expires_at = NULL,
    updated_at = statement_timestamp()
WHERE org_id = sqlc.arg(org_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND asleep_since IS NOT NULL;

-- name: GetMachineWakeState :one
SELECT EXISTS (
         SELECT 1
         FROM online_daemon_runtimes runtime
         WHERE runtime.org_id = machine.org_id
           AND runtime.machine_id = machine.id
       ) AS online,
       (machine.asleep_since IS NOT NULL)::boolean AS asleep,
       pool.runtime_protection_enabled,
       machine.wake_attempt_expires_at,
       coalesce(
         machine.wake_attempt_expires_at > statement_timestamp(),
         false
       )::boolean AS wake_attempt_active
FROM machines machine
JOIN machine_pools pool ON pool.org_id = machine.org_id
  AND pool.id = machine.machine_pool_id
  AND pool.deleted_at IS NULL
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id)
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.source_kind = 'pool'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND machine.provider_resource_id IS NOT NULL;

-- name: ClaimMachineWakeRequest :one
UPDATE machines machine
SET wake_attempt_expires_at = statement_timestamp()
      + sqlc.arg(wake_timeout_milliseconds)::bigint * interval '1 millisecond',
    updated_at = statement_timestamp()
FROM machine_pools pool
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id)
  AND machine.machine_pool_id = sqlc.arg(machine_pool_id)::uuid
  AND machine.source_kind = 'pool'
  AND machine.lifecycle_state = 'active'
  AND machine.deleted_at IS NULL
  AND machine.asleep_since IS NOT NULL
  AND machine.provider_resource_id IS NOT NULL
  AND (
    machine.wake_attempt_expires_at IS NULL
    OR (
      NOT pool.runtime_protection_enabled
      AND machine.wake_attempt_expires_at <= statement_timestamp()
    )
  )
  AND pool.org_id = machine.org_id
  AND pool.id = machine.machine_pool_id
  AND pool.deleted_at IS NULL
RETURNING machine.wake_attempt_expires_at;

-- name: RefreshDaemonRuntimeRegistration :one
UPDATE daemon_runtimes runtime
SET daemon_token_id = token.id,
    state = 'active',
    last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + (sqlc.arg(lease_timeout_milliseconds)::bigint * interval '1 millisecond'),
    ended_at = NULL,
    state_reason_code = NULL,
    state_reason_message = '',
    capacity = sqlc.arg(capacity),
    metadata = sqlc.arg(metadata),
    updated_at = statement_timestamp()
FROM machine_daemon_tokens token
JOIN machines machine ON machine.org_id = token.org_id
  AND machine.id = token.machine_id
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state = 'active'
WHERE runtime.org_id = sqlc.arg(org_id)
	AND runtime.machine_id = sqlc.arg(machine_id)
	AND runtime.daemon_instance_id = sqlc.arg(daemon_instance_id)
	AND machine.current_daemon_runtime_id = runtime.id
  AND (runtime.state = 'active' OR (runtime.state = 'ended' AND runtime.state_reason_code IN ('daemon_lease_expired', 'machine_asleep')))
  AND token.org_id = runtime.org_id AND token.machine_id = runtime.machine_id AND token.id = sqlc.arg(daemon_token_id) AND token.revoked_at IS NULL
RETURNING runtime.id;

-- name: InsertDaemonRuntime :one
INSERT INTO daemon_runtimes(org_id, machine_id, daemon_token_id, daemon_instance_id, daemon_version, state, capacity, metadata, created_at, last_seen_at, lease_expires_at, updated_at)
SELECT token.org_id,
  token.machine_id,
  token.id,
  sqlc.arg(daemon_instance_id),
  sqlc.arg(daemon_version),
  'active',
  sqlc.arg(capacity),
  sqlc.arg(metadata),
  statement_timestamp(),
  statement_timestamp(),
  statement_timestamp() + (sqlc.arg(lease_timeout_milliseconds)::bigint * interval '1 millisecond'),
  statement_timestamp()
FROM machine_daemon_tokens token
JOIN machines machine ON machine.org_id = token.org_id AND machine.id = token.machine_id
WHERE token.org_id = sqlc.arg(org_id) AND token.machine_id = sqlc.arg(machine_id) AND token.id = sqlc.arg(daemon_token_id) AND token.revoked_at IS NULL AND machine.deleted_at IS NULL
  AND machine.lifecycle_state = 'active'
RETURNING id;

-- name: SetMachineCurrentDaemonRuntime :one
UPDATE machines machine
SET current_daemon_runtime_id = runtime.id
FROM daemon_runtimes runtime
WHERE machine.org_id = sqlc.arg(org_id)
  AND machine.id = sqlc.arg(machine_id)
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state = 'active'
  AND runtime.org_id = machine.org_id
  AND runtime.machine_id = machine.id
  AND runtime.id = sqlc.arg(daemon_runtime_id)
  AND runtime.state = 'active'
RETURNING machine.id;

-- name: HeartbeatDaemonRuntime :one
UPDATE daemon_runtimes runtime
SET last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + (sqlc.arg(lease_timeout_milliseconds)::bigint * interval '1 millisecond'),
    capacity = CASE WHEN sqlc.arg(capacity)::jsonb = '{}'::jsonb THEN runtime.capacity ELSE sqlc.arg(capacity)::jsonb END,
    metadata = CASE WHEN sqlc.arg(metadata)::jsonb = '{}'::jsonb THEN runtime.metadata ELSE sqlc.arg(metadata)::jsonb END,
    updated_at = statement_timestamp()
FROM machine_daemon_tokens token
JOIN machines machine ON machine.org_id = token.org_id
  AND machine.id = token.machine_id
  AND machine.deleted_at IS NULL
  AND machine.lifecycle_state = 'active'
WHERE runtime.org_id = sqlc.arg(org_id) AND runtime.machine_id = sqlc.arg(machine_id) AND runtime.id = sqlc.arg(id)
  AND runtime.daemon_instance_id = sqlc.arg(daemon_instance_id)
  AND runtime.state = 'active'
  AND token.org_id = runtime.org_id AND token.machine_id = runtime.machine_id
  AND token.id = sqlc.arg(daemon_token_id)
  AND runtime.daemon_token_id = token.id
  AND machine.current_daemon_runtime_id = runtime.id
  AND token.revoked_at IS NULL
RETURNING runtime.id, runtime.org_id, runtime.machine_id, runtime.daemon_token_id, runtime.daemon_instance_id, runtime.daemon_version, runtime.state, coalesce(runtime.state_reason_code, '') AS state_reason_code, runtime.state_reason_message, runtime.capacity, runtime.metadata, runtime.created_at, runtime.last_seen_at, runtime.lease_expires_at, runtime.ended_at, runtime.updated_at;

-- name: RegisteredDaemonRuntimeVersion :one
SELECT runtime.daemon_version
FROM registered_daemon_runtimes runtime
WHERE runtime.org_id = sqlc.arg(org_id)
  AND runtime.machine_id = sqlc.arg(machine_id)
  AND runtime.id = sqlc.arg(id)
  AND runtime.daemon_token_id = sqlc.arg(daemon_token_id);

-- name: ReportableDaemonRuntimeExists :one
SELECT EXISTS (
  SELECT 1
  FROM reportable_daemon_runtimes runtime
  WHERE runtime.org_id = sqlc.arg(org_id)
    AND runtime.machine_id = sqlc.arg(machine_id)
    AND runtime.id = sqlc.arg(id)
    AND runtime.daemon_token_id = sqlc.arg(daemon_token_id)
);

-- name: OnlineDaemonRuntimeExists :one
SELECT EXISTS (
  SELECT 1
  FROM online_daemon_runtimes runtime
  WHERE runtime.org_id = sqlc.arg(org_id)
    AND runtime.machine_id = sqlc.arg(machine_id)
    AND runtime.id = sqlc.arg(id)
    AND runtime.daemon_token_id = sqlc.arg(daemon_token_id)
);

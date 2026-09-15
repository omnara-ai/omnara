-- name: UpsertIntegrationAppRuntimeUnit :one
INSERT INTO integration_runtime_units(
  org_id, integration_app_id, project_id, integration_install_id,
  provider, connector_key, unit_key, runtime_kind, desired_state, spec_revision,
  configuration, status, failure_count, available_at, created_at, updated_at
)
SELECT
  sqlc.arg(org_id), app.id, NULL, NULL, app.provider, app.connector_key,
  sqlc.arg(unit_key), sqlc.arg(runtime_kind),
  sqlc.arg(desired_state), sqlc.arg(spec_revision),
  sqlc.arg(configuration),
  CASE WHEN sqlc.arg(desired_state)::text = 'stopped' THEN 'stopped' ELSE 'idle' END,
  0, transaction_timestamp(), transaction_timestamp(), transaction_timestamp()
FROM integration_apps app
WHERE app.org_id = sqlc.arg(org_id)
  AND app.id = sqlc.arg(integration_app_id)
  AND app.deleted_at IS NULL
  AND (sqlc.arg(desired_state)::text <> 'running' OR app.state = 'active')
ON CONFLICT (integration_app_id, unit_key)
  WHERE project_id IS NULL AND deleted_at IS NULL
DO UPDATE SET
  desired_state = excluded.desired_state,
  spec_revision = excluded.spec_revision,
  configuration = excluded.configuration,
  failure_count = CASE
    WHEN integration_runtime_units.spec_revision IS DISTINCT FROM excluded.spec_revision
      OR integration_runtime_units.configuration IS DISTINCT FROM excluded.configuration
      OR integration_runtime_units.desired_state IS DISTINCT FROM excluded.desired_state
      THEN 0
    ELSE integration_runtime_units.failure_count
  END,
  available_at = CASE
    WHEN integration_runtime_units.spec_revision IS DISTINCT FROM excluded.spec_revision
      OR integration_runtime_units.configuration IS DISTINCT FROM excluded.configuration
      OR integration_runtime_units.desired_state IS DISTINCT FROM excluded.desired_state
      THEN statement_timestamp()
    ELSE integration_runtime_units.available_at
  END,
  status = CASE
    WHEN excluded.desired_state = 'stopped' THEN 'stopped'
    WHEN integration_runtime_units.status = 'stopped' THEN 'idle'
    ELSE integration_runtime_units.status
  END,
  lease_owner = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_owner END,
  lease_token = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_token END,
  leased_at = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.leased_at END,
  renewed_at = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.renewed_at END,
  lease_expires_at = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_expires_at END,
  lease_spec_revision = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_spec_revision END,
  lease_app_configuration_revision = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_app_configuration_revision END,
  lease_install_configuration_revision = NULL,
  updated_at = statement_timestamp()
WHERE integration_runtime_units.deleted_at IS NULL
  AND integration_runtime_units.org_id = excluded.org_id
  AND integration_runtime_units.project_id IS NULL
  AND integration_runtime_units.integration_install_id IS NULL
  AND integration_runtime_units.runtime_kind = excluded.runtime_kind
  AND (
    excluded.spec_revision > integration_runtime_units.spec_revision
    OR (
      excluded.spec_revision = integration_runtime_units.spec_revision
      AND excluded.configuration = integration_runtime_units.configuration
    )
  )
RETURNING id, org_id, integration_app_id, project_id, integration_install_id,
  provider, connector_key, unit_key, runtime_kind, desired_state, spec_revision, configuration,
  status, failure_count, available_at, lease_owner, lease_token, lease_generation, leased_at, renewed_at,
  lease_expires_at, lease_spec_revision, lease_app_configuration_revision,
  lease_install_configuration_revision, checkpoint_version, checkpoint_revision, checkpoint,
  last_error, deleted_at, created_at, updated_at;

-- name: UpsertIntegrationInstallRuntimeUnit :one
INSERT INTO integration_runtime_units(
  org_id, integration_app_id, project_id, integration_install_id,
  provider, connector_key, unit_key, runtime_kind, desired_state, spec_revision,
  configuration, status, failure_count, available_at, created_at, updated_at
)
SELECT
  sqlc.arg(org_id), app.id, install.project_id, install.id,
  app.provider, app.connector_key, sqlc.arg(unit_key), sqlc.arg(runtime_kind),
  sqlc.arg(desired_state), sqlc.arg(spec_revision), sqlc.arg(configuration),
  CASE WHEN sqlc.arg(desired_state)::text = 'stopped' THEN 'stopped' ELSE 'idle' END,
  0, transaction_timestamp(), transaction_timestamp(), transaction_timestamp()
FROM integration_installs install
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.deleted_at IS NULL
WHERE install.org_id = sqlc.arg(org_id)
  AND install.project_id = sqlc.arg(project_id)::uuid
  AND install.id = sqlc.arg(integration_install_id)
  AND install.integration_app_id = sqlc.arg(integration_app_id)
  AND install.deleted_at IS NULL
  AND (
    sqlc.arg(desired_state)::text <> 'running'
    OR (app.state = 'active' AND install.state = 'active')
  )
ON CONFLICT (integration_app_id, integration_install_id, unit_key)
  WHERE project_id IS NOT NULL AND deleted_at IS NULL
DO UPDATE SET
  desired_state = excluded.desired_state,
  spec_revision = excluded.spec_revision,
  configuration = excluded.configuration,
  failure_count = CASE
    WHEN integration_runtime_units.spec_revision IS DISTINCT FROM excluded.spec_revision
      OR integration_runtime_units.configuration IS DISTINCT FROM excluded.configuration
      OR integration_runtime_units.desired_state IS DISTINCT FROM excluded.desired_state
      THEN 0
    ELSE integration_runtime_units.failure_count
  END,
  available_at = CASE
    WHEN integration_runtime_units.spec_revision IS DISTINCT FROM excluded.spec_revision
      OR integration_runtime_units.configuration IS DISTINCT FROM excluded.configuration
      OR integration_runtime_units.desired_state IS DISTINCT FROM excluded.desired_state
      THEN statement_timestamp()
    ELSE integration_runtime_units.available_at
  END,
  status = CASE
    WHEN excluded.desired_state = 'stopped' THEN 'stopped'
    WHEN integration_runtime_units.status = 'stopped' THEN 'idle'
    ELSE integration_runtime_units.status
  END,
  lease_owner = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_owner END,
  lease_token = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_token END,
  leased_at = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.leased_at END,
  renewed_at = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.renewed_at END,
  lease_expires_at = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_expires_at END,
  lease_spec_revision = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_spec_revision END,
  lease_app_configuration_revision = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_app_configuration_revision END,
  lease_install_configuration_revision = CASE WHEN excluded.desired_state = 'stopped'
    THEN NULL ELSE integration_runtime_units.lease_install_configuration_revision END,
  updated_at = statement_timestamp()
WHERE integration_runtime_units.deleted_at IS NULL
  AND integration_runtime_units.org_id = excluded.org_id
  AND integration_runtime_units.project_id = excluded.project_id
  AND integration_runtime_units.integration_install_id = excluded.integration_install_id
  AND integration_runtime_units.runtime_kind = excluded.runtime_kind
  AND (
    excluded.spec_revision > integration_runtime_units.spec_revision
    OR (
      excluded.spec_revision = integration_runtime_units.spec_revision
      AND excluded.configuration = integration_runtime_units.configuration
    )
  )
RETURNING id, org_id, integration_app_id, project_id, integration_install_id,
  provider, connector_key, unit_key, runtime_kind, desired_state, spec_revision, configuration,
  status, failure_count, available_at, lease_owner, lease_token, lease_generation, leased_at, renewed_at,
  lease_expires_at, lease_spec_revision, lease_app_configuration_revision,
  lease_install_configuration_revision, checkpoint_version, checkpoint_revision, checkpoint,
  last_error, deleted_at, created_at, updated_at;

-- name: DeleteIntegrationInstallRuntimeUnits :exec
UPDATE integration_runtime_units
SET desired_state = 'stopped',
    status = 'stopped',
    lease_owner = NULL,
    lease_token = NULL,
    leased_at = NULL,
    renewed_at = NULL,
    lease_expires_at = NULL,
    lease_spec_revision = NULL,
    lease_app_configuration_revision = NULL,
    lease_install_configuration_revision = NULL,
    deleted_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)::uuid
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND deleted_at IS NULL;

-- name: ClaimIntegrationRuntimeUnits :many
WITH candidate AS MATERIALIZED (
  SELECT unit.id, app.configuration_revision AS app_configuration_revision,
         install.configuration_revision AS install_configuration_revision
  FROM integration_runtime_units unit
  JOIN integration_apps app
    ON app.org_id = unit.org_id
   AND app.id = unit.integration_app_id
   AND app.state = 'active'
   AND app.deleted_at IS NULL
  LEFT JOIN integration_installs install
    ON install.org_id = unit.org_id
   AND install.project_id = unit.project_id
   AND install.id = unit.integration_install_id
   AND install.integration_app_id = unit.integration_app_id
   AND install.state = 'active'
   AND install.deleted_at IS NULL
  WHERE unit.connector_key = sqlc.arg(connector_key)
    AND unit.provider = sqlc.arg(provider)
    AND unit.desired_state = 'running'
    AND unit.deleted_at IS NULL
    AND unit.available_at <= statement_timestamp()
    AND (unit.lease_token IS NULL OR unit.lease_expires_at <= statement_timestamp())
    AND (unit.integration_install_id IS NULL OR install.id IS NOT NULL)
  ORDER BY unit.available_at, unit.id
  FOR UPDATE OF unit SKIP LOCKED
  LIMIT sqlc.arg(row_limit)
)
UPDATE integration_runtime_units unit
SET status = 'running',
    lease_owner = sqlc.arg(lease_owner)::text,
    lease_token = uuidv7(),
    lease_generation = unit.lease_generation + 1,
    leased_at = statement_timestamp(),
    renewed_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() +
      (sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond'),
    available_at = statement_timestamp() +
      (sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond'),
    lease_spec_revision = unit.spec_revision,
    lease_app_configuration_revision = candidate.app_configuration_revision,
    lease_install_configuration_revision = candidate.install_configuration_revision,
    last_error = '{}'::jsonb,
    updated_at = statement_timestamp()
FROM candidate
WHERE unit.id = candidate.id
RETURNING unit.id, unit.org_id, unit.integration_app_id, unit.project_id,
  unit.integration_install_id, unit.provider, unit.connector_key, unit.unit_key, unit.runtime_kind,
  unit.desired_state, unit.spec_revision, unit.configuration,
  unit.status, unit.failure_count, unit.available_at, unit.lease_owner, unit.lease_token, unit.lease_generation,
  unit.leased_at, unit.renewed_at, unit.lease_expires_at,
  unit.lease_spec_revision, unit.lease_app_configuration_revision,
  unit.lease_install_configuration_revision,
  unit.checkpoint_version, unit.checkpoint_revision, unit.checkpoint,
  unit.last_error, unit.deleted_at, unit.created_at, unit.updated_at;

-- name: HeartbeatIntegrationRuntimeUnit :one
UPDATE integration_runtime_units unit
SET renewed_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() +
      (sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond'),
    available_at = statement_timestamp() +
      (sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond'),
    checkpoint = CASE WHEN sqlc.arg(write_checkpoint)::boolean
      THEN sqlc.arg(checkpoint)::jsonb ELSE checkpoint END,
    checkpoint_version = CASE WHEN sqlc.arg(write_checkpoint)::boolean
      THEN sqlc.arg(checkpoint_version)::integer ELSE checkpoint_version END,
    checkpoint_revision = CASE WHEN sqlc.arg(write_checkpoint)::boolean
      THEN checkpoint_revision + 1 ELSE checkpoint_revision END,
    failure_count = 0,
    updated_at = statement_timestamp()
WHERE unit.id = sqlc.arg(id)
  AND unit.lease_token = sqlc.arg(lease_token)::uuid
  AND unit.lease_generation = sqlc.arg(lease_generation)
  AND unit.spec_revision = unit.lease_spec_revision
  AND unit.desired_state = 'running'
  AND unit.lease_expires_at > statement_timestamp()
  AND unit.deleted_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM integration_apps app
    WHERE app.org_id = unit.org_id
      AND app.id = unit.integration_app_id
      AND EXISTS (
        SELECT 1
        FROM generate_subscripts(sqlc.arg(connector_keys)::text[], 1) AS capability(index)
        WHERE (sqlc.arg(connector_keys)::text[])[capability.index] = unit.connector_key
          AND (sqlc.arg(providers)::text[])[capability.index] = unit.provider
      )
      AND app.configuration_revision = unit.lease_app_configuration_revision
      AND app.state = 'active'
      AND app.deleted_at IS NULL
  )
  AND (
    unit.integration_install_id IS NULL
    OR EXISTS (
      SELECT 1
      FROM integration_installs install
      WHERE install.org_id = unit.org_id
        AND install.project_id = unit.project_id
        AND install.id = unit.integration_install_id
        AND install.integration_app_id = unit.integration_app_id
        AND install.state = 'active'
        AND install.deleted_at IS NULL
        AND install.configuration_revision = unit.lease_install_configuration_revision
    )
  )
RETURNING unit.id, unit.org_id, unit.integration_app_id, unit.project_id,
  unit.integration_install_id, unit.provider, unit.connector_key,
  unit.unit_key, unit.runtime_kind, unit.desired_state,
  unit.spec_revision, unit.configuration, unit.status, unit.failure_count,
  unit.available_at, unit.lease_owner,
  unit.lease_token, unit.lease_generation, unit.leased_at, unit.renewed_at,
  unit.lease_expires_at, unit.lease_spec_revision, unit.lease_app_configuration_revision,
  unit.lease_install_configuration_revision, unit.checkpoint_version, unit.checkpoint_revision,
  unit.checkpoint, unit.last_error, unit.deleted_at, unit.created_at, unit.updated_at;

-- name: ReleaseIntegrationRuntimeUnit :one
UPDATE integration_runtime_units unit
SET status = CASE
      WHEN desired_state = 'stopped' THEN 'stopped'
      WHEN sqlc.arg(error)::jsonb = '{}'::jsonb THEN 'idle'
      ELSE 'error'
    END,
    checkpoint = CASE WHEN sqlc.arg(write_checkpoint)::boolean
      THEN sqlc.arg(checkpoint)::jsonb ELSE checkpoint END,
    checkpoint_version = CASE WHEN sqlc.arg(write_checkpoint)::boolean
      THEN sqlc.arg(checkpoint_version)::integer ELSE checkpoint_version END,
    checkpoint_revision = CASE WHEN sqlc.arg(write_checkpoint)::boolean
      THEN checkpoint_revision + 1 ELSE checkpoint_revision END,
    lease_owner = NULL,
    lease_token = NULL,
    leased_at = NULL,
    renewed_at = NULL,
    lease_expires_at = NULL,
    lease_spec_revision = NULL,
    lease_app_configuration_revision = NULL,
    lease_install_configuration_revision = NULL,
    last_error = sqlc.arg(error),
    failure_count = CASE
      WHEN sqlc.arg(error)::jsonb = '{}'::jsonb THEN 0
      ELSE least(unit.failure_count + 1, 30)
    END,
    available_at = CASE
      WHEN desired_state = 'stopped' OR sqlc.arg(error)::jsonb = '{}'::jsonb
        THEN statement_timestamp()
      ELSE statement_timestamp() + make_interval(
        secs => least(300.0, 5.0 * power(2.0, least(unit.failure_count, 6))) *
          (
            0.8 + 0.4 * (
              (hashtextextended(unit.id::text, unit.failure_count::bigint) & 2147483647)::double precision /
              2147483647.0
            )
          )
      )
    END,
    updated_at = statement_timestamp()
WHERE unit.id = sqlc.arg(id)
  AND unit.lease_token = sqlc.arg(lease_token)::uuid
  AND unit.lease_generation = sqlc.arg(lease_generation)
  AND unit.spec_revision = unit.lease_spec_revision
  AND unit.desired_state = 'running'
  AND unit.lease_expires_at > statement_timestamp()
  AND unit.deleted_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM integration_apps app
    WHERE app.org_id = unit.org_id
      AND app.id = unit.integration_app_id
      AND EXISTS (
        SELECT 1
        FROM generate_subscripts(sqlc.arg(connector_keys)::text[], 1) AS capability(index)
        WHERE (sqlc.arg(connector_keys)::text[])[capability.index] = unit.connector_key
          AND (sqlc.arg(providers)::text[])[capability.index] = unit.provider
      )
      AND app.configuration_revision = unit.lease_app_configuration_revision
      AND app.state = 'active'
      AND app.deleted_at IS NULL
  )
  AND (
    unit.integration_install_id IS NULL
    OR EXISTS (
      SELECT 1
      FROM integration_installs install
      WHERE install.org_id = unit.org_id
        AND install.project_id = unit.project_id
        AND install.id = unit.integration_install_id
        AND install.integration_app_id = unit.integration_app_id
        AND install.configuration_revision = unit.lease_install_configuration_revision
        AND install.state = 'active'
        AND install.deleted_at IS NULL
    )
  )
RETURNING unit.id, unit.org_id, unit.integration_app_id, unit.project_id,
  unit.integration_install_id, unit.provider, unit.connector_key,
  unit.unit_key, unit.runtime_kind, unit.desired_state,
  unit.spec_revision, unit.configuration, unit.status, unit.failure_count,
  unit.available_at, unit.lease_owner,
  unit.lease_token, unit.lease_generation, unit.leased_at, unit.renewed_at,
  unit.lease_expires_at, unit.lease_spec_revision, unit.lease_app_configuration_revision,
  unit.lease_install_configuration_revision, unit.checkpoint_version, unit.checkpoint_revision,
  unit.checkpoint, unit.last_error, unit.deleted_at, unit.created_at, unit.updated_at;

-- A worker that notices its snapshot is stale should relinquish ownership
-- promptly, but it must not publish an obsolete checkpoint, provider outcome,
-- failure count, or retry delay. The lease token/generation still makes this a
-- fenced compare-and-swap; a replacement that already claimed the unit wins.
-- name: RelinquishStaleIntegrationRuntimeUnit :one
UPDATE integration_runtime_units unit
SET status = CASE WHEN desired_state = 'stopped' THEN 'stopped' ELSE 'idle' END,
    lease_owner = NULL,
    lease_token = NULL,
    leased_at = NULL,
    renewed_at = NULL,
    lease_expires_at = NULL,
    lease_spec_revision = NULL,
    lease_app_configuration_revision = NULL,
    lease_install_configuration_revision = NULL,
    available_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE unit.id = sqlc.arg(id)
  AND unit.lease_token = sqlc.arg(lease_token)::uuid
  AND unit.lease_generation = sqlc.arg(lease_generation)
  AND unit.deleted_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM integration_apps app
    WHERE app.org_id = unit.org_id
      AND app.id = unit.integration_app_id
      AND EXISTS (
        SELECT 1
        FROM generate_subscripts(sqlc.arg(connector_keys)::text[], 1) AS capability(index)
        WHERE (sqlc.arg(connector_keys)::text[])[capability.index] = unit.connector_key
          AND (sqlc.arg(providers)::text[])[capability.index] = unit.provider
      )
  )
RETURNING unit.id, unit.org_id, unit.integration_app_id, unit.project_id,
  unit.integration_install_id, unit.provider, unit.connector_key,
  unit.unit_key, unit.runtime_kind, unit.desired_state,
  unit.spec_revision, unit.configuration, unit.status, unit.failure_count,
  unit.available_at, unit.lease_owner,
  unit.lease_token, unit.lease_generation, unit.leased_at, unit.renewed_at,
  unit.lease_expires_at, unit.lease_spec_revision, unit.lease_app_configuration_revision,
  unit.lease_install_configuration_revision, unit.checkpoint_version, unit.checkpoint_revision,
  unit.checkpoint, unit.last_error, unit.deleted_at, unit.created_at, unit.updated_at;

-- name: IntegrationRuntimeLeaseIsCurrent :one
SELECT EXISTS (
  SELECT 1
  FROM integration_runtime_units unit
  JOIN integration_apps app
    ON app.org_id = unit.org_id
   AND app.id = unit.integration_app_id
   AND app.state = 'active'
   AND app.deleted_at IS NULL
   AND app.configuration_revision = unit.lease_app_configuration_revision
  JOIN integration_installs install
    ON install.org_id = unit.org_id
   AND install.id = sqlc.arg(integration_install_id)
   AND install.integration_app_id = unit.integration_app_id
   AND install.state = 'active'
   AND install.deleted_at IS NULL
  WHERE unit.id = sqlc.arg(id)
    AND unit.integration_app_id = sqlc.arg(integration_app_id)
    AND (
      unit.integration_install_id IS NULL
      OR (
        unit.integration_install_id = install.id
        AND unit.project_id = install.project_id
        AND install.configuration_revision = unit.lease_install_configuration_revision
      )
    )
    AND unit.lease_token = sqlc.arg(lease_token)::uuid
    AND unit.lease_generation = sqlc.arg(lease_generation)
    AND unit.spec_revision = unit.lease_spec_revision
    AND unit.lease_expires_at > statement_timestamp()
    AND unit.desired_state = 'running'
    AND unit.deleted_at IS NULL
)::boolean;

-- name: LockIntegrationRuntimeLeaseForMutation :one
WITH install_authority AS MATERIALIZED (
  SELECT install.id, install.org_id, install.project_id, install.integration_app_id,
    install.configuration_revision
  FROM integration_installs install
  WHERE install.id = sqlc.arg(integration_install_id)
    AND install.project_id = sqlc.arg(project_id)::uuid
    AND install.state = 'active'
    AND install.deleted_at IS NULL
  FOR SHARE OF install
), app_authority AS MATERIALIZED (
  SELECT app.id, app.org_id, app.configuration_revision
  FROM integration_apps app
  JOIN install_authority install
    ON install.integration_app_id = app.id
   AND install.org_id = app.org_id
  WHERE app.id = sqlc.arg(integration_app_id)
    AND app.state = 'active'
    AND app.deleted_at IS NULL
  FOR SHARE OF app
), unit_authority AS MATERIALIZED (
  SELECT unit.id
  FROM integration_runtime_units unit
  JOIN install_authority install
    ON install.integration_app_id = unit.integration_app_id
  JOIN app_authority app
    ON app.id = unit.integration_app_id
   AND app.org_id = unit.org_id
  WHERE unit.id = sqlc.arg(id)
    AND (
      unit.integration_install_id IS NULL
      OR (
        unit.integration_install_id = install.id
        AND unit.project_id = install.project_id
        AND install.configuration_revision = unit.lease_install_configuration_revision
      )
    )
    AND unit.lease_token = sqlc.arg(lease_token)::uuid
    AND unit.lease_generation = sqlc.arg(lease_generation)
    AND unit.spec_revision = unit.lease_spec_revision
    AND unit.lease_expires_at > statement_timestamp()
    AND unit.desired_state = 'running'
    AND unit.deleted_at IS NULL
    AND app.configuration_revision = unit.lease_app_configuration_revision
  FOR SHARE OF unit
)
SELECT EXISTS (SELECT 1 FROM unit_authority)::boolean;

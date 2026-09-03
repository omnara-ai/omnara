-- name: InsertIntegrationDelivery :one
INSERT INTO integration_deliveries(
  project_id, agent_id, integration_app_id, integration_install_id,
  integration_target_id, integration_target_binding_id,
  provider, connector_key, transport, delivery_kind, payload_version, payload,
  idempotency_scope, idempotency_key, state, available_at, notify_ref,
  created_at, updated_at
)
SELECT binding.project_id, binding.agent_id, install.integration_app_id,
       binding.integration_install_id, binding.integration_target_id, binding.id,
       install.provider, app.connector_key, sqlc.arg(transport),
       sqlc.arg(delivery_kind), sqlc.arg(payload_version), sqlc.arg(payload),
       sqlc.arg(idempotency_scope), sqlc.arg(idempotency_key), 'pending',
       transaction_timestamp(), sqlc.narg(notify_ref),
       transaction_timestamp(), transaction_timestamp()
FROM integration_target_bindings binding
JOIN integration_targets target
  ON target.project_id = binding.project_id
 AND target.id = binding.integration_target_id
 AND target.deleted_at IS NULL
JOIN integration_installs install
  ON install.project_id = binding.project_id
 AND install.id = binding.integration_install_id
 AND install.state = 'active'
 AND install.deleted_at IS NULL
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.state = 'active'
 AND app.deleted_at IS NULL
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.id = sqlc.arg(integration_target_binding_id)
  AND binding.send_allowed
  AND binding.revoked_at IS NULL
  AND (
    binding.integration_route_id IS NULL
    OR EXISTS (
      SELECT 1 FROM integration_routes route
      WHERE route.project_id = binding.project_id
        AND route.integration_install_id = binding.integration_install_id
        AND route.id = binding.integration_route_id
        AND route.state = 'active'
        AND route.deleted_at IS NULL
    )
  )
ON CONFLICT (project_id, agent_id, idempotency_scope, idempotency_key) DO NOTHING
RETURNING id, project_id, agent_id, integration_app_id, integration_install_id,
  integration_target_id, integration_target_binding_id, provider, connector_key,
  transport, delivery_kind, payload_version, payload, idempotency_scope,
  idempotency_key, state, attempt_count, available_at, claim_token,
  claim_generation, claimed_by, claimed_at, claim_expires_at, notify_ref,
  provider_message_ref, last_error, completed_at, created_at, updated_at;

-- name: GetIntegrationDeliveryByIdempotency :one
SELECT id, project_id, agent_id, integration_app_id, integration_install_id,
  integration_target_id, integration_target_binding_id, provider, connector_key,
  transport, delivery_kind, payload_version, payload, idempotency_scope,
  idempotency_key, state, attempt_count, available_at, claim_token,
  claim_generation, claimed_by, claimed_at, claim_expires_at, notify_ref,
  provider_message_ref, last_error, completed_at, created_at, updated_at
FROM integration_deliveries
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND idempotency_scope = sqlc.arg(idempotency_scope)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: GetIntegrationDelivery :one
SELECT id, project_id, agent_id, integration_app_id, integration_install_id,
  integration_target_id, integration_target_binding_id, provider, connector_key,
  transport, delivery_kind, payload_version, payload, idempotency_scope,
  idempotency_key, state, attempt_count, available_at, claim_token,
  claim_generation, claimed_by, claimed_at, claim_expires_at, notify_ref,
  provider_message_ref, last_error, completed_at, created_at, updated_at
FROM integration_deliveries
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id);

-- name: ClaimIntegrationDeliveries :many
WITH candidate AS MATERIALIZED (
  SELECT delivery.id,
         app.configuration_revision AS app_configuration_revision,
         install.configuration_revision AS install_configuration_revision
  FROM integration_deliveries delivery
  JOIN integration_target_bindings binding
    ON binding.project_id = delivery.project_id
   AND binding.id = delivery.integration_target_binding_id
   AND binding.agent_id = delivery.agent_id
   AND binding.integration_target_id = delivery.integration_target_id
   AND binding.send_allowed
   AND binding.revoked_at IS NULL
  JOIN integration_targets target
    ON target.project_id = binding.project_id
   AND target.id = binding.integration_target_id
   AND target.deleted_at IS NULL
  JOIN integration_installs install
    ON install.project_id = binding.project_id
   AND install.id = binding.integration_install_id
   AND install.state = 'active'
   AND install.deleted_at IS NULL
  JOIN integration_apps app
    ON app.org_id = install.org_id
   AND app.id = install.integration_app_id
   AND app.state = 'active'
   AND app.deleted_at IS NULL
  WHERE delivery.transport = 'connector'
    AND delivery.connector_key = sqlc.arg(connector_key)
    AND delivery.provider = sqlc.arg(provider)
    AND delivery.state IN ('pending', 'retry_wait')
    AND delivery.available_at <= statement_timestamp()
    AND (
      binding.integration_route_id IS NULL
      OR EXISTS (
        SELECT 1 FROM integration_routes route
        WHERE route.project_id = binding.project_id
          AND route.integration_install_id = binding.integration_install_id
          AND route.id = binding.integration_route_id
          AND route.state = 'active'
          AND route.deleted_at IS NULL
      )
    )
  ORDER BY delivery.available_at, delivery.id
  FOR UPDATE OF delivery SKIP LOCKED
  LIMIT sqlc.arg(row_limit)
)
UPDATE integration_deliveries delivery
SET state = 'claimed',
    attempt_count = delivery.attempt_count + 1,
    claim_token = uuidv7(),
    claim_generation = delivery.claim_generation + 1,
    claimed_by = sqlc.arg(claimed_by)::text,
    claimed_at = statement_timestamp(),
    claim_expires_at = statement_timestamp() +
      (sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond'),
    updated_at = statement_timestamp()
FROM candidate
WHERE delivery.id = candidate.id
RETURNING delivery.id, delivery.project_id, delivery.agent_id,
  delivery.integration_app_id, delivery.integration_install_id,
  delivery.integration_target_id, delivery.integration_target_binding_id,
  delivery.provider, delivery.connector_key, delivery.transport,
  delivery.delivery_kind, delivery.payload_version, delivery.payload,
  delivery.idempotency_scope, delivery.idempotency_key, delivery.state,
  delivery.attempt_count, delivery.available_at, delivery.claim_token,
  delivery.claim_generation, delivery.claimed_by, delivery.claimed_at,
  delivery.claim_expires_at, delivery.notify_ref, delivery.provider_message_ref,
  delivery.last_error, delivery.completed_at, delivery.created_at, delivery.updated_at,
  candidate.app_configuration_revision, candidate.install_configuration_revision;

-- name: CompleteIntegrationDelivery :one
UPDATE integration_deliveries
SET state = CASE
      WHEN sqlc.arg(state)::text = 'retry_wait'
        AND attempt_count >= sqlc.arg(max_attempts)::integer THEN 'failed'
      ELSE sqlc.arg(state)::text
    END,
    available_at = CASE
      WHEN sqlc.arg(state)::text = 'retry_wait'
        AND attempt_count < sqlc.arg(max_attempts)::integer THEN statement_timestamp() +
        (sqlc.arg(retry_microseconds)::bigint * interval '1 microsecond')
      ELSE available_at
    END,
    claim_token = NULL,
    claimed_by = NULL,
    claimed_at = NULL,
    claim_expires_at = NULL,
    provider_message_ref = nullif(sqlc.arg(provider_message_ref)::text, ''),
    last_error = CASE
      WHEN sqlc.arg(state)::text = 'retry_wait'
        AND attempt_count >= sqlc.arg(max_attempts)::integer
        THEN sqlc.arg(last_error)::jsonb || jsonb_build_object(
          'code', 'retry_budget_exhausted',
          'message', 'delivery exhausted the core safe-retry budget'
        )
      ELSE sqlc.arg(last_error)::jsonb
    END,
    completed_at = CASE
      WHEN sqlc.arg(state)::text IN ('delivered', 'failed', 'unknown', 'canceled')
        OR (
          sqlc.arg(state)::text = 'retry_wait'
          AND attempt_count >= sqlc.arg(max_attempts)::integer
        )
        THEN statement_timestamp()
      ELSE NULL
    END,
    updated_at = statement_timestamp()
WHERE id = sqlc.arg(id)
  AND state = 'claimed'
  AND claim_token = sqlc.arg(claim_token)::uuid
  AND claim_generation = sqlc.arg(claim_generation)
  AND claim_expires_at > statement_timestamp()
  AND EXISTS (
    SELECT 1
    FROM generate_subscripts(sqlc.arg(connector_keys)::text[], 1) AS capability(index)
    WHERE (sqlc.arg(connector_keys)::text[])[capability.index] = integration_deliveries.connector_key
      AND (sqlc.arg(providers)::text[])[capability.index] = integration_deliveries.provider
  )
RETURNING id, project_id, agent_id, integration_app_id, integration_install_id,
  integration_target_id, integration_target_binding_id, provider, connector_key,
  transport, delivery_kind, payload_version, payload, idempotency_scope,
  idempotency_key, state, attempt_count, available_at, claim_token,
  claim_generation, claimed_by, claimed_at, claim_expires_at, notify_ref,
  provider_message_ref, last_error, completed_at, created_at, updated_at;

-- name: ExpireIntegrationDeliveryClaims :many
WITH expired AS MATERIALIZED (
  SELECT id
  FROM integration_deliveries
  WHERE state = 'claimed' AND claim_expires_at <= statement_timestamp()
  ORDER BY claim_expires_at, id
  FOR UPDATE SKIP LOCKED
  LIMIT sqlc.arg(row_limit)
)
UPDATE integration_deliveries delivery
SET state = 'unknown',
    claim_token = NULL,
    claimed_by = NULL,
    claimed_at = NULL,
    claim_expires_at = NULL,
    last_error = jsonb_build_object('code', 'claim_expired', 'message', 'delivery outcome is unknown'),
    completed_at = statement_timestamp(),
    updated_at = statement_timestamp()
FROM expired
WHERE delivery.id = expired.id
RETURNING delivery.id, delivery.project_id, delivery.notify_ref;

-- name: CancelUnavailableIntegrationDeliveries :many
WITH sweep_cursor AS MATERIALIZED (
  SELECT last_item_id, cycle_end_id
  FROM integration_sweep_cursors
  WHERE sweep_kind = 'delivery_unavailable'
  FOR UPDATE SKIP LOCKED
), cycle AS MATERIALIZED (
  SELECT cursor.last_item_id,
         coalesce(cursor.cycle_end_id, upper_bound.id) AS cycle_end_id
  FROM sweep_cursor cursor
  LEFT JOIN LATERAL (
    SELECT delivery.id
    FROM integration_deliveries delivery
    WHERE delivery.transport = 'connector'
      AND delivery.state IN ('pending', 'retry_wait')
    ORDER BY delivery.id DESC
    LIMIT 1
  ) upper_bound ON true
), candidates AS MATERIALIZED (
  SELECT candidate.id
  FROM cycle
  CROSS JOIN LATERAL (
    SELECT delivery.id
    FROM integration_deliveries delivery
    WHERE delivery.transport = 'connector'
      AND delivery.state IN ('pending', 'retry_wait')
      AND delivery.id > cycle.last_item_id
      AND delivery.id <= cycle.cycle_end_id
    ORDER BY delivery.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(row_limit)
  ) candidate
), progress AS MATERIALIZED (
  SELECT count(*) AS candidate_count,
         (SELECT id FROM candidates ORDER BY id DESC LIMIT 1) AS last_candidate_id
  FROM candidates
), advance_cursor AS (
  UPDATE integration_sweep_cursors cursor
  SET last_item_id = CASE
        WHEN progress.candidate_count < sqlc.arg(row_limit)::integer
          OR progress.last_candidate_id = cycle.cycle_end_id
          THEN '00000000-0000-0000-0000-000000000000'::uuid
        ELSE progress.last_candidate_id
      END,
      cycle_end_id = CASE
        WHEN progress.candidate_count < sqlc.arg(row_limit)::integer
          OR progress.last_candidate_id = cycle.cycle_end_id
          THEN NULL
        ELSE cycle.cycle_end_id
      END,
      updated_at = statement_timestamp()
  FROM cycle CROSS JOIN progress
  WHERE cursor.sweep_kind = 'delivery_unavailable'
    AND cycle.cycle_end_id IS NOT NULL
  RETURNING cursor.sweep_kind
), unavailable AS MATERIALIZED (
  SELECT candidate.id
  FROM candidates candidate
  JOIN integration_deliveries delivery ON delivery.id = candidate.id
  WHERE NOT EXISTS (
      SELECT 1
      FROM integration_target_bindings binding
      JOIN integration_targets target
        ON target.project_id = binding.project_id
       AND target.id = binding.integration_target_id
       AND target.deleted_at IS NULL
      JOIN integration_installs install
        ON install.project_id = binding.project_id
       AND install.id = binding.integration_install_id
       AND install.state = 'active'
       AND install.deleted_at IS NULL
      JOIN integration_apps app
        ON app.org_id = install.org_id
       AND app.id = install.integration_app_id
       AND app.state = 'active'
       AND app.deleted_at IS NULL
      WHERE binding.project_id = delivery.project_id
        AND binding.id = delivery.integration_target_binding_id
        AND binding.agent_id = delivery.agent_id
        AND binding.integration_target_id = delivery.integration_target_id
        AND binding.send_allowed
        AND binding.revoked_at IS NULL
        AND (
          binding.integration_route_id IS NULL
          OR EXISTS (
            SELECT 1 FROM integration_routes route
            WHERE route.project_id = binding.project_id
              AND route.integration_install_id = binding.integration_install_id
              AND route.id = binding.integration_route_id
              AND route.state = 'active'
              AND route.deleted_at IS NULL
          )
        )
    )
)
UPDATE integration_deliveries delivery
SET state = 'canceled',
    last_error = jsonb_build_object(
      'code', 'channel_unavailable',
      'message', 'channel became unavailable before delivery'
    ),
    completed_at = statement_timestamp(),
    updated_at = statement_timestamp()
FROM unavailable
WHERE delivery.id = unavailable.id
  AND EXISTS (SELECT 1 FROM advance_cursor)
RETURNING delivery.id, delivery.project_id, delivery.notify_ref;

-- name: DeleteRetainedIntegrationDeliveries :execrows
WITH expired AS (
  SELECT candidate.id
  FROM integration_deliveries candidate
  WHERE candidate.state IN ('delivered', 'failed', 'unknown', 'canceled')
    AND candidate.completed_at < statement_timestamp() -
      (sqlc.arg(retention_microseconds)::bigint * interval '1 microsecond')
  ORDER BY candidate.completed_at, candidate.id
  FOR UPDATE OF candidate SKIP LOCKED
  LIMIT sqlc.arg(row_limit)
)
DELETE FROM integration_deliveries delivery
USING expired
WHERE delivery.id = expired.id;

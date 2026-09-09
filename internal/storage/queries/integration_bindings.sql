-- name: InsertIntegrationTargetBinding :one
INSERT INTO integration_target_bindings(
  project_id, agent_id, integration_install_id, integration_target_id,
  target_created_at, integration_route_id,
  receive_allowed, send_allowed, source, metadata,
  created_at, updated_at
)
SELECT
  sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(integration_install_id),
  target.id, target.created_at, sqlc.narg(integration_route_id),
  sqlc.arg(receive_allowed), sqlc.arg(send_allowed), sqlc.arg(source),
  sqlc.arg(metadata), transaction_timestamp(), transaction_timestamp()
FROM integration_targets target
WHERE target.project_id = sqlc.arg(project_id)
  AND target.integration_install_id = sqlc.arg(integration_install_id)
  AND target.id = sqlc.arg(integration_target_id)
  AND target.deleted_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM agents agent
    WHERE agent.project_id = sqlc.arg(project_id)
      AND agent.id = sqlc.arg(agent_id)
      AND agent.state = 'active'
  )
  AND (
    (
      sqlc.narg(integration_route_id)::uuid IS NULL
      AND NOT sqlc.arg(receive_allowed)::boolean
    ) OR EXISTS (
      SELECT 1
      FROM integration_routes route
      WHERE route.project_id = sqlc.arg(project_id)
        AND route.integration_install_id = sqlc.arg(integration_install_id)
        AND route.id = sqlc.narg(integration_route_id)::uuid
        AND route.state = 'active'
        AND route.deleted_at IS NULL
    )
  )
ON CONFLICT DO NOTHING
RETURNING id, project_id, agent_id, integration_install_id, integration_target_id,
  target_created_at, integration_route_id, receive_allowed, send_allowed, source, metadata,
  revoked_at, created_at, updated_at;

-- name: GetActiveIntegrationTargetBindingByIdentity :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.send_allowed,
  binding.source, binding.metadata, binding.revoked_at,
  binding.created_at, binding.updated_at
FROM integration_target_bindings binding
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.integration_route_id IS NOT DISTINCT FROM sqlc.narg(integration_route_id)::uuid
  AND (
    binding.integration_route_id IS NOT NULL
    OR binding.source = sqlc.arg(source)
  )
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
  );

-- name: LockIntegrationTargetForBinding :one
WITH install_authority AS MATERIALIZED (
  SELECT install.id, install.org_id, install.integration_app_id
  FROM integration_installs install
  WHERE install.project_id = sqlc.arg(project_id)
    AND install.id = sqlc.arg(integration_install_id)
    AND install.state = 'active'
    AND install.deleted_at IS NULL
  FOR SHARE OF install
), app_authority AS MATERIALIZED (
  SELECT install.id
  FROM integration_apps app
  JOIN install_authority install ON install.integration_app_id = app.id
  WHERE app.org_id = install.org_id
    AND app.state = 'active'
    AND app.deleted_at IS NULL
  FOR SHARE OF app
)
SELECT target.id
FROM integration_targets target
JOIN app_authority install ON install.id = target.integration_install_id
WHERE target.project_id = sqlc.arg(project_id)
  AND target.integration_install_id = sqlc.arg(integration_install_id)
  AND target.id = sqlc.arg(integration_target_id)
  AND target.deleted_at IS NULL
FOR NO KEY UPDATE OF target;

-- name: LockActiveIntegrationRouteForBinding :one
SELECT id
FROM integration_routes
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id)
  AND state = 'active'
  AND deleted_at IS NULL
FOR SHARE;

-- name: CountActiveReceiveBindingsForTargetRoute :one
SELECT count(*)
FROM integration_target_bindings binding
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.integration_route_id = sqlc.arg(integration_route_id)
  AND binding.receive_allowed
  AND binding.revoked_at IS NULL;

-- name: RevokeIntegrationTargetBinding :execrows
UPDATE integration_target_bindings
SET revoked_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND revoked_at IS NULL;

-- name: RevokeIntegrationTargetBindingsForRoute :exec
UPDATE integration_target_bindings
SET revoked_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND integration_route_id = sqlc.arg(integration_route_id)
  AND revoked_at IS NULL;

-- name: ListActiveReceiveBindingsForTargetRoute :many
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.send_allowed,
  binding.source, binding.metadata, binding.revoked_at,
  binding.created_at, binding.updated_at
FROM integration_target_bindings binding
JOIN integration_targets target
  ON target.project_id = binding.project_id
 AND target.integration_install_id = binding.integration_install_id
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
JOIN integration_routes route
  ON route.project_id = binding.project_id
 AND route.integration_install_id = binding.integration_install_id
 AND route.id = binding.integration_route_id
 AND route.state = 'active'
 AND route.deleted_at IS NULL
JOIN agents agent
  ON agent.project_id = binding.project_id
 AND agent.id = binding.agent_id
 AND agent.state = 'active'
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.integration_install_id = sqlc.arg(integration_install_id)
  AND binding.integration_route_id = sqlc.arg(integration_route_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.receive_allowed
  AND binding.revoked_at IS NULL
ORDER BY binding.created_at, binding.id
LIMIT sqlc.arg(row_limit);

-- name: GetIntegrationTargetBinding :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.send_allowed,
  binding.source, binding.metadata, binding.revoked_at,
  binding.created_at, binding.updated_at
FROM integration_target_bindings binding
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.id = sqlc.arg(id)
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
  );

-- name: IntegrationTargetBindingExists :one
-- @sqlc-vet-disable integration-target-bindings-deleted-at
-- Revocation replay must distinguish a historical binding from one that never existed.
SELECT EXISTS (
  SELECT 1
  FROM integration_target_bindings
  WHERE project_id = sqlc.arg(project_id)
    AND id = sqlc.arg(id)
);

-- name: GetActiveSendBindingForTarget :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.send_allowed,
  binding.source, binding.metadata, binding.revoked_at,
  binding.created_at, binding.updated_at
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
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
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
ORDER BY (binding.integration_route_id IS NULL) DESC,
  binding.receive_allowed DESC, binding.created_at, binding.id
LIMIT 1;

-- name: GetActiveReceiveBinding :one
WITH install_authority AS MATERIALIZED (
  SELECT install.id, install.org_id, install.integration_app_id
  FROM integration_installs install
  WHERE install.project_id = sqlc.arg(project_id)
    AND install.id = sqlc.arg(integration_install_id)
    AND install.state = 'active'
    AND install.deleted_at IS NULL
  FOR SHARE OF install
), app_authority AS MATERIALIZED (
  SELECT install.id
  FROM integration_apps app
  JOIN install_authority install
    ON install.integration_app_id = app.id
   AND install.org_id = app.org_id
  WHERE app.state = 'active'
    AND app.deleted_at IS NULL
  FOR SHARE OF app
)
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.send_allowed,
  binding.source, binding.metadata, binding.revoked_at,
  binding.created_at, binding.updated_at
FROM app_authority install
JOIN integration_target_bindings binding
  ON binding.integration_install_id = install.id
JOIN integration_targets target
  ON target.project_id = binding.project_id
 AND target.integration_install_id = binding.integration_install_id
 AND target.id = binding.integration_target_id
 AND target.deleted_at IS NULL
LEFT JOIN LATERAL (
  SELECT route.id
  FROM integration_routes route
  WHERE route.project_id = binding.project_id
    AND route.integration_install_id = binding.integration_install_id
    AND route.id = binding.integration_route_id
    AND route.state = 'active'
    AND route.deleted_at IS NULL
  FOR SHARE OF route
) route ON true
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.integration_install_id = sqlc.arg(integration_install_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.id = sqlc.arg(id)
  AND binding.receive_allowed
  AND binding.revoked_at IS NULL
  AND (
    (binding.integration_route_id IS NULL AND binding.source = 'legacy_target')
    OR route.id IS NOT NULL
  )
FOR SHARE OF binding;

-- name: GetActiveReceiveBindingForTarget :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.send_allowed,
  binding.source, binding.metadata, binding.revoked_at,
  binding.created_at, binding.updated_at
FROM integration_target_bindings binding
JOIN integration_targets target
  ON target.project_id = binding.project_id
 AND target.integration_install_id = binding.integration_install_id
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
LEFT JOIN integration_routes route
  ON route.project_id = binding.project_id
 AND route.integration_install_id = binding.integration_install_id
 AND route.id = binding.integration_route_id
 AND route.state = 'active'
 AND route.deleted_at IS NULL
WHERE binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.receive_allowed
  AND binding.revoked_at IS NULL
  AND (
    (binding.integration_route_id IS NULL AND binding.source = 'legacy_target')
    OR route.id IS NOT NULL
  )
ORDER BY binding.send_allowed DESC, binding.created_at, binding.id
LIMIT 1;

-- name: ListAgentChannelTargets :many
WITH candidate_targets AS MATERIALIZED (
  SELECT DISTINCT ON (binding.target_created_at, binding.integration_target_id)
    binding.integration_target_id AS id,
    binding.target_created_at AS created_at
  FROM integration_target_bindings binding
  JOIN integration_targets target
    ON target.project_id = binding.project_id
   AND target.id = binding.integration_target_id
   AND target.created_at = binding.target_created_at
   AND target.deleted_at IS NULL
  JOIN integration_installs install
    ON install.project_id = binding.project_id
   AND install.id = binding.integration_install_id
   AND install.deleted_at IS NULL
  JOIN integration_apps app
    ON app.org_id = install.org_id
   AND app.id = install.integration_app_id
   AND app.deleted_at IS NULL
  WHERE binding.project_id = sqlc.arg(project_id)
    AND binding.agent_id = sqlc.arg(agent_id)
    AND binding.revoked_at IS NULL
    AND (
      NOT sqlc.arg(cursor_set)::boolean
      OR (binding.target_created_at, binding.integration_target_id) < (
        sqlc.arg(cursor_created_at)::timestamptz,
        sqlc.arg(cursor_id)::uuid
      )
    )
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
  ORDER BY binding.target_created_at DESC, binding.integration_target_id DESC
  LIMIT sqlc.arg(row_limit)
)
SELECT target.id, target.integration_install_id, target.target_ref,
  target.provider_ref, target.provider_ref_kind, target.display_name,
  target.created_at,
  install.provider, install.state AS install_state,
  app.connector_key, app.state AS app_state,
  bool_or(binding.receive_allowed) AS receive_allowed,
  bool_or(binding.send_allowed) AS send_allowed
FROM candidate_targets candidate
JOIN integration_targets target
  ON target.project_id = sqlc.arg(project_id)
 AND target.id = candidate.id
JOIN integration_target_bindings binding
  ON binding.project_id = target.project_id
 AND binding.agent_id = sqlc.arg(agent_id)
 AND binding.integration_target_id = target.id
 AND binding.revoked_at IS NULL
JOIN integration_installs install
  ON install.project_id = target.project_id
 AND install.id = target.integration_install_id
 AND install.deleted_at IS NULL
JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.deleted_at IS NULL
WHERE (
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
GROUP BY target.id, target.integration_install_id, target.target_ref,
  target.provider_ref, target.provider_ref_kind, target.display_name, target.created_at,
  install.provider, install.state, app.connector_key, app.state
ORDER BY target.created_at DESC, target.id DESC;

-- name: GetAgentChannelToolEligibility :one
WITH channel_mode AS MATERIALIZED (
  SELECT EXISTS (
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
    WHERE binding.project_id = sqlc.arg(project_id)
      AND binding.agent_id = sqlc.arg(agent_id)
      AND binding.revoked_at IS NULL
      AND binding.source <> 'legacy_target'
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
  ) AS allowed
)
SELECT channel_mode.allowed AS list_allowed,
  coalesce(channel_mode.allowed AND EXISTS (
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
    WHERE binding.project_id = sqlc.arg(project_id)
      AND binding.agent_id = sqlc.arg(agent_id)
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
  ), false)::boolean AS send_allowed
FROM channel_mode;

-- name: ListModelCallIntegrationOriginTargets :many
SELECT DISTINCT input.integration_target_id
FROM model_call_contexts context
CROSS JOIN LATERAL agent_model_call_opening_content_inputs(
  sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(turn_id), context.input_event_sequence
) opening(input_id, event_sequence)
JOIN agent_inputs input
  ON input.project_id = context.project_id
 AND input.agent_id = context.agent_id
 AND input.id = opening.input_id
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.id = sqlc.arg(model_call_context_id)
  AND input.integration_target_id IS NOT NULL
ORDER BY input.integration_target_id;

-- name: GetLatestModelCallIntegrationOrigin :one
SELECT input.integration_target_id, input.integration_target_binding_id
FROM model_call_contexts context
CROSS JOIN LATERAL agent_model_call_opening_content_inputs(
  sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(turn_id), context.input_event_sequence
) opening(input_id, event_sequence)
JOIN agent_inputs input
  ON input.project_id = context.project_id
 AND input.agent_id = context.agent_id
 AND input.id = opening.input_id
WHERE context.project_id = sqlc.arg(project_id)
  AND context.agent_id = sqlc.arg(agent_id)
  AND context.id = sqlc.arg(model_call_context_id)
  AND input.integration_target_id IS NOT NULL
ORDER BY opening.event_sequence DESC, input.id DESC
LIMIT 1;

-- name: ListInputIntegrationOriginTargets :many
SELECT DISTINCT integration_target_id
FROM agent_inputs
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND id = ANY(sqlc.arg(input_ids)::uuid[])
  AND integration_target_id IS NOT NULL
ORDER BY integration_target_id;

-- name: GetLatestInputIntegrationOrigin :one
SELECT input.integration_target_id, input.integration_target_binding_id
FROM agent_inputs input
JOIN agent_events event
  ON event.agent_id = input.agent_id
 AND event.id = input.admitted_event_id
WHERE input.project_id = sqlc.arg(project_id)
  AND input.agent_id = sqlc.arg(agent_id)
  AND input.id = ANY(sqlc.arg(input_ids)::uuid[])
  AND input.integration_target_id IS NOT NULL
ORDER BY event.sequence DESC, input.id DESC
LIMIT 1;

-- name: GetActiveSendBinding :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.send_allowed,
  binding.source, binding.metadata, binding.revoked_at,
  binding.created_at, binding.updated_at
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
  AND binding.id = sqlc.arg(id)
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
  );
-- name: RevokeIntegrationInstallTargetBindings :exec
UPDATE integration_target_bindings
SET revoked_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND revoked_at IS NULL;

-- name: RevokeIntegrationTargetBindingsForAgent :exec
UPDATE integration_target_bindings
SET revoked_at = transaction_timestamp(),
    updated_at = transaction_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND agent_id = sqlc.arg(agent_id)
  AND revoked_at IS NULL;

-- name: IntegrationTargetBindingBelongsToAgent :one
-- @sqlc-vet-disable integration-target-bindings-deleted-at
-- Ownership survives revocation: deleting this exact binding never affects a replacement.
SELECT EXISTS (
  SELECT 1
  FROM integration_target_bindings binding
  WHERE binding.project_id = sqlc.arg(project_id)
    AND binding.agent_id = sqlc.arg(agent_id)
    AND binding.id = sqlc.arg(id)
);

-- name: InsertIntegrationTargetBinding :one
INSERT INTO integration_target_bindings(
  project_id, agent_id, integration_install_id, integration_target_id,
  target_created_at, integration_route_id,
  receive_allowed, read_allowed, send_allowed,
  reply_receive_allowed, reply_read_allowed, reply_send_allowed, source, metadata,
  created_at, updated_at
)
SELECT
  sqlc.arg(project_id), sqlc.arg(agent_id), sqlc.arg(integration_install_id),
  target.id, target.created_at, sqlc.narg(integration_route_id),
  sqlc.arg(receive_allowed), sqlc.arg(read_allowed), sqlc.arg(send_allowed),
  sqlc.narg(reply_receive_allowed), sqlc.narg(reply_read_allowed), sqlc.narg(reply_send_allowed), sqlc.arg(source),
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
  target_created_at, integration_route_id, receive_allowed, read_allowed, send_allowed,
  reply_receive_allowed, reply_read_allowed, reply_send_allowed, source, metadata,
  revoked_at, created_at, updated_at;

-- name: GetActiveIntegrationTargetBindingByIdentity :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
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
  SELECT install.id, install.org_id, install.integration_app_id, install.integration_kind
  FROM integration_installs install
  WHERE install.project_id = sqlc.arg(project_id)
    AND install.id = sqlc.arg(integration_install_id)
    AND install.state = 'active'
    AND install.deleted_at IS NULL
  FOR SHARE OF install
), app_authority AS MATERIALIZED (
  SELECT install.id
  FROM install_authority install
  LEFT JOIN LATERAL (
    SELECT app.id FROM integration_apps app
    WHERE app.id = install.integration_app_id AND app.org_id = install.org_id
      AND app.state = 'active' AND app.deleted_at IS NULL
    FOR SHARE OF app
  ) app ON true
  WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
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

-- name: GetChannelBindingIdentity :one
-- @sqlc-vet-disable integration-target-bindings-deleted-at
-- Replay needs immutable ownership even when the binding or its parents are retired.
SELECT id, project_id, agent_id, integration_install_id, integration_target_id
FROM integration_target_bindings
WHERE project_id = sqlc.arg(project_id)
  AND integration_install_id = sqlc.arg(integration_install_id)
  AND id = sqlc.arg(id);

-- name: LookupChannelReceiptRouting :one
-- @sqlc-vet-disable integration-target-bindings-deleted-at
-- One snapshot distinguishes prior receive history from this receipt's partial
-- workflow fanout. Revoked receive grants still count; send/read-only grants do
-- not claim listener routing. These observations validate no lease or authority.
SELECT target.id AS channel_id, target.parent_channel_id,
  EXISTS (
    SELECT 1 FROM integration_target_bindings binding
    WHERE binding.project_id = receipt.project_id
      AND binding.integration_install_id = receipt.integration_install_id
      AND binding.integration_target_id = target.id
      AND binding.receive_allowed
  ) AS has_receive_binding_history,
  EXISTS (
    SELECT 1 FROM integration_event_outcomes outcome
    JOIN agent_inputs input
      ON input.project_id = outcome.project_id
     AND input.agent_id = outcome.agent_id
     AND input.id = outcome.agent_input_id
     AND input.integration_target_id = target.id
    WHERE outcome.project_id = receipt.project_id
      AND outcome.receipt_id = receipt.id
      AND outcome.delivery_key LIKE 'workflow:%'
  ) AS workflow_started
FROM integration_event_receipts receipt
LEFT JOIN integration_targets target
  ON target.project_id = receipt.project_id
 AND target.integration_install_id = receipt.integration_install_id
 AND target.provider_ref = sqlc.arg(provider_ref)
 AND target.deleted_at IS NULL
WHERE receipt.project_id = sqlc.arg(project_id)
  AND receipt.integration_install_id = sqlc.arg(integration_install_id)
  AND receipt.id = sqlc.arg(receipt_id);

-- name: ListChannelReceiveBindings :many
SELECT DISTINCT ON (binding.agent_id) binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
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
LEFT JOIN integration_apps app
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
JOIN projects project
  ON project.id = binding.project_id
 AND project.org_id = install.org_id
 AND project.deleted_at IS NULL
JOIN orgs org
  ON org.id = project.org_id
 AND org.deleted_at IS NULL
JOIN agents agent
  ON agent.project_id = binding.project_id
 AND agent.id = binding.agent_id
 AND agent.state = 'active'
WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
  AND binding.project_id = sqlc.arg(project_id)
  AND binding.integration_install_id = sqlc.arg(integration_install_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.receive_allowed
  AND binding.revoked_at IS NULL
  AND binding.agent_id > sqlc.arg(after_agent_id)
  AND (binding.integration_route_id IS NULL OR route.id IS NOT NULL)
ORDER BY binding.agent_id, binding.id
LIMIT sqlc.arg(row_limit);

-- name: GetIntegrationTargetBinding :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
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

-- name: AgentHasChannelBindingHistory :one
-- @sqlc-vet-disable integration-target-bindings-deleted-at
-- Automatic discovery must not recreate a grant the owner already revoked.
SELECT EXISTS (
  SELECT 1 FROM integration_target_bindings
  WHERE project_id = sqlc.arg(project_id)
    AND agent_id = sqlc.arg(agent_id)
    AND integration_target_id = sqlc.arg(integration_target_id)
);

-- name: GetActiveSendBindingForTarget :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
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
LEFT JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.state = 'active'
 AND app.deleted_at IS NULL
WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
  AND binding.project_id = sqlc.arg(project_id)
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

-- name: LockActiveIntegrationTargetBinding :one
WITH install_authority AS MATERIALIZED (
  SELECT install.id, install.org_id, install.integration_app_id, install.integration_kind
  FROM integration_installs install
  WHERE install.project_id = sqlc.arg(project_id)
    AND install.id = sqlc.arg(integration_install_id)
    AND install.state = 'active'
    AND install.deleted_at IS NULL
  FOR SHARE OF install
), app_authority AS MATERIALIZED (
  SELECT install.id
  FROM install_authority install
  LEFT JOIN LATERAL (
    SELECT app.id FROM integration_apps app
    WHERE app.id = install.integration_app_id AND app.org_id = install.org_id
      AND app.state = 'active' AND app.deleted_at IS NULL
    FOR SHARE OF app
  ) app ON true
  WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
)
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
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
  AND CASE WHEN sqlc.arg(for_interaction_response)::boolean
    THEN binding.send_allowed ELSE binding.receive_allowed END
  AND binding.revoked_at IS NULL
  AND (
    binding.integration_route_id IS NULL
    OR route.id IS NOT NULL
  )
FOR SHARE OF binding;

-- name: GetActiveReceiveBindingForTarget :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
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
LEFT JOIN integration_apps app
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
WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
  AND binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.receive_allowed
  AND binding.revoked_at IS NULL
  AND (
    binding.integration_route_id IS NULL
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
  LEFT JOIN integration_apps app
    ON app.org_id = install.org_id
   AND app.id = install.integration_app_id
   AND app.deleted_at IS NULL
  WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
  AND binding.project_id = sqlc.arg(project_id)
    AND binding.agent_id = sqlc.arg(agent_id)
    AND binding.revoked_at IS NULL
    AND (sqlc.narg(parent_channel_id)::uuid IS NULL OR target.parent_channel_id = sqlc.narg(parent_channel_id)::uuid)
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
SELECT target.id, target.integration_install_id, target.parent_channel_id,
  target.provider_ref, target.provider_ref_kind, target.display_name,
  target.created_at,
  install.provider, install.integration_kind, install.state AS install_state,
  app.connector_key, app.state AS app_state,
  bool_or(binding.receive_allowed) AS receive_allowed,
  coalesce(bool_or(binding.read_allowed AND definition.capabilities -> 'read' = 'true'::jsonb), false)::boolean AS read_allowed,
  coalesce(bool_or(binding.send_allowed AND definition.capabilities -> 'send' = 'true'::jsonb), false)::boolean AS send_allowed
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
LEFT JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.deleted_at IS NULL
LEFT JOIN integration_channel_definitions definition
  ON definition.project_id = target.project_id
 AND definition.integration_install_id = target.integration_install_id
 AND definition.id = target.channel_definition_id
WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL)) AND (
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
GROUP BY target.id, target.integration_install_id, target.parent_channel_id,
  target.provider_ref, target.provider_ref_kind, target.display_name, target.created_at,
  install.provider, install.integration_kind, install.state, app.connector_key, app.state
ORDER BY target.created_at DESC, target.id DESC;

-- name: GetAgentChannelToolEligibility :one
-- Discovery remains available with any explicit channel relationship. Read/send
-- additionally require current implementation support and a live direct grant.
SELECT (count(*) > 0)::boolean AS list_allowed,
  coalesce(bool_or(binding.read_allowed AND definition.capabilities -> 'read' = 'true'::jsonb), false)::boolean AS read_allowed,
  coalesce(bool_or(binding.send_allowed AND definition.capabilities -> 'send' = 'true'::jsonb), false)::boolean AS send_allowed
FROM integration_target_bindings binding
JOIN integration_targets target
  ON target.project_id = binding.project_id AND target.id = binding.integration_target_id
 AND target.deleted_at IS NULL
JOIN integration_installs install
  ON install.project_id = binding.project_id AND install.id = binding.integration_install_id
 AND install.state = 'active' AND install.deleted_at IS NULL
LEFT JOIN integration_apps app
  ON app.org_id = install.org_id AND app.id = install.integration_app_id
 AND app.state = 'active' AND app.deleted_at IS NULL
LEFT JOIN integration_channel_definitions definition
  ON definition.project_id = target.project_id AND definition.integration_install_id = target.integration_install_id
 AND definition.id = target.channel_definition_id
WHERE binding.project_id = sqlc.arg(project_id) AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.revoked_at IS NULL
  AND (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
  AND (binding.integration_route_id IS NULL OR EXISTS (
    SELECT 1 FROM integration_routes route
    WHERE route.project_id = binding.project_id AND route.integration_install_id = binding.integration_install_id
      AND route.id = binding.integration_route_id AND route.state = 'active' AND route.deleted_at IS NULL
  ));

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


-- name: GetActiveReadBindingForTarget :one
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
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
LEFT JOIN integration_apps app
  ON app.org_id = install.org_id
 AND app.id = install.integration_app_id
 AND app.state = 'active'
 AND app.deleted_at IS NULL
WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
  AND binding.project_id = sqlc.arg(project_id)
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.read_allowed
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

-- name: LockChannelOperationBinding :one
-- A request pins one eligible binding, never a union of independent grants.
-- Supplying an ID rechecks that exact binding without substituting a replacement.
WITH agent_authority AS MATERIALIZED (
  SELECT agent.id
  FROM agents agent
  WHERE agent.project_id = sqlc.arg(project_id)
    AND agent.id = sqlc.arg(agent_id)
    AND agent.state = 'active'
  FOR UPDATE OF agent
), install_authority AS MATERIALIZED (
  SELECT install.id, install.org_id, install.integration_app_id, install.integration_kind
  FROM integration_installs install
  JOIN agent_authority agent ON true
  WHERE install.project_id = sqlc.arg(project_id)
    AND install.id = sqlc.arg(integration_install_id)
    AND install.state = 'active'
    AND install.deleted_at IS NULL
  FOR SHARE OF install
), app_authority AS MATERIALIZED (
  SELECT install.id
  FROM install_authority install
  LEFT JOIN LATERAL (
    SELECT app.id FROM integration_apps app
    WHERE app.id = install.integration_app_id AND app.org_id = install.org_id
      AND app.state = 'active' AND app.deleted_at IS NULL
    FOR SHARE OF app
  ) app ON true
  WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
)
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
  binding.source, binding.metadata, binding.revoked_at, binding.created_at, binding.updated_at
FROM app_authority install
JOIN integration_targets target ON target.integration_install_id = install.id
JOIN integration_target_bindings binding
  ON binding.project_id = target.project_id
 AND binding.integration_install_id = target.integration_install_id
 AND binding.integration_target_id = target.id
LEFT JOIN LATERAL (
  SELECT route.id
  FROM integration_routes route
  WHERE route.project_id = binding.project_id
    AND route.integration_install_id = binding.integration_install_id
    AND route.id = binding.integration_route_id
    AND route.state = 'active' AND route.deleted_at IS NULL
  FOR SHARE OF route
) route ON true
WHERE target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(integration_target_id)
  AND target.deleted_at IS NULL
  AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.revoked_at IS NULL
  AND (sqlc.narg(binding_id)::uuid IS NULL OR binding.id = sqlc.narg(binding_id)::uuid)
  AND CASE sqlc.arg(operation)::text
    WHEN 'read' THEN binding.read_allowed
    WHEN 'send' THEN binding.send_allowed
    ELSE false END
  AND (NOT sqlc.arg(creates_reply_channel)::boolean OR binding.reply_receive_allowed IS NOT NULL)
  AND (binding.integration_route_id IS NULL OR route.id IS NOT NULL)
ORDER BY binding.created_at, binding.id
LIMIT 1
FOR SHARE OF target, binding;

-- name: LockInitialChannelBinding :one
-- The target/install locks are held by initial registration. Reuse one current
-- relationship without widening it; any revoked history forbids auto-recreation.
SELECT binding.id, binding.project_id, binding.agent_id,
  binding.integration_install_id, binding.integration_target_id,
  binding.target_created_at, binding.integration_route_id,
  binding.receive_allowed, binding.read_allowed, binding.send_allowed,
  binding.reply_receive_allowed, binding.reply_read_allowed, binding.reply_send_allowed,
  binding.source, binding.metadata, binding.revoked_at, binding.created_at, binding.updated_at
FROM integration_target_bindings binding
LEFT JOIN LATERAL (
  SELECT route.id FROM integration_routes route
  WHERE route.project_id = binding.project_id AND route.integration_install_id = binding.integration_install_id
    AND route.id = binding.integration_route_id AND route.state = 'active' AND route.deleted_at IS NULL
  FOR SHARE OF route
) route ON true
WHERE binding.project_id = sqlc.arg(project_id) AND binding.agent_id = sqlc.arg(agent_id)
  AND binding.integration_target_id = sqlc.arg(integration_target_id)
  AND binding.revoked_at IS NULL
  AND (NOT sqlc.arg(require_receive)::boolean OR binding.receive_allowed)
  AND (binding.integration_route_id IS NULL OR route.id IS NOT NULL)
ORDER BY binding.created_at, binding.id
LIMIT 1
FOR SHARE OF binding;

-- Discovery and operation preparation use the current definition and direct
-- grants. A parent link is context, never inherited authorization.
-- name: GetAgentChannelAccess :one
SELECT target.id, target.parent_channel_id, target.display_name,
  target.provider_ref, target.provider_ref_kind, target.provider_metadata,
  install.id AS integration_install_id, install.integration_kind, app.id AS integration_app_id,
  app.connector_key, install.provider,
  (install.state = 'active' AND (install.integration_kind = 'external'
    OR (install.integration_kind = 'managed' AND app.id IS NOT NULL AND app.state = 'active')))::boolean AS active,
  definition.id AS definition_id, definition.implementation_key, definition.kind,
  definition.description, definition.send_params_schema, definition.capabilities,
  grants.receive_allowed, grants.read_allowed, grants.send_allowed, grants.reply_channel_allowed
FROM integration_targets target
JOIN integration_installs install
  ON install.project_id = target.project_id AND install.id = target.integration_install_id
 AND install.deleted_at IS NULL
LEFT JOIN integration_apps app
  ON app.org_id = install.org_id AND app.id = install.integration_app_id
 AND app.deleted_at IS NULL
JOIN integration_channel_definitions definition
  ON definition.project_id = target.project_id
 AND definition.integration_install_id = target.integration_install_id
 AND definition.id = target.channel_definition_id
JOIN LATERAL (
  SELECT bool_or(binding.receive_allowed)::boolean AS receive_allowed,
    bool_or(binding.read_allowed)::boolean AS read_allowed,
    bool_or(binding.send_allowed)::boolean AS send_allowed,
    bool_or(binding.send_allowed AND binding.reply_receive_allowed IS NOT NULL)::boolean AS reply_channel_allowed
  FROM integration_target_bindings binding
  WHERE binding.project_id = target.project_id
    AND binding.agent_id = sqlc.arg(agent_id)
    AND binding.integration_target_id = target.id
    AND binding.revoked_at IS NULL
    AND (
      binding.integration_route_id IS NULL
      OR EXISTS (
        SELECT 1 FROM integration_routes route
        WHERE route.project_id = binding.project_id
          AND route.integration_install_id = binding.integration_install_id
          AND route.id = binding.integration_route_id
          AND route.state = 'active' AND route.deleted_at IS NULL
      )
    )
  HAVING count(*) > 0
) grants ON true
WHERE (install.integration_kind = 'external' OR (install.integration_kind = 'managed' AND app.id IS NOT NULL))
  AND target.project_id = sqlc.arg(project_id)
  AND target.id = sqlc.arg(channel_id)
  AND target.deleted_at IS NULL;

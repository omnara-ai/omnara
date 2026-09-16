-- Project setup discovers registered addresses independently of agent grants.
-- Opaque metadata and message content are not part of this bounded projection.
-- name: ListRegisteredChannels :many
SELECT target.id, target.channel_definition_id, target.parent_channel_id,
       target.provider_ref, target.provider_ref_kind, target.display_name, target.created_at
FROM integration_targets target
JOIN integration_installs install
  ON install.project_id = target.project_id AND install.id = target.integration_install_id
 AND install.deleted_at IS NULL
WHERE target.project_id = sqlc.arg(project_id)
  AND target.integration_install_id = sqlc.arg(integration_install_id)
  AND target.deleted_at IS NULL
  AND (NOT sqlc.arg(cursor_set)::boolean
    OR (target.created_at, target.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY target.created_at DESC, target.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- Only unknown-installation/ping verification uses these credential candidates,
-- capped at 16 by the caller. Known-installation fanout resolves every matching
-- app separately and verifies its own setup, without this fallback cap.
-- Disconnected apps can still verify the GitHub App's signature; they cannot
-- receive integration input.
-- Deduplicate shared grants before bounding work. The HTTP verifier reads the
-- payload through secretstore again, so this lookup does not grant secret access.
-- name: ListGitHubWebhookCredentialApps :many
SELECT DISTINCT ON (app.credential_secret_id)
  app.id, app.org_id, app.project_id, app.installed_by_user_id,
  app.state,
  app.provider_tenant_id, app.provider_account_ref,
  app.provider_agent_display_name, app.credential_secret_id,
  app.provider_config, app.provider_identity, app.provider_metadata,
  app.last_oauth_flow_id, app.deleted_at, app.created_at, app.updated_at,
  app.name, app.app_type, app.settings, app.setup_revision
FROM project_apps app
JOIN projects project ON project.id = app.project_id AND project.org_id = app.org_id
JOIN orgs org ON org.id = app.org_id
JOIN secrets credential ON credential.id = app.credential_secret_id AND credential.org_id = app.org_id
JOIN secret_versions version ON version.id = credential.current_version_id AND version.secret_id = credential.id
WHERE app.app_type = ANY(sqlc.arg(app_types)::text[])
  AND app.provider_tenant_id = sqlc.arg(github_app_id)::text
  AND app.deleted_at IS NULL AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  AND credential.deleted_at IS NULL AND credential.management_kind = 'tenant'
  AND credential.kind = 'github_app_credentials'
  AND (
    (credential.owner_kind = 'project' AND credential.owner_project_id = app.project_id)
    OR EXISTS (
      SELECT 1 FROM secret_grants grant_access
      WHERE grant_access.org_id = app.org_id AND grant_access.secret_id = credential.id
        AND grant_access.target_project_id = app.project_id
    )
  )
ORDER BY app.credential_secret_id, app.id
LIMIT sqlc.arg(row_limit)::integer;

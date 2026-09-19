-- App-level ping/bootstrap verification needs a live, project-authorized
-- credential representative, not a new App registry. Disabled installations can
-- still verify the App's signature; they cannot receive integration input.
-- Deduplicate shared grants before bounding work. The HTTP verifier reads the
-- payload through secretstore again, so this lookup does not grant secret access.
-- name: ListGitHubWebhookCredentialConnections :many
SELECT DISTINCT ON (connection.credential_secret_id)
  connection.id, connection.org_id, connection.project_id, connection.installed_by_user_id,
  connection.provider, connection.state,
  connection.provider_tenant_id, connection.provider_account_ref,
  connection.provider_agent_display_name, connection.credential_secret_id,
  connection.provider_config, connection.provider_identity, connection.provider_metadata,
  connection.last_oauth_flow_id, connection.deleted_at, connection.created_at, connection.updated_at
FROM integration_connections connection
JOIN projects project ON project.id = connection.project_id AND project.org_id = connection.org_id
JOIN orgs org ON org.id = connection.org_id
JOIN secrets credential ON credential.id = connection.credential_secret_id AND credential.org_id = connection.org_id
JOIN secret_versions version ON version.id = credential.current_version_id AND version.secret_id = credential.id
WHERE connection.provider = 'github'
  AND connection.provider_tenant_id = sqlc.arg(app_id)::text
  AND connection.deleted_at IS NULL AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  AND credential.deleted_at IS NULL AND credential.management_kind = 'tenant'
  AND credential.kind = 'github_app_credentials'
  AND (
    (credential.owner_kind = 'project' AND credential.owner_project_id = connection.project_id)
    OR EXISTS (
      SELECT 1 FROM secret_grants grant_access
      WHERE grant_access.org_id = connection.org_id AND grant_access.secret_id = credential.id
        AND grant_access.target_project_id = connection.project_id
    )
  )
ORDER BY connection.credential_secret_id, connection.id
LIMIT sqlc.arg(row_limit)::integer;

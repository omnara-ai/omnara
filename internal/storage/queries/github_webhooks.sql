-- Ping/unknown-installation verification only; ordinary deliveries verify each
-- matching integration independently rather than borrowing these fallback credentials.
-- Include disconnected integrations so signed callbacks can be acknowledged without work.
-- name: ListGitHubWebhookCredentialIntegrations :many
SELECT DISTINCT ON (integration.credential_secret_id)
  integration.id, integration.org_id, integration.project_id, integration.installed_by_user_id,
  integration.state,
  integration.provider_tenant_id, integration.provider_account_ref,
  integration.provider_agent_display_name, integration.credential_secret_id,
  integration.provider_config, integration.provider_identity, integration.provider_metadata,
  integration.last_oauth_flow_id, integration.deleted_at, integration.created_at, integration.updated_at,
  integration.name, integration.integration_type, integration.settings, integration.setup_revision
FROM project_integrations integration
JOIN projects project ON project.id = integration.project_id AND project.org_id = integration.org_id
JOIN orgs org ON org.id = integration.org_id
JOIN secrets credential ON credential.id = integration.credential_secret_id AND credential.org_id = integration.org_id
JOIN secret_versions version ON version.id = credential.current_version_id AND version.secret_id = credential.id
WHERE integration.integration_type = ANY(sqlc.arg(integration_types)::text[])
  AND integration.provider_tenant_id = sqlc.arg(github_app_id)::text
  AND integration.deleted_at IS NULL AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  AND credential.deleted_at IS NULL AND credential.management_kind = 'tenant'
  AND credential.kind = 'github_app_credentials'
  AND (
    (credential.owner_kind = 'project' AND credential.owner_project_id = integration.project_id)
    OR EXISTS (
      SELECT 1 FROM secret_grants grant_access
      WHERE grant_access.org_id = integration.org_id AND grant_access.secret_id = credential.id
        AND grant_access.target_project_id = integration.project_id
    )
  )
ORDER BY integration.credential_secret_id, integration.id
LIMIT sqlc.arg(row_limit)::integer;

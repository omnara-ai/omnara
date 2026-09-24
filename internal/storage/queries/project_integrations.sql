-- name: InsertProjectIntegration :one
INSERT INTO project_integrations(org_id, project_id, name, integration_type, settings, state, created_at, updated_at)
VALUES (sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(name), sqlc.arg(integration_type),
        sqlc.arg(settings), 'disconnected', statement_timestamp(), statement_timestamp())
RETURNING id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision;

-- name: GetProjectIntegration :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision
FROM project_integrations
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: GetProjectIntegrationByName :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision
FROM project_integrations
WHERE project_id = sqlc.arg(project_id) AND name = sqlc.arg(name) AND deleted_at IS NULL;

-- name: GetProjectIntegrationByID :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision
FROM project_integrations WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: LockProjectIntegration :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision
FROM project_integrations
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- Lock order: project/integration gates (sorted integration IDs), inbox receipt, conversation gate, profile/agent rows.
-- name: LockProjectIntegrationLifecycleShared :exec
SELECT pg_advisory_xact_lock_shared(hashtextextended('project_integration:' || sqlc.arg(integration_id)::uuid::text, 0));

-- name: LockProjectIntegrationLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(hashtextextended('project_integration:' || sqlc.arg(integration_id)::uuid::text, 0));

-- name: UpdateProjectIntegrationSettings :one
UPDATE project_integrations SET settings = sqlc.arg(settings), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision;

-- name: ConfigureProjectIntegration :one
UPDATE project_integrations
SET installed_by_user_id = sqlc.arg(installed_by_user_id),
    provider_tenant_id = sqlc.arg(provider_tenant_id),
    provider_account_ref = sqlc.arg(provider_account_ref),
    provider_agent_display_name = sqlc.arg(provider_agent_display_name),
    credential_secret_id = sqlc.arg(credential_secret_id)::uuid,
    provider_config = sqlc.arg(provider_config), provider_identity = sqlc.arg(provider_identity),
    provider_metadata = sqlc.arg(provider_metadata),
    last_oauth_flow_id = coalesce(sqlc.narg(oauth_flow_id)::uuid, last_oauth_flow_id),
    state = 'active', setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
  AND setup_revision = sqlc.arg(expected_setup_revision)
RETURNING id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision;

-- name: DisconnectProjectIntegration :execrows
UPDATE project_integrations
SET state = 'disconnected', setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND (sqlc.narg(expected_setup_revision)::bigint IS NULL OR setup_revision = sqlc.narg(expected_setup_revision));

-- name: DeleteProjectIntegration :execrows
UPDATE project_integrations
SET state = 'disconnected', credential_secret_id = NULL, deleted_at = statement_timestamp(),
    setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListProjectIntegrations :many
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_type, settings, setup_revision
FROM project_integrations
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (NOT sqlc.arg(cursor_set)::boolean OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListProjectIntegrationsByProviderIdentity :many
SELECT integration.id, integration.org_id, integration.project_id, integration.installed_by_user_id, integration.state, integration.provider_tenant_id, integration.provider_account_ref, integration.provider_agent_display_name, integration.credential_secret_id, integration.provider_config, integration.provider_identity, integration.provider_metadata, integration.last_oauth_flow_id, integration.deleted_at, integration.created_at, integration.updated_at, integration.name, integration.integration_type, integration.settings, integration.setup_revision
FROM project_integrations integration
JOIN projects project ON project.id = integration.project_id
JOIN orgs org ON org.id = integration.org_id
WHERE integration.integration_type = ANY(sqlc.arg(integration_types)::text[]) AND integration.provider_tenant_id = sqlc.arg(provider_tenant_id)
  AND (sqlc.narg(provider_account_ref)::text IS NULL OR integration.provider_account_ref = sqlc.narg(provider_account_ref)::text)
  AND (integration.state = 'active' OR (sqlc.arg(include_disconnected)::boolean
    AND integration.state = 'disconnected' AND integration.credential_secret_id IS NOT NULL))
  AND integration.deleted_at IS NULL
  AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  AND (sqlc.narg(after_id)::uuid IS NULL OR integration.id > sqlc.narg(after_id)::uuid)
ORDER BY integration.id LIMIT sqlc.arg(row_limit);

-- name: AgentProfileHasProjectIntegration :one
SELECT EXISTS (
    SELECT 1 FROM project_integrations integration
    WHERE integration.project_id = sqlc.arg(project_id) AND integration.deleted_at IS NULL
      AND integration.settings @> jsonb_build_object('launcher', jsonb_build_object('slots',
          jsonb_build_array(jsonb_build_object('agent_profile_id', sqlc.arg(profile_id)::uuid::text))))
) AS referenced;

-- name: CountProjectIntegrations :one
SELECT count(*)::bigint FROM project_integrations WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteProjectIntegrationsForProjectDeletion :exec
UPDATE project_integrations
SET state = 'disconnected', credential_secret_id = NULL, deleted_at = statement_timestamp(),
    setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: IntegrationOAuthFlowConsumed :one
-- @sqlc-vet-disable project-integrations-deleted-at
-- Tombstones also prevent reusing a completed setup attempt.
SELECT EXISTS (SELECT 1 FROM project_integrations WHERE last_oauth_flow_id = sqlc.arg(flow_id)) AS consumed;

-- name: ListProjectIntegrationMetadataByIDs :many
SELECT id, project_id, name, integration_type, state, deleted_at
FROM project_integrations
WHERE project_id = sqlc.arg(project_id) AND id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY id;

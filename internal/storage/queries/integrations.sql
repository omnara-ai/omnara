-- name: InsertIntegration :one
INSERT INTO integrations(org_id, project_id, name, integration_kind, settings, state, created_at, updated_at)
VALUES (sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(name), sqlc.arg(integration_kind),
        sqlc.arg(settings), 'disconnected', statement_timestamp(), statement_timestamp())
RETURNING id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision;

-- name: GetIntegration :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision
FROM integrations
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: GetIntegrationByName :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision
FROM integrations
WHERE project_id = sqlc.arg(project_id) AND name = sqlc.arg(name) AND deleted_at IS NULL;

-- name: GetIntegrationByID :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision
FROM integrations WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: LockIntegration :one
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision
FROM integrations
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- name: LockIntegrationLifecycleShared :exec
SELECT pg_advisory_xact_lock_shared(hashtextextended('integration:' || sqlc.arg(integration_id)::uuid::text, 0));

-- name: LockIntegrationLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(hashtextextended('integration:' || sqlc.arg(integration_id)::uuid::text, 0));

-- name: UpdateIntegrationSettings :one
UPDATE integrations SET settings = sqlc.arg(settings), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision;

-- name: ConfigureIntegration :one
UPDATE integrations
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
RETURNING id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision;

-- name: DisconnectIntegration :execrows
UPDATE integrations
SET state = 'disconnected', setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND (sqlc.narg(expected_setup_revision)::bigint IS NULL OR setup_revision = sqlc.narg(expected_setup_revision));

-- name: DeleteIntegration :execrows
UPDATE integrations
SET state = 'disconnected', credential_secret_id = NULL, deleted_at = statement_timestamp(),
    setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListIntegrations :many
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision
FROM integrations
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (NOT sqlc.arg(cursor_set)::boolean OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListIntegrationsForProjects :many
SELECT id, org_id, project_id, installed_by_user_id, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, integration_kind, settings, setup_revision
FROM integrations
WHERE project_id = ANY(sqlc.arg(project_ids)::uuid[]) AND deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (NOT sqlc.arg(cursor_set)::boolean OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListIntegrationsByProviderIdentity :many
SELECT integration.id, integration.org_id, integration.project_id, integration.installed_by_user_id, integration.state, integration.provider_tenant_id, integration.provider_account_ref, integration.provider_agent_display_name, integration.credential_secret_id, integration.provider_config, integration.provider_identity, integration.provider_metadata, integration.last_oauth_flow_id, integration.deleted_at, integration.created_at, integration.updated_at, integration.name, integration.integration_kind, integration.settings, integration.setup_revision
FROM integrations integration
JOIN projects project ON project.id = integration.project_id
JOIN orgs org ON org.id = integration.org_id
WHERE integration.integration_kind = ANY(sqlc.arg(integration_kinds)::text[]) AND integration.provider_tenant_id = sqlc.arg(provider_tenant_id)
  AND (sqlc.narg(provider_account_ref)::text IS NULL OR integration.provider_account_ref = sqlc.narg(provider_account_ref)::text)
  AND (integration.state = 'active' OR (sqlc.arg(include_disconnected)::boolean
    AND integration.state = 'disconnected' AND integration.credential_secret_id IS NOT NULL))
  AND integration.deleted_at IS NULL
  AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  AND (sqlc.narg(after_id)::uuid IS NULL OR integration.id > sqlc.narg(after_id)::uuid)
ORDER BY integration.id LIMIT sqlc.arg(row_limit);

-- name: CountIntegrations :one
SELECT count(*)::bigint FROM integrations WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteIntegrationsForProjectDeletion :exec
UPDATE integrations
SET state = 'disconnected', credential_secret_id = NULL, deleted_at = statement_timestamp(),
    setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: IntegrationOAuthFlowConsumed :one
-- @sqlc-vet-disable integrations-deleted-at
SELECT EXISTS (SELECT 1 FROM integrations WHERE last_oauth_flow_id = sqlc.arg(flow_id)) AS consumed;

-- name: ListIntegrationMetadataByIDs :many
SELECT id, project_id, name, integration_kind, state, deleted_at
FROM integrations
WHERE project_id = sqlc.arg(project_id) AND id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY id;

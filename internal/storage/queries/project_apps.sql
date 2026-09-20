-- Apps own credentials and behavior. Metadata writes never change setup_revision.
-- name: InsertProjectApp :one
INSERT INTO project_apps(org_id, project_id, name, definition_id, provider, settings, state, created_at, updated_at)
VALUES (sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(name), sqlc.arg(definition_id),
        sqlc.arg(provider), sqlc.arg(settings), 'disconnected', statement_timestamp(), statement_timestamp())
RETURNING id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision;

-- name: GetProjectApp :one
SELECT id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: GetProjectAppByName :one
SELECT id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND name = sqlc.arg(name) AND deleted_at IS NULL;

-- Private provider ingress resolves identity before a project principal exists.
-- name: GetProjectAppByID :one
SELECT id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision
FROM project_apps WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: LockProjectApp :one
SELECT id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- Take the lifecycle gate before app/profile/agent row locks. Sorted app IDs
-- provide a common order when one agent uses several apps.
-- name: LockProjectAppLifecycleShared :exec
SELECT pg_advisory_xact_lock_shared(hashtextextended('project_app:' || sqlc.arg(app_id)::uuid::text, 0));

-- name: LockProjectAppLifecycleExclusive :exec
SELECT pg_advisory_xact_lock(hashtextextended('project_app:' || sqlc.arg(app_id)::uuid::text, 0));

-- name: UpdateProjectAppSettings :one
UPDATE project_apps SET settings = sqlc.arg(settings), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision;

-- Provider identity and secret version were verified before entering this
-- transaction. The revision rejects a stale setup result; unrelated settings
-- edits deliberately do not invalidate that verification.
-- name: ConfigureProjectApp :one
UPDATE project_apps
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
RETURNING id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision;

-- name: DisconnectProjectApp :execrows
UPDATE project_apps
SET state = 'disconnected', setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id)
  AND deleted_at IS NULL
  AND (sqlc.narg(expected_setup_revision)::bigint IS NULL OR setup_revision = sqlc.narg(expected_setup_revision));

-- name: DeleteProjectApp :execrows
UPDATE project_apps
SET state = 'disconnected', credential_secret_id = NULL, deleted_at = statement_timestamp(),
    setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListProjectApps :many
SELECT id, org_id, project_id, installed_by_user_id, provider, state, provider_tenant_id, provider_account_ref, provider_agent_display_name, credential_secret_id, provider_config, provider_identity, provider_metadata, last_oauth_flow_id, deleted_at, created_at, updated_at, name, definition_id, settings, setup_revision
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (NOT sqlc.arg(cursor_set)::boolean OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- A physical bot can have independent saved apps, including in other projects.
-- Iterate all pages; a truncated fanout must never be acknowledged as complete.
-- name: ListProjectAppsByProviderIdentity :many
SELECT app.id, app.org_id, app.project_id, app.installed_by_user_id, app.provider, app.state, app.provider_tenant_id, app.provider_account_ref, app.provider_agent_display_name, app.credential_secret_id, app.provider_config, app.provider_identity, app.provider_metadata, app.last_oauth_flow_id, app.deleted_at, app.created_at, app.updated_at, app.name, app.definition_id, app.settings, app.setup_revision
FROM project_apps app
JOIN projects project ON project.id = app.project_id
JOIN orgs org ON org.id = app.org_id
WHERE app.provider = sqlc.arg(provider) AND app.provider_tenant_id = sqlc.arg(provider_tenant_id)
  AND (sqlc.narg(provider_account_ref)::text IS NULL OR app.provider_account_ref = sqlc.narg(provider_account_ref)::text)
  AND (app.state = 'active' OR (sqlc.arg(include_disconnected)::boolean
    AND app.state = 'disconnected' AND app.credential_secret_id IS NOT NULL))
  AND app.deleted_at IS NULL
  AND project.deleted_at IS NULL AND org.deleted_at IS NULL
  AND (sqlc.narg(after_id)::uuid IS NULL OR app.id > sqlc.narg(after_id)::uuid)
ORDER BY app.id LIMIT sqlc.arg(row_limit);

-- name: AgentProfileHasProjectApp :one
SELECT EXISTS (
    SELECT 1 FROM project_apps app
    WHERE app.project_id = sqlc.arg(project_id) AND app.deleted_at IS NULL
      AND app.settings @> jsonb_build_object('launcher', jsonb_build_object('slots',
          jsonb_build_array(jsonb_build_object('agent_profile_id', sqlc.arg(profile_id)::uuid::text))))
) AS referenced;

-- name: CountProjectApps :one
SELECT count(*)::bigint FROM project_apps WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: DeleteProjectAppsForProjectDeletion :exec
UPDATE project_apps
SET state = 'disconnected', credential_secret_id = NULL, deleted_at = statement_timestamp(),
    setup_revision = setup_revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- name: IntegrationOAuthFlowConsumed :one
-- @sqlc-vet-disable project-apps-deleted-at
-- Tombstones also prevent reusing a completed setup attempt.
SELECT EXISTS (SELECT 1 FROM project_apps WHERE last_oauth_flow_id = sqlc.arg(flow_id)) AS consumed;

-- Metadata keeps stored configs interpretable after app deletion. These reads
-- grant no execution authority; tools check live setup immediately before I/O.
-- name: ListProjectAppMetadataByIDs :many
SELECT id, project_id, name, definition_id, provider, state, deleted_at
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY id;

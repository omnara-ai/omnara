-- name: InsertProjectApp :one
INSERT INTO project_apps(project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled)
VALUES (sqlc.arg(project_id), sqlc.arg(name), sqlc.arg(definition_id), sqlc.arg(settings),
        sqlc.narg(launch_connection_id), sqlc.narg(launch_scope_kind), sqlc.narg(launch_scope_ref), sqlc.arg(enabled))
RETURNING id, project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at;

-- A disable-only edit cannot acquire destination/secret locks after its app row.
-- Match the observed setup atomically; concurrent settings edits must be re-read.
-- name: DisableUnchangedProjectApp :one
UPDATE project_apps
SET enabled = false, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
  AND definition_id = sqlc.arg(definition_id) AND name = sqlc.arg(name)
  AND settings = sqlc.arg(settings)::jsonb
RETURNING id, project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at;

-- name: GetProjectApp :one
SELECT id, project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: LockProjectApp :one
SELECT id FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- name: UpdateProjectApp :one
UPDATE project_apps
SET name = sqlc.arg(name), settings = sqlc.arg(settings),
    launch_connection_id = sqlc.narg(launch_connection_id), launch_scope_kind = sqlc.narg(launch_scope_kind),
    launch_scope_ref = sqlc.narg(launch_scope_ref), enabled = sqlc.arg(enabled), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND definition_id = sqlc.arg(definition_id) AND deleted_at IS NULL
RETURNING id, project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at;

-- name: DeleteProjectApp :execrows
UPDATE project_apps SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ListProjectApps :many
SELECT id, project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (NOT sqlc.arg(cursor_set)::boolean OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListProjectAppLaunchers :many
SELECT id, project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND launch_connection_id = sqlc.arg(connection_id)
  AND launch_scope_kind = sqlc.arg(scope_kind) AND launch_scope_ref = sqlc.arg(scope_ref)
  AND enabled AND deleted_at IS NULL
ORDER BY id;

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
UPDATE project_apps SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL;

-- OAuth reconnect preserves the oldest configured setup, including a disabled
-- launcher or one removed by an operator. Project app count is resource-bounded.
-- name: GetConnectionProjectAppForSetup :one
SELECT id, project_id, name, definition_id, settings, launch_connection_id, launch_scope_kind, launch_scope_ref, enabled, deleted_at, created_at, updated_at
FROM project_apps
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL
  AND definition_id = sqlc.arg(definition_id)
  AND settings->'resource'->>'connection' = sqlc.arg(connection_ref)::text
ORDER BY created_at, id
LIMIT 1;

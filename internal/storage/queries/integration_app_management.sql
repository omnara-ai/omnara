-- App administration and project setup use the same stable inventory. The
-- project projection exposes only active shared apps or that project's apps.
-- name: ListIntegrationApps :many
SELECT id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  state, created_at, updated_at
FROM integration_apps
WHERE org_id = sqlc.arg(org_id) AND deleted_at IS NULL
  AND (sqlc.narg(eligible_project_id)::uuid IS NULL OR (
    state = 'active' AND (owner_project_id IS NULL OR owner_project_id = sqlc.narg(eligible_project_id))))
  AND (NOT sqlc.arg(cursor_set)::boolean
    OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- Physical provider identity and ownership remain immutable. Each supplied
-- configuration field is replaced independently so concurrent patches do not
-- overwrite unrelated fields. The revision trigger invalidates gateway caches.
-- name: UpdateIntegrationApp :one
UPDATE integration_apps
SET display_name = coalesce(sqlc.narg(display_name)::text, display_name),
  credential_secret_id = CASE WHEN sqlc.arg(set_credential)::boolean
    THEN sqlc.narg(credential_secret_id)::uuid ELSE credential_secret_id END,
  provider_config = coalesce(sqlc.narg(provider_config)::jsonb, provider_config),
  state = coalesce(sqlc.narg(state)::text, state)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id, org_id, owner_project_id, provider, provider_app_ref, display_name,
  connector_key, credential_secret_id, installation_credential_kind,
  provider_config, provider_metadata, configuration_revision, state,
  deleted_at, created_at, updated_at;

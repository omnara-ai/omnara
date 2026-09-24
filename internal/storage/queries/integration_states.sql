-- name: GetIntegrationState :one
SELECT id, project_id, integration_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at
FROM integration_states
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND kind = sqlc.arg(kind) AND id = sqlc.arg(id);

-- name: GetIntegrationStateByKey :one
SELECT id, project_id, integration_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at
FROM integration_states
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND kind = sqlc.arg(kind) AND key = sqlc.arg(key);

-- name: InsertIntegrationState :one
INSERT INTO integration_states(project_id, integration_id, kind, key, scope_kind, scope_ref, data, expires_at)
VALUES (sqlc.arg(project_id), sqlc.arg(integration_id), sqlc.arg(kind), sqlc.arg(key),
        sqlc.narg(scope_kind), sqlc.narg(scope_ref), sqlc.arg(data),
        CASE WHEN sqlc.arg(lifetime_milliseconds)::bigint > 0
             THEN statement_timestamp() + sqlc.arg(lifetime_milliseconds)::bigint * interval '1 millisecond' END)
RETURNING id, project_id, integration_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at;

-- name: ReplaceIntegrationState :one
UPDATE integration_states
SET data = sqlc.arg(data),
    revision = revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND kind = sqlc.arg(kind) AND id = sqlc.arg(id) AND revision = sqlc.arg(expected_revision)
  AND (NOT sqlc.arg(require_unexpired)::boolean OR expires_at IS NULL OR expires_at > statement_timestamp())
RETURNING id, project_id, integration_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at;

-- name: ExpireIntegrationState :exec
UPDATE integration_states
SET expires_at = statement_timestamp(), revision = revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND kind = sqlc.arg(kind) AND id = sqlc.arg(id) AND revision = sqlc.arg(expected_revision)
  AND (expires_at IS NULL OR expires_at > statement_timestamp());

-- name: GetIntegrationStateIntegrationID :one
SELECT integration_id FROM integration_states WHERE kind = sqlc.arg(kind) AND id = sqlc.arg(id);

-- name: CleanupDeletedIntegrationStates :execrows
WITH deleted_integrations AS MATERIALIZED (
    SELECT integration.project_id, integration.id
    FROM project_integrations integration
    JOIN projects project ON project.id = integration.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE integration.deleted_at IS NOT NULL OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
), candidates AS (
    SELECT obsolete.id
    FROM deleted_integrations integration
    CROSS JOIN LATERAL (
        SELECT state.id FROM integration_states state
        WHERE state.project_id = integration.project_id AND state.integration_id = integration.id
        ORDER BY state.kind, state.key
        LIMIT sqlc.arg(row_limit)
        FOR UPDATE SKIP LOCKED
    ) obsolete
    LIMIT sqlc.arg(row_limit)
)
DELETE FROM integration_states state USING candidates WHERE state.id = candidates.id;

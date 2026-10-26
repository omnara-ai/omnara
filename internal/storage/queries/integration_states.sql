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
    FROM integrations integration
    JOIN projects project ON project.id = integration.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE integration.deleted_at IS NOT NULL OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
), candidates AS (
    SELECT obsolete.id
    FROM deleted_integrations integration
    CROSS JOIN LATERAL (
        SELECT state.id FROM integration_states state
        WHERE state.project_id = integration.project_id AND state.integration_id = integration.id
          AND NOT EXISTS (
              SELECT 1 FROM integration_inbox inbox
              WHERE inbox.project_id = state.project_id AND inbox.integration_id = state.integration_id
                AND inbox.integration_state_id = state.id
          )
        ORDER BY state.kind, state.key
        LIMIT sqlc.arg(row_limit)
        FOR UPDATE SKIP LOCKED
    ) obsolete
    LIMIT sqlc.arg(row_limit)
)
DELETE FROM integration_states state USING candidates WHERE state.id = candidates.id;

-- name: MergeIntegrationState :exec
INSERT INTO integration_states(project_id, integration_id, kind, key, scope_kind, scope_ref, data)
VALUES (sqlc.arg(project_id), sqlc.arg(integration_id), sqlc.arg(kind), sqlc.arg(key),
        sqlc.narg(scope_kind), sqlc.narg(scope_ref), sqlc.arg(data))
ON CONFLICT (project_id, integration_id, kind, key) DO UPDATE
SET data = integration_states.data || excluded.data,
    revision = integration_states.revision + 1, updated_at = statement_timestamp()
WHERE sqlc.arg(force)::boolean OR NOT integration_states.data @> excluded.data;

-- name: ListActiveGitHubCheckRunStates :many
SELECT state.id, state.project_id, state.integration_id, state.kind, state.key, state.scope_kind, state.scope_ref,
       state.data, state.revision, state.expires_at, state.created_at, state.updated_at
FROM integration_states state
JOIN integrations integration ON integration.project_id = state.project_id AND integration.id = state.integration_id
WHERE state.kind = 'github_check_run' AND state.id > sqlc.arg(after_id)
  AND integration.state = 'active' AND integration.deleted_at IS NULL
ORDER BY state.id
LIMIT sqlc.arg(row_limit);

-- name: DeleteIntegrationState :execrows
DELETE FROM integration_states
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND kind = sqlc.arg(kind) AND id = sqlc.arg(id) AND revision = sqlc.arg(expected_revision);

-- name: MergeIntegrationStatesInScope :exec
UPDATE integration_states
SET data = data || sqlc.arg(data)::jsonb, revision = revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND integration_id = sqlc.arg(integration_id)
  AND kind = sqlc.arg(kind) AND scope_kind = sqlc.arg(scope_kind) AND scope_ref = sqlc.arg(scope_ref);

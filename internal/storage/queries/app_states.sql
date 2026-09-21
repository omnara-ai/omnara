-- These primitives compose inside the owning workflow's transaction. Callers
-- hold project/app lifecycle gates before mutation; updates fence stale reads.
-- Expired records remain readable for replay and recovery.
-- name: GetAppState :one
SELECT id, project_id, app_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at
FROM app_states
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND kind = sqlc.arg(kind) AND id = sqlc.arg(id);

-- name: GetAppStateByKey :one
SELECT id, project_id, app_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at
FROM app_states
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND kind = sqlc.arg(kind) AND key = sqlc.arg(key);

-- Zero lifetime means no deadline. Relative deadlines use the database clock.
-- name: InsertAppState :one
INSERT INTO app_states(project_id, app_id, kind, key, scope_kind, scope_ref, data, expires_at)
VALUES (sqlc.arg(project_id), sqlc.arg(app_id), sqlc.arg(kind), sqlc.arg(key),
        sqlc.narg(scope_kind), sqlc.narg(scope_ref), sqlc.arg(data),
        CASE WHEN sqlc.arg(lifetime_milliseconds)::bigint > 0
             THEN statement_timestamp() + sqlc.arg(lifetime_milliseconds)::bigint * interval '1 millisecond' END)
RETURNING id, project_id, app_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at;

-- Identity, scope and deadline are immutable through ordinary replacement.
-- A workflow can require its decision deadline still to be live at the write.
-- name: ReplaceAppState :one
UPDATE app_states
SET data = sqlc.arg(data),
    revision = revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND kind = sqlc.arg(kind) AND id = sqlc.arg(id) AND revision = sqlc.arg(expected_revision)
  AND (NOT sqlc.arg(require_unexpired)::boolean OR expires_at IS NULL OR expires_at > statement_timestamp())
RETURNING id, project_id, app_id, kind, key, scope_kind, scope_ref, data, revision, expires_at, created_at, updated_at;

-- Explicit expiry never erases the record or its replay identity.
-- name: ExpireAppState :exec
UPDATE app_states
SET expires_at = statement_timestamp(), revision = revision + 1, updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id) AND app_id = sqlc.arg(app_id)
  AND kind = sqlc.arg(kind) AND id = sqlc.arg(id) AND revision = sqlc.arg(expected_revision)
  AND (expires_at IS NULL OR expires_at > statement_timestamp());

-- Private callback routing only; verification and workflow authorization follow.
-- name: GetAppStateAppID :one
SELECT app_id FROM app_states WHERE kind = sqlc.arg(kind) AND id = sqlc.arg(id);

-- Deleted ownership reclaims every kind, regardless of its workflow deadline.
-- name: CleanupDeletedAppStates :execrows
WITH deleted_apps AS MATERIALIZED (
    SELECT app.project_id, app.id
    FROM project_apps app
    JOIN projects project ON project.id = app.project_id
    JOIN orgs org ON org.id = project.org_id
    WHERE app.deleted_at IS NOT NULL OR project.deleted_at IS NOT NULL OR org.deleted_at IS NOT NULL
), candidates AS (
    SELECT obsolete.id
    FROM deleted_apps app
    CROSS JOIN LATERAL (
        SELECT state.id FROM app_states state
        WHERE state.project_id = app.project_id AND state.app_id = app.id
        ORDER BY state.kind, state.key
        LIMIT sqlc.arg(row_limit)
        FOR UPDATE SKIP LOCKED
    ) obsolete
    LIMIT sqlc.arg(row_limit)
)
DELETE FROM app_states state USING candidates WHERE state.id = candidates.id;

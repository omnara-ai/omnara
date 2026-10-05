-- name: CreateMemoryStore :one
INSERT INTO memory_stores(id, project_id, name, description, agent_access)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(name), sqlc.arg(description), sqlc.arg(agent_access))
RETURNING id, project_id, name, description, agent_access, created_at, updated_at, deleted_at;

-- name: GetMemoryStore :one
SELECT id, project_id, name, description, agent_access, created_at, updated_at, deleted_at
FROM memory_stores
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: GetMemoryStoreByName :one
SELECT id, project_id, name, description, agent_access, created_at, updated_at, deleted_at
FROM memory_stores
WHERE project_id = sqlc.arg(project_id)
  AND name = sqlc.arg(name)
  AND deleted_at IS NULL;

-- name: LockMemoryStore :one
SELECT id, project_id, name, description, agent_access, created_at, updated_at, deleted_at
FROM memory_stores
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: UpdateMemoryStore :one
UPDATE memory_stores
SET description = sqlc.arg(description),
    agent_access = sqlc.arg(agent_access),
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
RETURNING id, project_id, name, description, agent_access, created_at, updated_at, deleted_at;

-- name: ListMemoryStores :many
SELECT id, project_id, name, description, agent_access, created_at, updated_at, deleted_at
FROM memory_stores
WHERE project_id = sqlc.arg(project_id)
  AND deleted_at IS NULL
  AND (sqlc.arg(name_pattern)::text = '' OR name ILIKE sqlc.arg(name_pattern)::text ESCAPE '\')
  AND (name COLLATE "C") > sqlc.arg(after_name)::text COLLATE "C"
ORDER BY name COLLATE "C"
LIMIT sqlc.arg(row_limit);

-- name: CountMemoryStores :one
SELECT count(*)
FROM memory_stores
WHERE project_id = sqlc.arg(project_id)
  AND deleted_at IS NULL;

-- name: DeleteMemoryStore :exec
UPDATE memory_stores
SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL;

-- name: LockAgentForMemoryWrite :one
SELECT id
FROM agents
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(agent_id)
FOR SHARE;

-- name: GetAgentMemoryConfig :one
SELECT coalesce(c.compiled_definition->'memory_stores', '[]'::jsonb)::jsonb AS memory_stores
FROM agents a
JOIN agent_configs c ON c.project_id = a.project_id AND c.id = a.current_config_id
WHERE a.project_id = sqlc.arg(project_id)
  AND a.id = sqlc.arg(agent_id)
  AND a.state = 'active';

-- name: LockMemoryStoreShared :one
SELECT id
FROM memory_stores
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND deleted_at IS NULL
FOR SHARE;

-- name: DeleteMemoryStoresForProjects :exec
UPDATE memory_stores
SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE project_id = ANY(sqlc.arg(project_ids)::uuid[])
  AND deleted_at IS NULL;

-- name: ListAttachedMemoryStores :many
SELECT s.id, s.name, s.description, s.agent_access, p.org_id
FROM memory_stores s
JOIN projects p ON p.id = s.project_id
WHERE s.project_id = sqlc.arg(project_id)
  AND s.id = ANY(sqlc.arg(store_ids)::uuid[])
  AND s.deleted_at IS NULL
  AND s.name >= sqlc.arg(from_name)::text COLLATE "C"
  AND (sqlc.arg(store_name)::text = '' OR s.name = sqlc.arg(store_name))
  AND s.name >= sqlc.arg(store_prefix)::text COLLATE "C"
  AND s.name < (sqlc.arg(store_prefix)::text || '{') COLLATE "C"
ORDER BY s.name COLLATE "C"
LIMIT sqlc.narg(row_limit)::integer;

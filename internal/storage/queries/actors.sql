-- name: UpsertActorIdentity :one
INSERT INTO actors(project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at)
VALUES (
  sqlc.arg(project_id), sqlc.arg(provider), sqlc.narg(provider_tenant_id),
  sqlc.arg(provider_user_id), NULLIF(sqlc.arg(display_name)::text, ''),
  sqlc.arg(metadata)::jsonb, transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT (project_id, provider, provider_tenant_id, provider_user_id) DO UPDATE
SET display_name = coalesce(excluded.display_name, actors.display_name),
    metadata = actors.metadata || excluded.metadata,
    updated_at = excluded.updated_at
WHERE coalesce(excluded.display_name, actors.display_name) IS DISTINCT FROM actors.display_name
   OR actors.metadata || excluded.metadata IS DISTINCT FROM actors.metadata
RETURNING id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at;

-- name: PutActor :one
INSERT INTO actors(project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at)
VALUES (
  sqlc.arg(project_id), 'external', sqlc.narg(provider_tenant_id),
  sqlc.arg(provider_user_id), NULLIF(sqlc.arg(display_name)::text, ''),
  sqlc.arg(metadata), transaction_timestamp(), transaction_timestamp()
)
ON CONFLICT (project_id, provider, provider_tenant_id, provider_user_id) DO UPDATE
SET display_name = CASE WHEN sqlc.arg(display_name_set)::boolean THEN excluded.display_name ELSE actors.display_name END,
    metadata = CASE WHEN sqlc.arg(metadata_set)::boolean THEN excluded.metadata ELSE actors.metadata END,
    updated_at = excluded.updated_at
WHERE (sqlc.arg(display_name_set)::boolean AND excluded.display_name IS DISTINCT FROM actors.display_name)
   OR (sqlc.arg(metadata_set)::boolean AND excluded.metadata IS DISTINCT FROM actors.metadata)
RETURNING id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at;

-- name: GetActorByIdentity :one
SELECT id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND provider = sqlc.arg(provider)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_user_id = sqlc.arg(provider_user_id);

-- name: GetActor :one
SELECT id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id);

-- name: ListActors :many
SELECT id, project_id, provider, provider_tenant_id, provider_user_id, display_name, metadata, created_at, updated_at
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND (sqlc.arg(provider)::text = '' OR provider = sqlc.arg(provider))
  AND (sqlc.arg(provider_tenant_id)::text = '' OR provider_tenant_id = sqlc.arg(provider_tenant_id))
  AND (sqlc.arg(provider_user_id)::text = '' OR provider_user_id = sqlc.arg(provider_user_id))
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(row_limit)::bigint;

-- name: ListActorDisplayNames :many
SELECT provider_user_id, coalesce(display_name, '') AS display_name
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND provider = sqlc.arg(provider)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_user_id = ANY(sqlc.arg(provider_user_ids)::text[])
  AND display_name IS NOT NULL;

-- name: UpdateActorDisplayName :execrows
UPDATE actors
SET display_name = sqlc.arg(display_name)::text,
    updated_at = statement_timestamp()
WHERE project_id = sqlc.arg(project_id)
  AND provider = sqlc.arg(provider)
  AND provider_tenant_id IS NOT DISTINCT FROM sqlc.narg(provider_tenant_id)
  AND provider_user_id = sqlc.arg(provider_user_id)
  AND display_name IS DISTINCT FROM sqlc.arg(display_name)::text;

-- name: ListActorIdentitiesByIDs :many
SELECT id, provider, provider_user_id
FROM actors
WHERE project_id = sqlc.arg(project_id)
  AND id = ANY(sqlc.arg(ids)::uuid[]);

-- name: GetLatestInputActor :one
SELECT a.id, a.provider, a.provider_user_id, i.queued_at
FROM agent_inputs i
JOIN actors a ON a.project_id = i.project_id AND a.id = i.actor_id
WHERE i.project_id = sqlc.arg(project_id)
  AND i.agent_id = sqlc.arg(agent_id)
  AND i.id = ANY(sqlc.arg(input_ids)::uuid[])
ORDER BY a.provider = 'omnara' AND starts_with(a.provider_user_id, sqlc.arg(agent_public_id_prefix)::text),
  i.queued_at DESC, i.id DESC
LIMIT 1;

-- name: GetLatestRootOpeningActorBefore :one
SELECT a.id, a.provider, a.provider_user_id
FROM agents g
JOIN agent_events e ON e.agent_id = g.root_agent_id AND e.is_opening_event
JOIN agent_inputs i ON i.project_id = g.project_id AND i.agent_id = e.agent_id AND i.id = e.agent_input_id
JOIN actors a ON a.project_id = i.project_id AND a.id = i.actor_id
WHERE g.project_id = sqlc.arg(project_id)
  AND g.id = sqlc.arg(agent_id)
  AND e.created_at <= sqlc.arg(opened_before)
  AND NOT (a.provider = 'omnara' AND starts_with(a.provider_user_id, sqlc.arg(agent_public_id_prefix)::text))
ORDER BY e.sequence DESC
LIMIT 1;
